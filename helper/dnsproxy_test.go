package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func testQuery(id uint16, name string, qtype uint16) []byte {
	pkt := []byte{byte(id >> 8), byte(id), 0x01, 0, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, label := range strings.Split(name, ".") {
		pkt = append(pkt, byte(len(label)))
		pkt = append(pkt, label...)
	}
	pkt = append(pkt, 0)
	pkt = binary.BigEndian.AppendUint16(pkt, qtype)
	return binary.BigEndian.AppendUint16(pkt, 1)
}

func dnsTestProxy(t *testing.T, handler func(*http.Request) (int, string)) (*magicDNSProxy, *int) {
	t.Helper()
	calls := 0
	b := &backend{client: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		code, body := handler(r)
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}}
	p := newMagicDNSProxy(b)
	p.state = filepath.Join(t.TempDir(), "magicdns.json")
	p.run = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	p.ctProbe = func(int) error { return syscall.ENOENT }
	p.sweep = func(bool) (int, error) { return 0, nil }
	return p, &calls
}

func TestWireHex(t *testing.T) {
	if got := wireHex("tail1234.ts.net"); got != "08 74 61 69 6c 31 32 33 34 02 74 73 03 6e 65 74 00" {
		t.Fatal(got)
	}
}

func TestDesiredRules(t *testing.T) {
	if v4, v6 := desiredRules("off", []string{"tail1234.ts.net"}); len(v4) != 0 || len(v6) != 0 {
		t.Fatal("off mode must not install rules")
	}
	v4, v6 := desiredRules("tailnet", []string{"tail1234.ts.net", "corp.example"})
	if len(v4) != 4 || len(v6) != 4 {
		t.Fatalf("got %d/%d rules", len(v4), len(v6))
	}
	for _, chain := range [][][]string{v4, v6} {
		if strings.Join(chain[0], " ") != markReturn || strings.Join(chain[1], " ") != saveMark {
			t.Fatalf("chain head %v", chain[:2])
		}
	}
	for i, c := range []struct {
		rule   []string
		window string
	}{{v4[2], "--from 40 --to 300"}, {v6[2], "--from 60 --to 320"}} {
		r := strings.Join(c.rule, " ")
		for _, want := range []string{"-m string --algo bm --icase " + c.window, "|08 74 61 69 6c 31 32 33 34 02 74 73 03 6e 65 74 00|", "-p udp -j REDIRECT --to-ports 1053"} {
			if !strings.Contains(r, want) {
				t.Fatalf("family %d rule %q lacks %q", i, r, want)
			}
		}
	}
	for _, mode := range []string{"exit-doh", "exit-passthrough", "passthrough-fallback"} {
		v4, v6 = desiredRules(mode, []string{"tail1234.ts.net"})
		// Link-local IPv6 servers stay on the system path: no zone to forward with.
		v6head := [][]string{v6[0], v6[1], v6[len(v6)-1]}
		for _, chain := range [][][]string{v4, v6head} {
			if len(chain) != 3 || strings.Join(chain[0], " ") != markReturn || strings.Join(chain[1], " ") != saveMark || strings.Join(chain[2], " ") != "-p udp -j REDIRECT --to-ports 1053" {
				t.Fatalf("%s rules %v %v", mode, v4, v6)
			}
		}
		if len(v6) != 4 || strings.Join(v6[2], " ") != linkLocalReturn {
			t.Fatalf("%s v6 rules %v", mode, v6)
		}
	}
}

const (
	markReturn = "-m mark --mark 0x10000000/0x1e000000 -j RETURN"
	saveMark   = "-j CONNMARK --save-mark --nfmask 0x1ffff --ctmask 0x1ffff"

	linkLocalReturn = "-d fe80::/10 -j RETURN"
)

func TestSelectMagicDNSMode(t *testing.T) {
	for _, c := range []struct {
		exit, doh, suffixes, noString, ct bool
		want                              string
	}{
		{false, false, false, false, true, "off"},
		{false, false, false, true, true, "off"},
		{false, false, true, false, true, "tailnet"},
		{false, false, true, true, true, "passthrough-fallback"},
		{true, true, true, false, true, "exit-doh"},
		{true, true, false, true, true, "exit-doh"},
		{true, false, true, false, true, "exit-passthrough"},
		{true, false, false, true, true, "exit-passthrough"},
		// Without conntrack lookups nothing is caught wholesale.
		{false, false, true, true, false, "off"},
		{true, false, true, false, false, "tailnet"},
		{true, false, true, true, false, "off"},
		{true, true, true, false, false, "exit-doh"},
	} {
		if got := selectMagicDNSMode(c.exit, c.doh, c.suffixes, c.noString, c.ct); got != c.want {
			t.Errorf("%+v: got %s", c, got)
		}
	}
}

func TestConntrackOrigDst(t *testing.T) {
	client := netip.MustParseAddrPort("192.168.1.5:34567")
	q := conntrackQuery(client)
	if int(binary.NativeEndian.Uint32(q)) != len(q) || binary.NativeEndian.Uint16(q[4:]) != 0x101 || q[16] != 2 {
		t.Fatalf("header %x", q[:20])
	}
	reply := nlFind(q[20:], ctaTupleReply)
	ip, proto := nlFind(reply, ctaTupleIP), nlFind(reply, ctaTupleProto)
	if string(nlFind(ip, ctaIPv4Src)) != "\x7f\x00\x00\x01" || string(nlFind(ip, ctaIPv4Dst)) != "\xc0\xa8\x01\x05" ||
		nlFind(proto, ctaProtoNum)[0] != 17 || binary.BigEndian.Uint16(nlFind(proto, ctaProtoSrcPort)) != 1053 ||
		binary.BigEndian.Uint16(nlFind(proto, ctaProtoDstPort)) != 34567 {
		t.Fatalf("query %x", q)
	}

	// A crafted IPCTNL_MSG_CT_NEW reply: orig 192.168.1.5:34567 -> 1.1.1.1:53.
	be := func(v uint16) []byte { return binary.BigEndian.AppendUint16(nil, v) }
	orig := nlAttr(ctaTupleOrig|nlaNested,
		nlAttr(ctaTupleIP|nlaNested, nlAttr(ctaIPv4Src, []byte{192, 168, 1, 5}), nlAttr(ctaIPv4Dst, []byte{1, 1, 1, 1})),
		nlAttr(ctaTupleProto|nlaNested, nlAttr(ctaProtoNum, []byte{17}), nlAttr(ctaProtoSrcPort, be(34567)), nlAttr(ctaProtoDstPort, be(53))))
	body := append([]byte{2, 0, 0, 0}, append(nlAttr(3, []byte{0, 0, 0, 8}), append(orig, append(reply, nlAttr(ctaMark, []byte{0, 1, 0, 0x64})...)...)...)...)
	msg := binary.NativeEndian.AppendUint32(nil, uint32(16+len(body)))
	msg = binary.NativeEndian.AppendUint16(msg, 0x100)
	msg = append(append(msg, make([]byte, 10)...), body...)
	if got, mark, err := parseConntrackOrigDst(msg); err != nil || got != netip.MustParseAddrPort("1.1.1.1:53") || mark != 0x10064 || passthroughMark(mark) != 0x10030064 {
		t.Fatalf("got %v %x %v", got, mark, err)
	}

	nlErr := binary.NativeEndian.AppendUint32(nil, 36)
	nlErr = binary.NativeEndian.AppendUint16(nlErr, 2)
	nlErr = append(nlErr, make([]byte, 10)...)
	nlErr = binary.NativeEndian.AppendUint32(nlErr, uint32(0xfffffffe)) // -ENOENT
	nlErr = append(nlErr, q[:16]...)
	if _, _, err := parseConntrackOrigDst(nlErr); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("error reply: %v", err)
	}
	// IPv6: reply tuple [::1]:1053 -> client, orig client -> 2001:db8:53::1:53.
	client6 := netip.MustParseAddrPort("[2001:db8::5]:34567")
	q6 := conntrackQuery(client6)
	reply6 := nlFind(q6[20:], ctaTupleReply)
	ip6 := nlFind(reply6, ctaTupleIP)
	if q6[16] != syscall.AF_INET6 || string(nlFind(ip6, ctaIPv6Src)) != string(netip.IPv6Loopback().AsSlice()) ||
		string(nlFind(ip6, ctaIPv6Dst)) != string(client6.Addr().AsSlice()) || nlFind(ip6, ctaIPv4Src) != nil ||
		binary.BigEndian.Uint16(nlFind(nlFind(reply6, ctaTupleProto), ctaProtoSrcPort)) != 1053 {
		t.Fatalf("v6 query %x", q6)
	}
	server6 := netip.MustParseAddr("2001:db8:53::1")
	orig6 := nlAttr(ctaTupleOrig|nlaNested,
		nlAttr(ctaTupleIP|nlaNested, nlAttr(ctaIPv6Src, client6.Addr().AsSlice()), nlAttr(ctaIPv6Dst, server6.AsSlice())),
		nlAttr(ctaTupleProto|nlaNested, nlAttr(ctaProtoNum, []byte{17}), nlAttr(ctaProtoSrcPort, be(34567)), nlAttr(ctaProtoDstPort, be(53))))
	body6 := append([]byte{10, 0, 0, 0}, append(orig6, append(reply6, nlAttr(ctaMark, []byte{0, 1, 0, 0x65})...)...)...)
	msg6 := binary.NativeEndian.AppendUint32(nil, uint32(16+len(body6)))
	msg6 = binary.NativeEndian.AppendUint16(msg6, 0x100)
	msg6 = append(append(msg6, make([]byte, 10)...), body6...)
	if got, mark, err := parseConntrackOrigDst(msg6); err != nil || got != netip.AddrPortFrom(server6, 53) || mark != 0x10065 {
		t.Fatalf("v6 got %v %x %v", got, mark, err)
	}

	for _, bad := range [][]byte{nil, msg[:19], msg[:len(msg)-4]} {
		if _, _, err := parseConntrackOrigDst(bad); err == nil {
			t.Fatalf("accepted %x", bad)
		}
	}
}

func TestAnswerPassthrough(t *testing.T) {
	server, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	go func() {
		buf := make([]byte, 512)
		n, addr, err := server.ReadFromUDP(buf)
		if err != nil {
			return
		}
		stray := reply(testQuery(1, "other.example", 1), 12, 0)
		_, _ = server.WriteToUDP(stray, addr)
		_, _ = server.WriteToUDP(reply(buf[:n], n, 3), addr)
	}()
	p, calls := dnsTestProxy(t, func(*http.Request) (int, string) { return 500, "" })
	p.suffix, p.mode = "tail1234.ts.net", "passthrough-fallback"
	client := netip.MustParseAddrPort("192.0.2.5:40000")
	p.origDst = func(c netip.AddrPort) (netip.AddrPort, uint32, error) {
		if c != client {
			t.Errorf("lookup for %v", c)
		}
		return server.LocalAddr().(*net.UDPAddr).AddrPort(), 0, nil
	}
	out := p.answer(context.Background(), testQuery(0x7777, "www.example.com", 1), client)
	if *calls != 0 || len(out) < 12 || out[0] != 0x77 || out[1] != 0x77 || out[3]&0x0f != 3 {
		t.Fatalf("calls=%d reply=%x", *calls, out)
	}
}

func TestDNSQuestion(t *testing.T) {
	pkt := testQuery(7, "Host.Tail1234.ts.net", 28)
	name, qtype, end, ok := dnsQuestion(pkt)
	if !ok || name != "host.tail1234.ts.net" || qtype != 28 || end != len(pkt) {
		t.Fatalf("%q %d %d %v", name, qtype, end, ok)
	}
	response := append([]byte(nil), pkt...)
	response[2] |= 0x80
	twoQuestions := append([]byte(nil), pkt...)
	twoQuestions[5] = 2
	for _, bad := range [][]byte{response, twoQuestions, pkt[:12], pkt[:len(pkt)-2]} {
		if _, _, _, ok := dnsQuestion(bad); ok {
			t.Fatalf("accepted %x", bad)
		}
	}
}

func TestReplyBuilder(t *testing.T) {
	pkt := testQuery(0xabcd, "a.example", 1)
	pkt[11] = 1 // ARCOUNT, must be cleared
	_, _, end, _ := dnsQuestion(pkt)
	out := reply(pkt, end, 2)
	if out[0] != 0xab || out[1] != 0xcd || out[2] != 0x81 || out[3] != 0x82 {
		t.Fatalf("header %x", out[:4])
	}
	if binary.BigEndian.Uint16(out[4:6]) != 1 || binary.BigEndian.Uint16(out[6:8]) != 0 || binary.BigEndian.Uint16(out[10:12]) != 0 {
		t.Fatalf("counts %x", out[4:12])
	}
	if string(out[12:]) != string(pkt[12:end]) {
		t.Fatal("question not kept")
	}
}

func TestAnswerLocal(t *testing.T) {
	p, calls := dnsTestProxy(t, func(r *http.Request) (int, string) {
		if r.URL.Path != "/localapi/v0/dns-query" || r.URL.Query().Get("name") != "host.tail1234.ts.net." || r.URL.Query().Get("type") != "AAAA" {
			t.Errorf("unexpected request %s", r.URL)
		}
		body, _ := json.Marshal(struct{ Bytes []byte }{reply(testQuery(1, "host.tail1234.ts.net", 28), 12, 2)})
		return 200, string(body)
	})
	p.suffix, p.mode = "tail1234.ts.net", "tailnet"
	out := p.answer(context.Background(), testQuery(0x4242, "host.tail1234.ts.net", 28), netip.AddrPort{})
	if out[0] != 0x42 || out[1] != 0x42 || out[3]&0x0f != 3 || out[2]&0x01 != 1 {
		t.Fatalf("reply %x", out)
	}
	out = p.answer(context.Background(), testQuery(5, "host.tail1234.ts.net", 65), netip.AddrPort{})
	if *calls != 1 || out[3]&0x0f != 0 || binary.BigEndian.Uint16(out[6:8]) != 0 {
		t.Fatalf("qtype 65 must be local NODATA: calls=%d reply=%x", *calls, out)
	}
}

func TestAnswerNoOriginalDstServfail(t *testing.T) {
	p, calls := dnsTestProxy(t, func(*http.Request) (int, string) { return 500, "" })
	p.suffix, p.mode = "tail1234.ts.net", "exit-passthrough"
	for _, dst := range []netip.AddrPort{{}, netip.MustParseAddrPort("127.0.0.1:1053")} {
		p.origDst = func(netip.AddrPort) (netip.AddrPort, uint32, error) {
			if !dst.IsValid() {
				return dst, 0, errors.New("conntrack: no such file or directory")
			}
			return dst, 0, nil
		}
		out := p.answer(context.Background(), testQuery(9, "www.example.com", 1), netip.MustParseAddrPort("192.0.2.5:40000"))
		if *calls != 0 || out[3]&0x0f != 2 {
			t.Fatalf("dst=%v calls=%d reply=%x", dst, *calls, out)
		}
	}
	bad := testQuery(9, "www.example.com", 1)
	bad[5] = 0
	if out := p.answer(context.Background(), bad, netip.AddrPort{}); out[3]&0x0f != 1 {
		t.Fatalf("FORMERR expected, got %x", out)
	}
}

func TestRefreshModes(t *testing.T) {
	running := true
	var commands []string
	p, _ := dnsTestProxy(t, func(r *http.Request) (int, string) {
		switch r.URL.Path + "?" + r.URL.RawQuery {
		case "/localapi/v0/status?peers=false":
			if !running {
				return 200, `{"BackendState":"Stopped"}`
			}
			return 200, `{"BackendState":"Running","CurrentTailnet":{"MagicDNSSuffix":"tail1234.ts.net"},"ExitNodeStatus":{"ID":"nExit"}}`
		case "/localapi/v0/status?peers=true":
			return 200, `{"Peer":{"k":{"ID":"nExit"}}}`
		case "/localapi/v0/prefs?":
			return 200, `{"ExitNodeID":"nExit"}`
		case "/localapi/v0/dns-config?":
			return 503, "no netmap"
		}
		return 404, ""
	})
	p.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		if strings.HasSuffix(name, "cmd") {
			return []byte("hostname\n"), nil
		}
		return nil, nil
	}
	p.refresh(context.Background())
	if p.mode != "exit-passthrough" || p.exitDoH != "" || p.privateDNS != "hostname" {
		t.Fatalf("mode=%s doh=%q private=%s", p.mode, p.exitDoH, p.privateDNS)
	}
	state := readMagicDNSState(p.state, time.Now())
	if state == nil || !state.Running || state.Mode != "exit-passthrough" || !state.Rules || !state.Rules6 || state.Upstream != "passthrough" ||
		state.Daemon != "Running" || state.ExitNode != "nExit" {
		t.Fatalf("state %+v", state)
	}
	for _, c := range commands {
		if strings.Contains(c, "iptables") && !strings.Contains(c, " shellscaled_dns") {
			t.Fatalf("command outside module chain: %s", c)
		}
	}
	running = false
	p.refresh(context.Background())
	if p.mode != "off" {
		t.Fatalf("mode=%s", p.mode)
	}
	if state := readMagicDNSState(p.state, time.Now()); state == nil || state.Daemon != "Stopped" || state.ExitNode != "" {
		t.Fatalf("state %+v", state)
	}
}

func TestApplyRulesFailureFlushes(t *testing.T) {
	p, _ := dnsTestProxy(t, func(*http.Request) (int, string) { return 500, "" })
	var flushes int
	p.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[3] == "-F" {
			flushes++
			return nil, nil
		}
		return []byte("Couldn't load match `string'"), errors.New("exit status 2")
	}
	v4, _ := desiredRules("tailnet", []string{"tail1234.ts.net"})
	p.applyRules(context.Background(), 0, v4)
	if p.rulesOK[0] || flushes != 2 {
		t.Fatalf("ok=%v flushes=%d", p.rulesOK[0], flushes)
	}
}

func TestRefreshExitProbeRetryAndRoutesGate(t *testing.T) {
	corpDNS, dohOK := false, false
	p, _ := dnsTestProxy(t, func(r *http.Request) (int, string) {
		switch r.URL.Path + "?" + r.URL.RawQuery {
		case "/localapi/v0/status?peers=false":
			return 200, `{"BackendState":"Running","CurrentTailnet":{"MagicDNSSuffix":"tail1234.ts.net"},"ExitNodeStatus":{"ID":"nExit"}}`
		case "/localapi/v0/status?peers=true":
			return 200, `{"Peer":{"k":{"ID":"nExit","PeerAPIURL":["http://100.64.0.2:1234"]}}}`
		case "/localapi/v0/prefs?":
			if corpDNS {
				return 200, `{"CorpDNS":true}`
			}
			return 200, `{"CorpDNS":false}`
		case "/localapi/v0/dns-config?":
			return 200, `{"Routes":{"corp.example.":[{"Addr":"10.0.0.53"}]}}`
		}
		return 404, ""
	})
	p.doh = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		if !dohOK {
			return nil, errors.New("timeout")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	})}
	p.refresh(context.Background())
	if p.mode != "exit-passthrough" || len(p.routes) != 0 {
		t.Fatalf("mode=%s routes=%d", p.mode, len(p.routes))
	}
	corpDNS, dohOK = true, true
	p.refresh(context.Background())
	if p.mode != "exit-doh" || len(p.routes) != 1 {
		t.Fatalf("retry: mode=%s routes=%d", p.mode, len(p.routes))
	}
	dohOK = false
	for i := 0; i < 6 && p.mode == "exit-doh"; i++ {
		p.refresh(context.Background())
	}
	if p.mode != "exit-passthrough" {
		t.Fatalf("lost DoH must fall back, mode=%s", p.mode)
	}
}

func TestRefreshStringFallback(t *testing.T) {
	p, _ := dnsTestProxy(t, func(r *http.Request) (int, string) {
		if r.URL.Path+"?"+r.URL.RawQuery == "/localapi/v0/status?peers=false" {
			return 200, `{"BackendState":"Running","CurrentTailnet":{"MagicDNSSuffix":"tail1234.ts.net"}}`
		}
		return 404, ""
	})
	matches := filepath.Join(t.TempDir(), "ip_tables_matches")
	old := ipTablesMatchesPath
	ipTablesMatchesPath = matches
	defer func() { ipTablesMatchesPath = old }()
	// A failure while the kernel lists xt_string is transient and retried.
	_ = os.WriteFile(matches, []byte("mark\nstring\n"), 0600)
	var appends []string
	p.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if len(args) > 3 && args[3] == "-A" {
			rule := strings.Join(args[5:], " ")
			appends = append(appends, rule)
			if strings.Contains(rule, "-m string") {
				return []byte("Couldn't load match `string'"), errors.New("exit status 2")
			}
		}
		return nil, nil
	}
	p.refresh(context.Background())
	if p.mode != "tailnet" || p.noString || p.rulesOK[0] {
		t.Fatalf("transient: mode=%s ok=%v", p.mode, p.rulesOK)
	}
	appends = nil
	p.refresh(context.Background())
	if len(appends) == 0 {
		t.Fatal("failed rules must be retried")
	}
	_ = os.WriteFile(matches, []byte("mark\n"), 0600)
	p.refresh(context.Background())
	if p.mode != "passthrough-fallback" || !p.rulesOK[0] || !p.rulesOK[1] {
		t.Fatalf("mode=%s ok=%v appends=%q", p.mode, p.rulesOK, appends)
	}
	appends = nil
	p.refresh(context.Background())
	if p.mode != "passthrough-fallback" || len(appends) != 0 {
		t.Fatalf("fallback must stick without reapplying: mode=%s appends=%q", p.mode, appends)
	}
	if state := readMagicDNSState(p.state, time.Now()); state == nil || state.Upstream != "passthrough" {
		t.Fatalf("state %+v", state)
	}
}

func TestRefreshWithoutConntrack(t *testing.T) {
	p, _ := dnsTestProxy(t, func(r *http.Request) (int, string) {
		if r.URL.Path+"?"+r.URL.RawQuery == "/localapi/v0/status?peers=false" {
			return 200, `{"BackendState":"Running","CurrentTailnet":{"MagicDNSSuffix":"tail1234.ts.net"},"ExitNodeStatus":{"ID":"nExit"}}`
		}
		return 404, ""
	})
	probes := 0
	p.ctProbe = func(int) error { probes++; return syscall.EPERM }
	p.refresh(context.Background())
	p.refresh(context.Background())
	if p.mode != "tailnet" || probes != 2 {
		t.Fatalf("mode=%s probes=%d", p.mode, probes)
	}
}

// One family's failing conntrack lookups narrow only that family's chain.
func TestRefreshConntrackPerFamily(t *testing.T) {
	for _, bad := range []int{0, 1} {
		p, _ := dnsTestProxy(t, func(r *http.Request) (int, string) {
			if r.URL.Path+"?"+r.URL.RawQuery == "/localapi/v0/status?peers=false" {
				return 200, `{"BackendState":"Running","CurrentTailnet":{"MagicDNSSuffix":"tail1234.ts.net"},"ExitNodeStatus":{"ID":"nExit"}}`
			}
			return 404, ""
		})
		p.ctProbe = func(family int) error {
			if family == bad {
				return syscall.EOPNOTSUPP
			}
			return syscall.ENOENT
		}
		chains := map[string][]string{}
		p.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
			if args[3] == "-A" {
				chains[name] = append(chains[name], strings.Join(args[5:], " "))
			}
			return nil, nil
		}
		p.refresh(context.Background())
		v4, v6 := chains["/system/bin/iptables"], chains["/system/bin/ip6tables"]
		good, narrowed := v4, v6
		if bad == 0 {
			good, narrowed = v6, v4
		}
		if p.mode != "exit-passthrough" || good[len(good)-1] != "-p udp -j REDIRECT --to-ports 1053" ||
			len(narrowed) != 3 || !strings.Contains(narrowed[2], "-m string") {
			t.Fatalf("bad=%d mode=%s v4=%q v6=%q", bad, p.mode, v4, v6)
		}
	}
}

// A dead [::1] listener hands IPv6 DNS back to the system path.
func TestRefreshDeadIPv6Listener(t *testing.T) {
	p, _ := dnsTestProxy(t, func(r *http.Request) (int, string) {
		if r.URL.Path+"?"+r.URL.RawQuery == "/localapi/v0/status?peers=false" {
			return 200, `{"BackendState":"Running","CurrentTailnet":{"MagicDNSSuffix":"tail1234.ts.net"}}`
		}
		return 404, ""
	})
	var v6 []string
	p.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if strings.HasSuffix(name, "ip6tables") {
			v6 = append(v6, strings.Join(args[3:], " "))
		}
		return nil, nil
	}
	p.refresh(context.Background())
	if !p.rulesOK[1] || len(v6) != 4 {
		t.Fatalf("ok=%v v6=%q", p.rulesOK, v6)
	}
	v6 = nil
	p.dead6.Store(true)
	p.refresh(context.Background())
	p.refresh(context.Background())
	if p.listen6 || p.rulesOK[1] || len(v6) != 1 || v6[0] != "-F "+magicDNSChain {
		t.Fatalf("listen6=%v ok=%v v6=%q", p.listen6, p.rulesOK, v6)
	}
	if state := readMagicDNSState(p.state, time.Now()); state == nil || state.Rules6 || !state.Rules {
		t.Fatalf("state %+v", state)
	}
}

func TestRefreshIPv6NAT(t *testing.T) {
	p, _ := dnsTestProxy(t, func(r *http.Request) (int, string) {
		if r.URL.Path+"?"+r.URL.RawQuery == "/localapi/v0/status?peers=false" {
			return 200, `{"BackendState":"Running","CurrentTailnet":{"MagicDNSSuffix":"tail1234.ts.net"},"ExitNodeStatus":{"ID":"nExit"}}`
		}
		return 404, ""
	})
	var v6 []string
	p.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if strings.HasSuffix(name, "ip6tables") {
			if args[1] != "-t" || args[2] != "nat" {
				t.Errorf("ip6tables outside nat: %v", args)
			}
			if args[3] == "-A" {
				v6 = append(v6, strings.Join(args[5:], " "))
			}
		}
		return nil, nil
	}
	p.refresh(context.Background())
	if p.mode != "exit-passthrough" || len(v6) != 4 || v6[0] != markReturn || v6[2] != linkLocalReturn || v6[3] != "-p udp -j REDIRECT --to-ports 1053" {
		t.Fatalf("mode=%s v6=%q", p.mode, v6)
	}
	if state := readMagicDNSState(p.state, time.Now()); state == nil || !state.Rules6 {
		t.Fatalf("state %+v", state)
	}

	// Without the [::1] listener the IPv6 chain is left untouched.
	q, _ := dnsTestProxy(t, func(r *http.Request) (int, string) {
		if r.URL.Path+"?"+r.URL.RawQuery == "/localapi/v0/status?peers=false" {
			return 200, `{"BackendState":"Running","CurrentTailnet":{"MagicDNSSuffix":"tail1234.ts.net"}}`
		}
		return 404, ""
	})
	q.listen6 = false
	q.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if strings.HasSuffix(name, "ip6tables") {
			t.Errorf("ip6tables without listener: %v", args)
		}
		return nil, nil
	}
	q.refresh(context.Background())
	if q.mode != "tailnet" || !q.rulesOK[0] || q.rulesOK[1] {
		t.Fatalf("mode=%s ok=%v", q.mode, q.rulesOK)
	}
}

func TestAnswerPassthroughIPv6(t *testing.T) {
	server, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback})
	if err != nil {
		t.Skip("no IPv6 loopback:", err)
	}
	defer server.Close()
	go func() {
		buf := make([]byte, 512)
		if n, addr, err := server.ReadFromUDP(buf); err == nil {
			_, _ = server.WriteToUDP(reply(buf[:n], n, 3), addr)
		}
	}()
	p, calls := dnsTestProxy(t, func(*http.Request) (int, string) { return 500, "" })
	p.suffix, p.mode = "tail1234.ts.net", "exit-passthrough"
	client := netip.MustParseAddrPort("[2001:db8::5]:40000")
	p.origDst = func(c netip.AddrPort) (netip.AddrPort, uint32, error) {
		if c != client {
			t.Errorf("lookup for %v", c)
		}
		return server.LocalAddr().(*net.UDPAddr).AddrPort(), 0x10064, nil
	}
	out := p.answer(context.Background(), testQuery(0x6666, "www.example.com", 28), client)
	if *calls != 0 || len(out) < 12 || out[0] != 0x66 || out[1] != 0x66 || out[3]&0x0f != 3 {
		t.Fatalf("calls=%d reply=%x", *calls, out)
	}
	// The loopback listener itself is never a forward target.
	p.origDst = func(netip.AddrPort) (netip.AddrPort, uint32, error) {
		return netip.MustParseAddrPort("[::1]:1053"), 0, nil
	}
	if out := p.answer(context.Background(), testQuery(1, "www.example.com", 28), client); out[3]&0x0f != 2 {
		t.Fatalf("reply %x", out)
	}
}

func TestApplyRulesRetry(t *testing.T) {
	p, _ := dnsTestProxy(t, func(*http.Request) (int, string) { return 500, "" })
	fail, runs := true, 0
	p.run = func(context.Context, string, ...string) ([]byte, error) {
		runs++
		if fail {
			return []byte("Another app is currently holding the xtables lock."), errors.New("exit status 4")
		}
		return nil, nil
	}
	v4, _ := desiredRules("exit-passthrough", nil)
	if p.applyRules(context.Background(), 0, v4) || p.rulesOK[0] {
		t.Fatal("failure reported as applied")
	}
	fail, runs = false, 0
	if !p.applyRules(context.Background(), 0, v4) || !p.rulesOK[0] || runs != 1+len(v4) {
		t.Fatalf("retry: ok=%v runs=%d", p.rulesOK[0], runs)
	}
	runs = 0
	if !p.applyRules(context.Background(), 0, v4) || runs != 0 {
		t.Fatalf("unchanged rules reapplied: runs=%d", runs)
	}
}

func TestAnswerDoHFailureFallsBack(t *testing.T) {
	server, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	go func() {
		buf := make([]byte, 512)
		if n, addr, err := server.ReadFromUDP(buf); err == nil {
			_, _ = server.WriteToUDP(reply(buf[:n], n, 0), addr)
		}
	}()
	p, _ := dnsTestProxy(t, func(*http.Request) (int, string) { return 500, "" })
	p.mode, p.exitDoH = "exit-doh", "http://100.64.0.2:1234/dns-query"
	p.doh = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("timeout")
	})}
	p.origDst = func(netip.AddrPort) (netip.AddrPort, uint32, error) {
		return server.LocalAddr().(*net.UDPAddr).AddrPort(), 0, nil
	}
	out := p.answer(context.Background(), testQuery(0x1234, "www.example.com", 1), netip.MustParseAddrPort("192.0.2.5:40000"))
	if len(out) < 12 || out[3]&0x0f != 0 {
		t.Fatalf("reply %x", out)
	}
}
