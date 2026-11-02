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
	"strings"
	"syscall"
	"testing"
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

// TestRouteLookupPortRule asks the kernel for real; it changes links and ip
// rules, so run it as root inside a disposable network namespace.
func TestRouteLookupPortRule(t *testing.T) {
	if os.Getenv("SHELLSCALED_NETLINK_TEST") != "1" {
		t.Skip("set SHELLSCALED_NETLINK_TEST=1 to run as root in a disposable network namespace with iproute2")
	}
	for _, cmd := range []string{
		"link add rtwan type dummy", "link set rtwan up", "addr add 10.9.0.1/24 dev rtwan",
		"link add rtts type dummy", "link set rtts up", "addr add 10.9.1.1/24 dev rtts",
		"route add default via 10.9.0.2 dev rtwan", "route add default via 10.9.1.2 dev rtts table 152",
		"rule add pref 152 ipproto tcp dport 8443 lookup 152",
	} {
		if out, err := exec.Command("ip", strings.Fields(cmd)...).CombinedOutput(); err != nil {
			t.Fatalf("ip %s: %v %s", cmd, err, out)
		}
	}
	fd, err := netlinkSocket(syscall.NETLINK_ROUTE)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	lookup := func(dport uint16) route {
		t.Helper()
		f := tcpFlow{src: netip.MustParseAddr("10.9.0.1"), dst: netip.MustParseAddr("192.0.2.1"), sport: 40000, dport: dport}
		buf := make([]byte, 4096)
		if err := syscall.Sendto(fd, routeReq(f), 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
			t.Fatal(err)
		}
		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			t.Fatal(err)
		}
		rt, err := parseRoute(buf[:n])
		if err != nil {
			t.Fatalf("dport %d: %v", dport, err)
		}
		return rt
	}
	wan, err1 := net.InterfaceByName("rtwan")
	ts, err2 := net.InterfaceByName("rtts")
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	if rt := lookup(8443); rt.oif != uint32(ts.Index) || rt.dstLen != 0 {
		t.Errorf("matching flow: %+v, want table 152 default via rtts (%d)", rt, ts.Index)
	}
	if rt := lookup(443); rt.oif != uint32(wan.Index) || rt.dstLen != 0 {
		t.Errorf("other port: %+v, want main default via rtwan (%d)", rt, wan.Index)
	}
}
