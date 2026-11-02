package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	const ts, wlan, tun, lo = 30, 20, 40, 1
	unicast := func(oif uint32, dstLen byte) route { return route{syscall.RTN_UNICAST, dstLen, oif} }
	for _, c := range []struct {
		name            string
		tsSrc           bool
		rt              route
		switched, stale bool
	}{
		{"physical source into exit default route", false, unicast(ts, 0), false, true},
		{"tailnet source on exit default route", true, unicast(ts, 0), false, false},
		{"tailnet source after exit node removed", true, unicast(wlan, 0), false, true},
		{"physical source on wlan", false, unicast(wlan, 0), false, false},
		{"tailnet source on accepted subnet route", true, unicast(ts, 24), false, false},
		{"tailnet source on allow-LAN throw", true, unicast(wlan, 24), false, true},
		{"physical source on allow-LAN throw", false, unicast(wlan, 24), false, false},
		{"other VPN uid range", false, unicast(tun, 0), false, false},
		{"own interface address", false, route{syscall.RTN_LOCAL, 32, lo}, false, false},
		{"tailnet source to own wlan address", true, route{syscall.RTN_LOCAL, 32, wlan}, false, false},
		{"switched: exit default route", true, unicast(ts, 0), true, true},
		{"switched: accepted subnet route", true, unicast(ts, 24), true, false},
		{"switched: physical source on wlan", false, unicast(wlan, 0), true, false},
	} {
		if got := classify(c.tsSrc, c.rt, ts, c.switched); got != c.stale {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func testInetDiag(family byte, src, dst netip.Addr, mark []byte) []byte {
	msg := []byte{family, 1, 0, 0}
	msg = binary.BigEndian.AppendUint16(msg, 50000)
	msg = binary.BigEndian.AppendUint16(msg, 443)
	for _, a := range []netip.Addr{src, dst} {
		b := make([]byte, 16)
		copy(b, a.AsSlice())
		msg = append(msg, b...)
	}
	msg = binary.NativeEndian.AppendUint32(msg, 3)            // interface
	msg = binary.NativeEndian.AppendUint64(msg, 0x1122334455) // cookie
	msg = append(msg, make([]byte, 12)...)                    // expires, queues
	msg = binary.NativeEndian.AppendUint32(msg, 10123)        // uid
	msg = append(msg, make([]byte, 4)...)                     // inode
	msg = append(msg, nlAttr(1, make([]byte, 16))...)         // INET_DIAG_MEMINFO
	if mark != nil {
		msg = append(msg, nlAttr(inetDiagMark, mark)...)
	}
	return msg
}

func TestInetDiagEncoding(t *testing.T) {
	dump := inetDiagReq(sockDiagByFamily, 0x301, afInet6, tcpLiveStates, make([]byte, 48))
	if len(dump) != 72 || binary.NativeEndian.Uint32(dump) != 72 || binary.NativeEndian.Uint16(dump[4:]) != 20 ||
		binary.NativeEndian.Uint16(dump[6:]) != 0x301 || dump[16] != 10 || dump[17] != 6 || dump[18] != 0 ||
		binary.NativeEndian.Uint32(dump[20:]) != 0xb3e {
		t.Fatalf("dump request % x", dump)
	}

	mark := binary.NativeEndian.AppendUint32(nil, 0x10064)
	diag := testInetDiag(afInet, netip.MustParseAddr("192.168.1.23"), netip.MustParseAddr("1.1.1.1"), mark)
	f, ok := parseInetDiag(diag)
	if !ok || f != (tcpFlow{netip.MustParseAddr("192.168.1.23"), netip.MustParseAddr("1.1.1.1"), 0x10064, 10123, 3, 50000, 443}) {
		t.Fatalf("parsed %+v %v", f, ok)
	}
	req := destroyReq(diag)
	if len(req) != 72 || binary.NativeEndian.Uint16(req[4:]) != 21 || binary.NativeEndian.Uint16(req[6:]) != 5 ||
		req[16] != 2 || req[17] != 6 || binary.NativeEndian.Uint32(req[20:]) != tcpLiveStates || !bytes.Equal(req[24:], diag[4:52]) {
		t.Fatalf("destroy request % x", req)
	}
	if binary.BigEndian.Uint16(req[24:]) != 50000 || binary.BigEndian.Uint16(req[26:]) != 443 {
		t.Fatalf("ports % x", req[24:28])
	}

	diag6 := testInetDiag(afInet6, netip.MustParseAddr("::ffff:192.168.1.23"), netip.MustParseAddr("::ffff:1.1.1.1"), mark)
	if f, ok := parseInetDiag(diag6); !ok || f.src.String() != "192.168.1.23" || f.dst.String() != "1.1.1.1" {
		t.Fatalf("parsed v4-mapped %+v %v", f, ok)
	}
	if _, ok := parseInetDiag(testInetDiag(afInet6, f.src, f.dst, nil)); ok {
		t.Fatal("a socket without a mark must not be classified")
	}
	if _, ok := parseInetDiag(diag[:71]); ok {
		t.Fatal("short message parsed")
	}
}

func TestRouteEncoding(t *testing.T) {
	f := tcpFlow{netip.MustParseAddr("100.101.102.103"), netip.MustParseAddr("192.168.50.7"), 0xc0064, 10123, 0, 50000, 443}
	req := routeReq(f)
	if binary.NativeEndian.Uint32(req) != uint32(len(req)) || binary.NativeEndian.Uint16(req[4:]) != syscall.RTM_GETROUTE ||
		binary.NativeEndian.Uint16(req[6:]) != syscall.NLM_F_REQUEST || req[16] != afInet || binary.NativeEndian.Uint32(req[24:]) != rtmFFibMatch {
		t.Fatalf("route request % x", req)
	}
	attrs := req[28:]
	if !bytes.Equal(nlFind(attrs, syscall.RTA_DST), f.dst.AsSlice()) || !bytes.Equal(nlFind(attrs, syscall.RTA_SRC), f.src.AsSlice()) ||
		binary.NativeEndian.Uint32(nlFind(attrs, rtaMark)) != 0xc0064 || binary.NativeEndian.Uint32(nlFind(attrs, rtaUID)) != 10123 ||
		nlFind(attrs, syscall.RTA_OIF) != nil {
		t.Fatalf("route attributes % x", attrs)
	}
	// RTA_IP_PROTO is a u8, RTA_SPORT and RTA_DPORT are __be16.
	if !bytes.Equal(nlFind(attrs, rtaIPProto), []byte{syscall.IPPROTO_TCP}) || !bytes.Equal(nlFind(attrs, rtaSport), []byte{0xc3, 0x50}) ||
		!bytes.Equal(nlFind(attrs, rtaDport), []byte{0x01, 0xbb}) {
		t.Fatalf("route protocol and ports % x", attrs)
	}
	f.src, f.dst, f.oif = netip.MustParseAddr("fe80::1"), netip.MustParseAddr("fe80::2"), 7
	if req := routeReq(f); req[16] != afInet6 || binary.NativeEndian.Uint32(nlFind(req[28:], syscall.RTA_OIF)) != 7 {
		t.Fatalf("bound v6 route request % x", req)
	}

	reply := binary.NativeEndian.AppendUint32(nil, 0)
	reply = binary.NativeEndian.AppendUint16(reply, syscall.RTM_NEWROUTE)
	reply = append(reply, make([]byte, 10)...)
	reply = append(reply, afInet, 24, 0, 0, 52, 0, 0, syscall.RTN_UNICAST, 0, 0, 0, 0)
	reply = append(reply, nlAttr(syscall.RTA_OIF, binary.NativeEndian.AppendUint32(nil, 30))...)
	if rt, err := parseRoute(reply); err != nil || rt != (route{syscall.RTN_UNICAST, 24, 30}) {
		t.Fatalf("parsed %+v %v", rt, err)
	}
	errMsg := binary.NativeEndian.AppendUint32(nil, 36)
	errMsg = binary.NativeEndian.AppendUint16(errMsg, 2)
	errMsg = append(errMsg, make([]byte, 10)...)
	errMsg = append(binary.NativeEndian.AppendUint32(errMsg, uint32(0xffffffff-uint32(syscall.ENETUNREACH)+1)), make([]byte, 16)...)
	if _, err := parseRoute(errMsg); err != syscall.ENETUNREACH {
		t.Fatalf("lookup error %v", err)
	}
}

func TestNLErrno(t *testing.T) {
	msg := func(typ uint16, errno int32) []byte {
		b := binary.NativeEndian.AppendUint32(nil, 36)
		b = binary.NativeEndian.AppendUint16(b, typ)
		b = append(b, make([]byte, 10)...)
		return append(binary.NativeEndian.AppendUint32(b, uint32(errno)), make([]byte, 16)...)
	}
	if err := nlErrno(msg(2, 0)); err != nil {
		t.Fatal(err)
	}
	if err := nlErrno(msg(2, -2)); err != syscall.ENOENT {
		t.Fatal(err)
	}
	if nlErrno(msg(3, 0)) == nil || nlErrno(msg(2, 0)[:19]) == nil {
		t.Fatal("non-error message accepted")
	}
}

func TestRefreshSweepsOnRoutingChange(t *testing.T) {
	defer func(old string) { exitNodeIPv6LocalFile = old }(exitNodeIPv6LocalFile)
	exitNodeIPv6LocalFile = filepath.Join(t.TempDir(), "exit-node-ipv6-local")
	status, prefs := `{"BackendState":"Running"}`, `{"ExitNodeID":"nExit","ExitNodeAllowLANAccess":true}`
	p, _ := dnsTestProxy(t, func(r *http.Request) (int, string) {
		switch r.URL.Path + "?" + r.URL.RawQuery {
		case "/localapi/v0/status?peers=false":
			return 200, status
		case "/localapi/v0/prefs?":
			return 200, prefs
		}
		return 404, ""
	})
	var swept []bool
	p.sweep = func(switched bool) (int, error) { swept = append(swept, switched); return 0, nil }
	step := func(want ...bool) {
		t.Helper()
		swept = nil
		p.refresh(context.Background())
		if !slices.Equal(swept, want) {
			t.Fatalf("swept %v, want %v", swept, want)
		}
	}
	step(false) // first observation
	step()
	if err := os.WriteFile(exitNodeIPv6LocalFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	step(false)
	prefs = `{"ExitNodeID":"nOther","ExitNodeAllowLANAccess":true}`
	step(true) // exit node switched
	prefs = "x"
	step() // prefs unknown
	status = "x"
	step() // status unknown
	status = `{"BackendState":"Stopped"}`
	step(false)
	status, prefs = `{"BackendState":"Running"}`, `{"ExitNodeAllowLANAccess":true}`
	step() // allow-LAN without an exit node changes nothing
	prefs = `{"ExitNodeID":"nExit"}`
	step(false) // selecting an exit node is not a switch
}

func TestFlowRepairIsIndependentOfMagicDNSControl(t *testing.T) {
	for _, invalidRequest := range []bool{false, true} {
		t.Run(map[bool]string{false: "DNS-disabled", true: "invalid-DNS-request"}[invalidRequest], func(t *testing.T) {
			p := dnsControlProxy(t)
			if invalidRequest {
				if err := os.WriteFile(magicDNSEnabledFile, []byte(`{"enabled":true}`), 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := requestMagicDNS(context.Background(), false); err != nil {
				t.Fatal(err)
			}
			sweeps := 0
			p.sweep = func(bool) (int, error) {
				sweeps++
				ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
				defer cancel()
				if err := withMagicDNSControl(ctx, func() error { return nil }); err != nil {
					t.Errorf("flow repair retained the DNS request lock: %v", err)
				}
				return 0, nil
			}
			p.refresh(context.Background())
			if sweeps != 1 {
				t.Fatalf("DNS state prevented independent route observation: %d sweeps", sweeps)
			}
		})
	}
}

// Native netlink keeps this policy regression runnable with Android stock ip.
// The fixture refuses init's network namespace before changing any links.
func routeTestExchange(fd int, req []byte) ([]byte, error) {
	if err := syscall.Sendto(fd, req, 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return nil, err
	}
	buf := make([]byte, 64<<10)
	n, _, err := syscall.Recvfrom(fd, buf, 0)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func routeTestRequest(typ, flags uint16, body, attrs []byte) []byte {
	b := binary.NativeEndian.AppendUint32(nil, uint32(16+len(body)+len(attrs)))
	b = binary.NativeEndian.AppendUint16(b, typ)
	b = binary.NativeEndian.AppendUint16(b, flags)
	b = append(b, make([]byte, 8)...)
	b = append(b, body...)
	return append(b, attrs...)
}

func routeTestFixture(t *testing.T) (fd, wanIndex, tsIndex int) {
	t.Helper()
	if os.Getenv("SHELLSCALED_NETLINK_TEST") != "1" {
		t.Skip("requires opt-in inside a disposable Android network namespace")
	}
	self, err1 := os.Readlink("/proc/self/ns/net")
	init, err2 := os.Readlink("/proc/1/ns/net")
	if err1 != nil || err2 != nil || self == init {
		t.Fatal("refusing non-isolated network namespace")
	}
	ip := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("/system/bin/ip", args...).CombinedOutput(); err != nil {
			t.Fatalf("ip %v: %v: %s", args, err, out)
		}
	}
	for i, name := range []string{"scflowwan", "scflowts"} {
		ip("link", "add", name, "type", "dummy")
		t.Cleanup(func() {
			if out, err := exec.Command("/system/bin/ip", "link", "del", name).CombinedOutput(); err != nil {
				t.Errorf("remove fixture link: %v %s", err, out)
			}
		})
		ip("link", "set", name, "up")
		ip("addr", "add", []string{"10.9.0.1/24", "10.9.1.1/24"}[i], "dev", name)
	}
	ip("route", "add", "default", "via", "10.9.0.2", "dev", "scflowwan")
	ip("route", "add", "default", "via", "10.9.1.2", "dev", "scflowts", "table", "152")
	wan, err := net.InterfaceByName("scflowwan")
	if err != nil {
		t.Fatal(err)
	}
	ts, err := net.InterfaceByName("scflowts")
	if err != nil {
		t.Fatal(err)
	}
	fd, err = netlinkSocket(syscall.NETLINK_ROUTE)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	// fib_rule_hdr: AF_INET, table 152, FR_ACT_TO_TBL. These constants
	// and host-order ranges come from Linux uapi/linux/fib_rules.h.
	body := []byte{afInet, 0, 0, 0, 152, 0, 0, 1, 0, 0, 0, 0}
	attrs := nlAttr(6, binary.NativeEndian.AppendUint32(nil, 152))    // FRA_PRIORITY
	attrs = append(attrs, nlAttr(22, []byte{syscall.IPPROTO_TCP})...) // FRA_IP_PROTO
	ports := binary.NativeEndian.AppendUint16(nil, 8443)
	ports = binary.NativeEndian.AppendUint16(ports, 8443)
	attrs = append(attrs, nlAttr(24, ports)...) // FRA_DPORT_RANGE
	uids := binary.NativeEndian.AppendUint32(nil, 10000)
	uids = binary.NativeEndian.AppendUint32(uids, 10000)
	attrs = append(attrs, nlAttr(20, uids)...)      // FRA_UID_RANGE
	add := routeTestRequest(32, 0x605, body, attrs) // NEW, REQUEST|ACK|CREATE|EXCL
	reply, err := routeTestExchange(fd, add)
	if err == nil {
		err = nlErrno(reply)
	}
	if err != nil {
		t.Fatalf("add native TCP dport/UID rule: %v", err)
	}
	t.Cleanup(func() {
		reply, err := routeTestExchange(fd, routeTestRequest(33, 5, body, attrs))
		if err == nil {
			err = nlErrno(reply)
		}
		if err != nil {
			t.Errorf("remove native fixture rule: %v", err)
		}
	})
	return fd, wan.Index, ts.Index
}

func routeTestLookup(t *testing.T, fd int, f tcpFlow) route {
	t.Helper()
	req := routeReq(f)
	reply, err := routeTestExchange(fd, req)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := parseRoute(reply)
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

func TestRouteLookupPortRule(t *testing.T) {
	fd, wan, ts := routeTestFixture(t)
	f := tcpFlow{src: netip.MustParseAddr("10.9.0.1"), dst: netip.MustParseAddr("192.0.2.1"), uid: 10000, sport: 40000, dport: 8443}
	for _, tc := range []struct {
		port uint16
		uid  uint32
		want int
	}{{8443, 10000, ts}, {443, 10000, wan}, {8443, 10001, wan}} {
		f.dport, f.uid = tc.port, tc.uid
		if rt := routeTestLookup(t, fd, f); rt.oif != uint32(tc.want) || rt.dstLen != 0 {
			t.Fatalf("dport %d UID %d: route %+v, want ifindex %d", tc.port, tc.uid, rt, tc.want)
		}
	}
	// Negative control: remove only the new protocol/port attrs from the
	// real request. The same 8443/UID10000 flow must miss the TCP port rule.
	f.dport, f.uid = 8443, 10000
	req := routeReq(f)
	clean := append([]byte(nil), req[:28]...)
	for attrs := req[28:]; len(attrs) >= 4; {
		size := int(binary.NativeEndian.Uint16(attrs))
		if size < 4 || size > len(attrs) {
			t.Fatal("bad request attrs")
		}
		aligned := (size + 3) &^ 3
		if aligned > len(attrs) {
			t.Fatal("short aligned attr")
		}
		typ := binary.NativeEndian.Uint16(attrs[2:]) &^ 0xc000
		if typ != rtaIPProto && typ != rtaSport && typ != rtaDport {
			clean = append(clean, attrs[:aligned]...)
		}
		attrs = attrs[aligned:]
	}
	binary.NativeEndian.PutUint32(clean, uint32(len(clean)))
	reply, err := routeTestExchange(fd, clean)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := parseRoute(reply)
	if err != nil || rt.oif != uint32(wan) {
		t.Fatalf("negative control: route %+v err %v", rt, err)
	}
	t.Log("TCP dport and UID policy match; removing protocol/ports misses")
}
