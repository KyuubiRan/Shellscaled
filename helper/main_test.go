package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestCommandBoundary(t *testing.T) {
	allowed := []string{"tailscale status", "tailscale status --json", "tailscale ip -6", "tailscale version", "tailscale netcheck", "tailscale ping --help", "tailscale ping --c=2 --timeout=1s example-peer", "tailscale up", "tailscale down", "tailscale login", "tailscale set --accept-routes=false --shields-up=true", "tailscale set --exit-node= --netfilter-mode=off --accept-dns=false", "tailscaled --version", "tailscaled --help"}
	for _, text := range allowed {
		if _, err := validateCommand(text); err != nil {
			t.Errorf("allowed command rejected %q: %v", text, err)
		}
	}
	denied := []string{
		"", "tailscale", "id", "am start https://example.test", "tailscale console", "tailscale devices", "tailscale about", "sh -c tailscale", "tailscalex status", "/system/bin/tailscale status", "env tailscale status", "TS_BE_CLI=1 tailscale status",
		"tailscale status;id", "tailscale status && id", "tailscale status | cat", "tailscale status > /data/local/tmp/x", "tailscale status < x", "tailscale status\nid", "tailscale status\rid", "tailscale status\x00id", "tailscale status\tid",
		"tailscale ping $(id)", "tailscale ping `id`", "tailscale ping ${PATH}", "tailscale ping \\host", "tailscale ping 'host'", "tailscale ping \"host\"", "tailscale status *",
		"tailscale --socket=/other status", "tailscale status --socket=/other", "tailscale ssh example-peer id", "tailscale debug local-creds", "tailscale file get /data/adb", "tailscale configure sysext", "tailscale update", "tailscale logout", "tailscale set --auth-key=file:/data/adb/key", "tailscale set --operator=root", "tailscale up --login-server=http://example.com", "tailscale set --accept-dns=true", "tailscale set --netfilter-mode=on", "tailscale set --hostname=foo/bar",
		"tailscale ping --c=0 example-peer", "tailscale ping --c=999 example-peer", "tailscale ping --timeout=1h example-peer", "tailscale ping peer1 peer2", "tailscale ping", "tailscale netcheck --every=1s", "tailscaled", "tailscaled --cleanup", "tailscaled --state=/other", "tailscaled --version --help", "tailscale status " + strings.Repeat("x", 2048),
	}
	for _, text := range denied {
		if _, err := validateCommand(text); err == nil {
			t.Errorf("unsafe command accepted: %q", text)
		}
	}
}

func TestRPCRejectsUnknownOrMixedRequests(t *testing.T) {
	encode := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	for _, raw := range []string{`{"action":"status"}`, `{"action":"status","locale":"system"}`, `{"action":"status","locale":"en"}`, `{"action":"status","locale":"zh-CN"}`, `{"action":"status","locale":"zh-TW"}`, `{"action":"binary-channel","channel":"stable"}`, `{"action":"binary-channel","channel":"preview","locale":"en"}`, `{"action":"binary-check"}`, `{"action":"binary-update","locale":"en"}`, `{"action":"devices"}`, `{"action":"console","locale":"en"}`, `{"action":"about"}`, `{"action":"about","locale":"en"}`, `{"action":"up"}`, `{"action":"down"}`, `{"action":"settings","settings":{"acceptRoutes":false}}`, `{"action":"command","command":"tailscale status"}`, `{"action":"exit-node","exitNode":"100.64.0.2"}`, `{"action":"exit-node","exitNode":""}`, `{"action":"advertise","advertiseRoutes":"10.0.0.0/8","advertiseExitNode":true}`, `{"action":"advertise","advertiseRoutes":""}`, `{"action":"taildrop-path","path":"/sdcard/Download"}`} {
		if _, err := decodeRequest(encode(raw)); err != nil {
			t.Errorf("valid request rejected: %v", err)
		}
	}
	for _, raw := range []string{`{"action":"binary-channel"}`, `{"action":"binary-channel","channel":"prerelease"}`, `{"action":"binary-channel","channel":"../../other"}`, `{"action":"binary-channel","channel":"preview","command":""}`, `{"action":"binary-channel","channel":"stable","settings":null}`, `{"action":"binary-update","channel":"preview"}`, `{"action":"binary-check","channel":null}`, `{"action":"status","channel":"stable"}`, `{"action":"status","Channel":"stable"}`, `{"action":"status","command":""}`, `{"action":"binary-check","command":""}`, `{"action":"binary-update","settings":null}`, `{"action":"binary-check","url":"https://example.test"}`, `{"action":"binary-update","command":"id"}`, `{"action":"binary-update","settings":{"shieldsUp":true}}`, `{"action":"binary-check","version":"1.2.3"}`, `{"action":"console","command":"id"}`, `{"action":"console","url":"https://example.test"}`, `{"action":"console","settings":{"shieldsUp":true}}`, `{"action":"devices","command":"tailscale status"}`, `{"action":"devices","settings":{"shieldsUp":true}}`, `{"action":"devices","peers":true}`, `{"action":"exec","command":"id"}`, `{"action":"status","locale":"../../other"}`, `{"action":"status","locale":"fr"}`, `{"action":"status","command":"id"}`, `{"action":"settings","settings":{"acceptDNS":true}}`, `{"action":"settings"}`, `{"action":"command","command":"tailscale status","settings":{"shieldsUp":true}}`, `{"action":"status","env":{"PATH":"/evil"}}`, `{"action":"status"} {"action":"up"}`, `{"action":"exit-node","exitNode":"100.64.0.2","command":""}`, `{"action":"advertise","advertiseRoutes":"0.0.0.0/0"}`, `{"action":"advertise"}`, `{"action":"taildrop-path","path":"/data/adb"}`, `null`, `[]`} {
		if _, err := decodeRequest(encode(raw)); err == nil {
			t.Errorf("invalid request accepted: %s", raw)
		}
	}
	for _, value := range []string{"not-base64!", strings.Repeat("a", 8193)} {
		if _, err := decodeRequest(value); err == nil {
			t.Error("bad encoding accepted")
		}
	}
}

func TestApplicationLocales(t *testing.T) {
	for _, tc := range []struct{ input, want string }{{"zh-CN", "zh-CN"}, {"zh-Hans-SG", "zh-CN"}, {"zh_Hant_HK", "zh-TW"}, {"zh-TW", "zh-TW"}, {"en-GB", "en"}, {"de-DE", "en"}} {
		if got := normalizeLocale(tc.input); got != tc.want {
			t.Errorf("normalizeLocale(%q) = %q", tc.input, got)
		}
	}
	for _, locale := range []string{"zh-CN", "zh-TW", "en"} {
		for key, values := range messages {
			if values[0] == "" || values[1] == "" || values[2] == "" || tr(key, locale) == "" {
				t.Errorf("missing translation for %q", key)
			}
		}
		if description("Running", locale) == description("Stopped", locale) {
			t.Error("connection descriptions must distinguish up and down")
		}
		if !strings.Contains(localizedError("未开放参数 --socket", locale), "--socket") {
			t.Error("unsupported option must remain identifiable")
		}
		if strings.Contains(localizedError("LocalAPI 500: private-marker", locale), "private-marker") {
			t.Error("raw service error leaked into user-facing message")
		}
	}
	if stateLabel("Running", "en") != "Connected" || stateLabel("Running", "zh-TW") != "已連線" {
		t.Error("wrong translated state label")
	}
}

func TestTaildropPathValidation(t *testing.T) {
	for _, path := range []string{
		"/storage/emulated/0/Download/Tailscale",
		"/sdcard/Download/Tailscale",
		"/storage/emulated/0/Download",
		"/storage/emulated/0/Documents/Incoming",
		"/sdcard/Download",
	} {
		if !validTaildropPath(path) {
			t.Errorf("valid Taildrop path rejected: %q", path)
		}
	}
	for _, path := range []string{
		"",
		"relative/Download",
		"/data/adb/other",
		"/storage/emulated/0/Download/../Pictures",
		"/storage/emulated/0/Download/../../data/adb",
	} {
		if validTaildropPath(path) {
			t.Errorf("unsafe Taildrop path accepted: %q", path)
		}
	}
}

func TestAwaitTaildropFileEventAndCancellation(t *testing.T) {
	for _, cancelWait := range []bool{false, true} {
		t.Run(fmt.Sprint("cancel=", cancelWait), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started, arrival := make(chan struct{}), make(chan struct{})
			b := &backend{client: &http.Client{Timeout: 5 * time.Second, Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" || r.URL.Path != "/localapi/v0/files/" || r.URL.RawQuery != "waitsec=3600" {
					t.Error("expected the daemon's blocking file-arrival endpoint")
				}
				if deadline, ok := r.Context().Deadline(); !ok || time.Until(deadline) < time.Hour {
					t.Error("ordinary RPC timeout interrupted the file wait")
				}
				close(started)
				select {
				case <-arrival:
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`[{"Name":"test.txt"}]`)), Header: make(http.Header)}, nil
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
			})}}
			done := make(chan struct{})
			var pending bool
			var err error
			go func() {
				pending, err = b.awaitTaildrop(ctx)
				close(done)
			}()
			<-started
			select {
			case <-done:
				t.Fatal("file wait returned before an event")
			default:
			}
			if cancelWait {
				cancel()
			} else {
				close(arrival)
			}
			<-done
			if cancelWait && err == nil || !cancelWait && (!pending || err != nil) {
				t.Fatalf("unexpected file wait result: pending=%v, err=%v", pending, err)
			}
			if b.client.Timeout != 5*time.Second {
				t.Fatal("ordinary RPC timeout changed")
			}
		})
	}
}

func TestPublishTaildropStaging(t *testing.T) {
	staging := t.TempDir()
	dir := t.TempDir()
	path := filepath.Join(staging, "received.txt")
	if err := os.WriteFile(path, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	count, err := publishTaildropStaging(staging, dir)
	if err != nil || count != 1 {
		t.Fatalf("publish count=%d err=%v", count, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "received.txt"))
	if err != nil || string(data) != "hello" {
		t.Fatalf("published data=%q err=%v", data, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("staged source still exists: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "received.txt"), []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("again"), 0600); err != nil {
		t.Fatal(err)
	}
	if count, err := publishTaildropStaging(staging, dir); err != nil || count != 1 {
		t.Fatalf("conflict publish count=%d err=%v", count, err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "received (1).txt")); err != nil || string(data) != "again" {
		t.Fatalf("conflict data=%q err=%v", data, err)
	}

	if err := os.Symlink(filepath.Join(staging, "received.txt"), filepath.Join(staging, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := publishTaildropStaging(staging, dir); err == nil {
		t.Fatal("symlink staging entry was accepted")
	}
}

func TestPublishTaildropKeepsStagingOnOwnerFailure(t *testing.T) {
	staging := t.TempDir()
	dir := t.TempDir()
	source := filepath.Join(staging, "owner-check.txt")
	if err := os.WriteFile(source, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	normalize := func(path string) error { return errors.New("synthetic owner verification failure") }
	if _, err := publishTaildropStagingWith(staging, dir, normalize); err == nil {
		t.Fatal("owner failure was ignored")
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("staged source was removed after owner failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "owner-check.txt")); !os.IsNotExist(err) {
		t.Fatalf("partial destination was retained: %v", err)
	}
}

func TestTaildropBrowsePathValidation(t *testing.T) {
	if validTaildropBrowsePath("") {
		t.Fatal("empty browse path bypassed validation")
	}
	for _, path := range []string{"/storage/emulated/0", "/sdcard", "/storage/emulated/0/Download", "/storage/emulated/0/Documents/Inbox"} {
		if !validTaildropBrowsePath(path) {
			t.Errorf("valid browse path rejected: %q", path)
		}
	}
	for _, path := range []string{"/data/adb", "/storage/emulated/0/Android/data/com.example", "/storage/emulated/0/Android/obb", "/storage/emulated/0/Download/../Documents"} {
		if validTaildropBrowsePath(path) {
			t.Errorf("unsafe browse path accepted: %q", path)
		}
	}
	if !validTaildropBrowsePath("/storage/emulated/0/Pictures") || !validTaildropBrowsePath("/storage/emulated/0/Music") {
		t.Fatal("ordinary shared-storage directories were rejected")
	}
	if got, ok := normalizeTaildropPath("/sdcard/Download/Tailscale"); !ok || got != "/storage/emulated/0/Download/Tailscale" {
		t.Fatalf("sdcard alias was not canonicalized: %q %v", got, ok)
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMaskedSettingsPreserveUnrelatedPrefs(t *testing.T) {
	var patch map[string]any
	b := &backend{client: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		body := `{}`
		if r.Method == "PATCH" {
			if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
				t.Fatal(err)
			}
		}
		if r.Method == "GET" && r.URL.Path == "/localapi/v0/status" {
			body = `{"BackendState":"Running","TailscaleIPs":[],"AuthURL":"https://login.tailscale.com/a/private"}`
		}
		if r.Method == "GET" && r.URL.Path == "/localapi/v0/prefs" {
			body = `{"RouteAll":true,"ShieldsUp":false,"ExitNodeID":"example","Persist":{"PrivateNodeKey":"must-not-leak"}}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}
	value := true
	r := b.setSettings(context.Background(), &settingsPatch{ShieldsUp: &value})
	if !r.OK || patch["ShieldsUp"] != true || patch["ShieldsUpSet"] != true || len(patch) != 2 {
		t.Fatalf("unexpected patch: %+v", patch)
	}
	encoded, _ := json.Marshal(r)
	if strings.Contains(string(encoded), "must-not-leak") || strings.Contains(string(encoded), "PrivateNodeKey") || strings.Contains(string(encoded), "Persist") {
		t.Fatal("private prefs leaked")
	}
}

func TestAdvertiseRoutesValidation(t *testing.T) {
	for _, value := range []string{"10.0.0.0/8", "192.168.1.0/24, fd7a:115c:a1e0::/64"} {
		if routes, err := parseAdvertiseRoutes(value); err != nil || len(routes) == 0 {
			t.Fatalf("valid routes rejected: %q, %v", value, err)
		}
	}
	for _, value := range []string{"0.0.0.0/0", "10.0.0.1/8", "10.0.0.0/8,10.0.0.0/8" + strings.Repeat(",10.0.0.0/8", 16), "bad"} {
		if routes, err := parseAdvertiseRoutes(value); err == nil || routes != nil && value == "0.0.0.0/0" {
			t.Errorf("invalid route accepted: %q", value)
		}
	}
}

func TestAdvertiseRoutesMaskedPatchPreservesRoles(t *testing.T) {
	var patch map[string]any
	b := &backend{client: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"BackendState":"Running","TailscaleIPs":[]}`
		if r.Method == http.MethodGet && r.URL.Path == "/localapi/v0/prefs" {
			body = `{"AdvertiseRoutes":["10.0.0.0/8","0.0.0.0/0","::/0"],"RouteAll":true,"ShieldsUp":true}`
		}
		if r.Method == http.MethodPatch {
			if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
				t.Fatal(err)
			}
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}
	keepExit := false
	if r := b.setAdvertisement(context.Background(), &keepExit, nil); !r.OK {
		t.Fatalf("disable exit advertisement failed: %s", r.Error)
	}
	if got := patch["AdvertiseRoutes"].([]any); len(got) != 1 || got[0] != "10.0.0.0/8" || patch["AdvertiseRoutesSet"] != true {
		t.Fatalf("exit disable did not preserve subnet route: %+v", patch)
	}
	routes := "192.168.0.0/16"
	if r := b.setAdvertisement(context.Background(), nil, &routes); !r.OK {
		t.Fatalf("route update failed: %s", r.Error)
	}
	got := patch["AdvertiseRoutes"].([]any)
	if len(got) != 3 || got[0] != "0.0.0.0/0" || got[1] != "::/0" || got[2] != routes {
		t.Fatalf("route update did not preserve exit routes: %+v", patch)
	}
	clear := ""
	if r := b.setAdvertisement(context.Background(), nil, &clear); !r.OK {
		t.Fatalf("route clear failed: %s", r.Error)
	}
	got = patch["AdvertiseRoutes"].([]any)
	if len(got) != 2 || got[0] != "0.0.0.0/0" || got[1] != "::/0" {
		t.Fatalf("route clear did not preserve exit routes: %+v", patch)
	}
}

func writeProcFile(t *testing.T, root, name, value string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func exitNodeBackend(t *testing.T, prefs string, patches *[]map[string]any) *backend {
	t.Helper()
	return &backend{client: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"BackendState":"Running","TailscaleIPs":[]}`
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/localapi/v0/prefs":
			body = prefs
		case r.Method == http.MethodGet && r.URL.RawQuery == "peers=true":
			body = `{"Peer":{"k1":{"ID":"nOn","HostName":"on","TailscaleIPs":["100.64.0.2"],"Online":true,"ExitNodeOption":true},` +
				`"k2":{"ID":"nOff","HostName":"off","TailscaleIPs":["100.64.0.3"],"Online":false,"ExitNodeOption":true},` +
				`"k3":{"ID":"nPlain","HostName":"plain","TailscaleIPs":["100.64.0.4"],"Online":true}}}`
		case r.Method == http.MethodPatch:
			var patch map[string]any
			if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
				t.Fatal(err)
			}
			*patches = append(*patches, patch)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}
}

func TestSetExitNodeUsesMaskedPrefs(t *testing.T) {
	root := t.TempDir()
	defer func(old string) { procRoot = old }(procRoot)
	procRoot = root
	var patches []map[string]any
	b := exitNodeBackend(t, `{"ExitNodeID":"old","RouteAll":true}`, &patches)
	check := func(target string, all, wlan, wantMode int, wantID string) {
		t.Helper()
		writeProcFile(t, root, "sys/net/ipv4/conf/all/rp_filter", strconv.Itoa(all))
		writeProcFile(t, root, "sys/net/ipv4/conf/wlan0/rp_filter", strconv.Itoa(wlan))
		patches = nil
		if r := b.setExitNode(context.Background(), target); !r.OK {
			t.Fatalf("setExitNode(%q) failed: %s", target, r.Error)
		}
		if len(patches) != 1 {
			t.Fatalf("want one patch, got %+v", patches)
		}
		p := patches[0]
		if p["ExitNodeID"] != wantID || p["ExitNodeIP"] != "" || p["NetfilterMode"] != float64(wantMode) ||
			p["ExitNodeIDSet"] != true || p["ExitNodeIPSet"] != true || p["NetfilterModeSet"] != true {
			t.Fatalf("unexpected patch for %q (rp_filter %d/%d): %+v", target, all, wlan, p)
		}
		for _, key := range []string{"ExitNodeAllowLANAccessSet", "CorpDNSSet", "RouteAllSet", "AdvertiseRoutesSet", "AutoExitNodeSet"} {
			if _, ok := p[key]; ok {
				t.Fatalf("patch must not touch %s: %+v", key, p)
			}
		}
	}
	check("nOn", 0, 0, 0, "nOn")
	check("100.64.0.2", 0, 0, 0, "nOn")
	check("nOn", 2, 1, 0, "nOn")
	check("nOn", 0, 1, 2, "nOn")
	check("", 0, 1, 0, "")

	for _, target := range []string{"nOff", "nPlain", "nMissing", "100.64.0.9"} {
		patches = nil
		if r := b.setExitNode(context.Background(), target); r.OK || len(patches) != 0 {
			t.Fatalf("target %q must be rejected without a patch: %+v %+v", target, r, patches)
		}
	}
	patches = nil
	adv := exitNodeBackend(t, `{"AdvertiseRoutes":["0.0.0.0/0","::/0"]}`, &patches)
	if r := adv.setExitNode(context.Background(), "nOn"); r.OK || len(patches) != 0 {
		t.Fatalf("advertising device must not use an exit node: %+v %+v", r, patches)
	}
}

func TestStatusExitNodeOnlineAndIPv6(t *testing.T) {
	root := t.TempDir()
	defer func(old string) { procRoot = old }(procRoot)
	procRoot = root
	statusFor := func(st string) response {
		b := &backend{client: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
			body := st
			if r.URL.Path == "/localapi/v0/prefs" {
				body = `{"ExitNodeID":"nOn"}`
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})}}
		r, _ := b.statusWithLocal(context.Background())
		return r
	}
	r := statusFor(`{"BackendState":"Running","ExitNodeStatus":{"ID":"nOn","Online":false}}`)
	if r.ExitNodeIPv6 != nil {
		t.Fatalf("an unreadable route table must leave exitNodeIPv6 unknown: %+v", r)
	}
	writeProcFile(t, root, "net/ipv6_route", "")
	r = statusFor(`{"BackendState":"Running","ExitNodeStatus":{"ID":"nOn","Online":false}}`)
	if r.ExitNodeOnline == nil || *r.ExitNodeOnline || r.ExitNodeIPv6 == nil || *r.ExitNodeIPv6 {
		t.Fatalf("unexpected exit node status: %+v", r)
	}
	r = statusFor(`{"BackendState":"Running"}`)
	if r.ExitNodeOnline == nil || *r.ExitNodeOnline {
		t.Fatalf("a running node without ExitNodeStatus must report the exit node offline: %+v", r)
	}
	r = statusFor(`{"BackendState":"Stopped"}`)
	if r.ExitNodeOnline != nil {
		t.Fatalf("a stopped node must omit exitNodeOnline: %+v", r)
	}
	if data, _ := json.Marshal(r); strings.Contains(string(data), "exitNodeOnline") {
		t.Fatalf("exitNodeOnline must be omitted: %s", data)
	}

	route := "00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 00000400 00000001 00000000 00000001 %s\n"
	writeProcFile(t, root, "net/ipv6_route", fmt.Sprintf(route, "wlan0"))
	if v := tunnelIPv6Default(); v == nil || *v {
		t.Fatal("::/0 via wlan0 must not count as tunnelled")
	}
	writeProcFile(t, root, "net/ipv6_route", fmt.Sprintf(route, "wlan0")+fmt.Sprintf(route, "tailscale0"))
	if v := tunnelIPv6Default(); v == nil || !*v {
		t.Fatal("::/0 via tailscale0 must count as tunnelled")
	}
}

func TestOutputIsBounded(t *testing.T) {
	var b limitedBuffer
	input := []byte(strings.Repeat("x", 2<<20))
	n, err := b.Write(input)
	if err != nil || n != len(input) || b.Len() != 65536 || !b.truncated {
		t.Fatal("output bound failed")
	}
}

func deviceBackend(t *testing.T, body string) *backend {
	t.Helper()
	return &backend{client: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/localapi/v0/status" || r.URL.RawQuery != "peers=true" || r.Body != nil {
			t.Errorf("devices must only read peer status")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}
}

func TestExitNodesExcludeOtherDevices(t *testing.T) {
	body := `{"Self":{"ID":"self","ExitNodeOption":true},"Peer":{
		"a":{"ID":"exit","ExitNodeOption":true,"Online":true},
		"b":{"ID":"ordinary","Online":true}
	}}`
	r := deviceBackend(t, body).deviceList(context.Background(), true)
	if !r.OK || r.Devices == nil || len(*r.Devices) != 1 || (*r.Devices)[0].ID != "exit" {
		t.Fatal("expected only peer exit nodes")
	}
}

func TestDevicesProjectionAndOrdering(t *testing.T) {
	body := `{
		"Self":{"ID":"self","HostName":"self-host","DNSName":"local.example.test.","TailscaleIPs":["100.64.0.1","fd7a:115c:a1e0::1"],"Online":false,"OS":"android","PublicKey":"private-marker-self-key"},
		"Peer":{
			"private-marker-map-key-1":{"ID":"offline","HostName":"aardvark","Online":false,"LastSeen":"2026-01-01T00:00:00Z"},
			"private-marker-map-key-2":{"ID":"zulu","HostName":"zulu-host","DNSName":"Zulu.example.test.","Online":true,"LastSeen":"2026-01-01T00:00:00Z"},
			"private-marker-map-key-3":{"ID":"renamed","HostName":"old-hostname","DNSName":"alpha.example.test.","Online":true,"Addrs":["private-marker-endpoint"],"CurAddr":"private-marker-current-address","PublicKey":"private-marker-node-key","UserID":123,"PeerAPIURL":["private-marker-peer-api"]},
			"private-marker-map-key-4":{"ID":"fallback","HostName":"beta","Online":true,"OS":"linux","TailscaleIPs":[]},
			"private-marker-map-key-5":{"ID":"self","HostName":"duplicate","Online":true},
			"private-marker-null":null
		},
		"User":{"123":{"LoginName":"private-marker-account"}},
		"AuthURL":"private-marker-auth-url","CurrentTailnet":{"Name":"private-marker-tailnet"}
	}`
	r := deviceBackend(t, body).devices(context.Background())
	if !r.OK || r.Devices == nil || r.Truncated {
		t.Fatal("expected bounded device response")
	}
	devices := *r.Devices
	var gotIDs []string
	for _, device := range devices {
		gotIDs = append(gotIDs, device.ID)
	}
	if want := []string{"self", "renamed", "fallback", "zulu", "offline"}; !reflect.DeepEqual(gotIDs, want) {
		t.Errorf("wrong self/online/name order: %v", gotIDs)
	}
	if !devices[0].Self || devices[1].Self || devices[1].Name != "alpha" || devices[1].DNSName != "alpha.example.test" || devices[2].Name != "beta" {
		t.Error("wrong name, self, or DNS projection")
	}
	if len(devices[0].IPs) != 2 || devices[4].LastSeen == nil || devices[3].LastSeen != nil {
		t.Error("wrong address or offline timestamp projection")
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-marker") || strings.Contains(string(encoded), "old-hostname") {
		t.Fatal("non-UI LocalAPI fields leaked")
	}
	var public struct {
		Devices []map[string]json.RawMessage `json:"devices"`
	}
	if err := json.Unmarshal(encoded, &public); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"id": true, "name": true, "dnsName": true, "ips": true, "online": true, "os": true, "self": true, "lastSeen": true}
	for _, device := range public.Devices {
		for field := range device {
			if !allowed[field] {
				t.Errorf("unexpected device field %q", field)
			}
		}
	}
}

func TestDevicesEmptyAndMalformedResponses(t *testing.T) {
	for _, body := range []string{`{}`, `{"Self":null,"Peer":null}`, `{"Peer":{"ignored":null}}`} {
		r := deviceBackend(t, body).devices(context.Background())
		encoded, _ := json.Marshal(r)
		if !r.OK || r.Devices == nil || len(*r.Devices) != 0 || !strings.Contains(string(encoded), `"devices":[]`) {
			t.Error("empty inventory must return an empty JSON array")
		}
	}
	for _, body := range []string{`{"Peer":[]}`, `{"Peer":{"ignored":{"LastSeen":"bad"}}}`, `{"Peer":false}`, `{"Peer":{"ignored":1}}`} {
		r := deviceBackend(t, body).devices(context.Background())
		if r.OK || r.Devices != nil {
			t.Error("malformed inventory accepted")
		}
	}
}

func TestDevicesLimitsAndUntrustedValues(t *testing.T) {
	// Values remain inert strings. Only the explicit Tailscale IP field is used;
	// invalid addresses, zones, duplicates, and oversized values are discarded.
	malicious := `<img src=x onerror=alert(1)>;$(id)`
	peer := map[string]any{"ID": strings.Repeat("i", 200), "HostName": malicious,
		"DNSName": "", "OS": strings.Repeat("界", 100),
		"TailscaleIPs": []string{"not-an-ip", "fe80::1%wlan0", "100.64.0.2", "100.64.0.2", "fd7a:115c:a1e0::2", "100.64.0.3", "100.64.0.4", "100.64.0.5", strings.Repeat("1", 1000)},
		"PublicKey":    "private-marker-public-key", "NodeKey": "private-marker-node-key", "UserID": "private-marker-user"}
	encoded, _ := json.Marshal(map[string]any{"Peer": map[string]any{"private-marker-map-key": peer}})
	r := deviceBackend(t, string(encoded)).devices(context.Background())
	if !r.OK || r.Devices == nil || len(*r.Devices) != 1 {
		t.Fatal("expected projected device")
	}
	d := (*r.Devices)[0]
	if d.Name != malicious || len(d.ID) != 128 || len(d.OS) > 32 || !utf8.ValidString(d.OS) || len(d.IPs) != 4 {
		t.Error("field bounds or literal-data handling failed")
	}
	encoded, _ = json.Marshal(r)
	if strings.Contains(string(encoded), "private-marker") {
		t.Error("unselected fields leaked")
	}

	peers := map[string]any{}
	for i := 1100; i > 0; i-- {
		peers[fmt.Sprintf("ignored-key-%d", i)] = map[string]any{"ID": fmt.Sprintf("id-%04d", i), "HostName": fmt.Sprintf("device-%04d", i), "Online": true}
	}
	check := func() response {
		t.Helper()
		encoded, _ := json.Marshal(map[string]any{"Self": map[string]any{"ID": "self", "HostName": "zzz-self"}, "Peer": peers})
		return deviceBackend(t, string(encoded)).devices(context.Background())
	}
	r = check()
	if !r.OK || !r.Truncated || len(*r.Devices) != 1000 || !(*r.Devices)[0].Self || (*r.Devices)[1].ID != "id-0001" || (*r.Devices)[999].ID != "id-0999" {
		t.Fatal("count cap must retain self and the first sorted peers")
	}
	// Six-byte JSON escapes deliberately exercise the encoded-byte budget.
	for key := range peers {
		if len(peers) > 400 {
			delete(peers, key)
		}
	}
	for _, raw := range peers {
		entry := raw.(map[string]any)
		entry["ID"] = strings.Repeat("<", 128)
		entry["HostName"] = strings.Repeat("&", 300)
		entry["DNSName"] = strings.Repeat(">", 300)
		entry["OS"] = strings.Repeat("<", 100)
	}
	r = check()
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK || !r.Truncated || len(*r.Devices) >= len(peers) || len(encoded) > (1<<20)+1024 || !(*r.Devices)[0].Self {
		t.Fatal("encoded payload must remain below the WebUI limit")
	}
	for _, d := range *r.Devices {
		if len(d.ID) > 128 || len(d.Name) > 128 || len(d.DNSName) > 253 || len(d.OS) > 32 {
			t.Error("device string limit exceeded")
		}
	}
}

func TestOrdinaryStatusOmitsPeers(t *testing.T) {
	b := &backend{client: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		body := `{}`
		if r.URL.Path == "/localapi/v0/status" {
			if r.URL.RawQuery != "peers=false" {
				t.Error("ordinary status requested peer inventory")
			}
			body = `{"BackendState":"Running","TailscaleIPs":[],"Peer":{"private-marker":{"HostName":"private-marker"}}}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}
	r := b.status(context.Background())
	encoded, _ := json.Marshal(r)
	if !r.OK || r.Devices != nil || strings.Contains(string(encoded), "private-marker") {
		t.Error("ordinary status returned peer inventory")
	}
}

func TestConsoleCommandHasFixedIntent(t *testing.T) {
	cmd := consoleCommand(context.Background())
	want := []string{"/system/bin/am", "start", "-W", "--user", "current", "-a", "android.intent.action.VIEW", "-c", "android.intent.category.BROWSABLE", "-d", "https://console.tailscale.com/admin/machines"}
	if cmd.Path != want[0] || !reflect.DeepEqual(cmd.Args, want) || cmd.Stdout != nil || cmd.Stderr != nil || cmd.WaitDelay != time.Second || cmd.Cancel == nil {
		t.Fatal("console must use a fixed bounded external-browser intent without shell output")
	}
	for _, locale := range []string{"zh-CN", "zh-TW", "en"} {
		message := localizedError("无法打开控制台，请安装或启用浏览器后重试", locale)
		if message == "" || locale == "en" && !strings.Contains(message, "browser") {
			t.Error("console error lacks a native translation")
		}
	}
}

func TestAboutCommandHasFixedIntent(t *testing.T) {
	cmd := aboutCommand(context.Background())
	want := []string{"/system/bin/am", "start", "-W", "--user", "current", "-a", "android.intent.action.VIEW", "-c", "android.intent.category.BROWSABLE", "-d", "https://github.com/KyuubiRan/Shellscaled"}
	if cmd.Path != want[0] || !reflect.DeepEqual(cmd.Args, want) || cmd.Stdout != nil || cmd.Stderr != nil || cmd.WaitDelay != time.Second || cmd.Cancel == nil {
		t.Fatal("project page must use a fixed external-browser intent without shell output")
	}
	// Only inspect the command: this test never launches a real browser.
	for _, locale := range []string{"zh-CN", "zh-TW", "en"} {
		message := localizedError("无法打开项目主页，请安装或启用浏览器后重试", locale)
		if message == "" || (locale == "en" && !strings.Contains(message, "project page")) {
			t.Error("project-page error lacks a native translation")
		}
	}
}

func TestAboutRPCIsActionAndLocaleOnly(t *testing.T) {
	encode := func(value any) string { data, _ := json.Marshal(value); return base64.StdEncoding.EncodeToString(data) }
	for _, locale := range []string{"system", "en", "zh-CN", "zh-TW"} {
		if _, err := decodeRequest(encode(map[string]any{"action": "about", "locale": locale})); err != nil {
			t.Fatal("valid project-page action rejected")
		}
	}
	for key, value := range map[string]any{"command": "", "settings": nil, "channel": nil, "url": "https://example.invalid/", "component": "example.invalid/.Activity", "args": []string{"--user", "0"}, "env": map[string]string{"PATH": "/other"}, "path": "/other", "localeOverride": "en", "Action": "console"} {
		if _, err := decodeRequest(encode(map[string]any{"action": "about", key: value})); err == nil {
			t.Errorf("project-page action accepted extra field %q", key)
		}
	}
	for _, raw := range []string{`{"action":"about","command":"id"}`, `{"action":"about","settings":{"shieldsUp":true}}`, `{"action":"about","channel":"stable"}`, `{"action":"about","locale":"../../other"}`} {
		if _, err := decodeRequest(base64.StdEncoding.EncodeToString([]byte(raw))); err == nil {
			t.Error("mixed project-page request accepted")
		}
	}
}

func TestLoginRPCIsActionAndLocaleOnly(t *testing.T) {
	encode := func(value any) string { data, _ := json.Marshal(value); return base64.StdEncoding.EncodeToString(data) }
	for _, locale := range []string{"", "system", "en", "zh-CN", "zh-TW"} {
		if _, err := decodeRequest(encode(map[string]any{"action": "login", "locale": locale})); err != nil {
			t.Fatal("valid login action rejected")
		}
	}
	for key, value := range map[string]any{"command": "", "settings": nil, "channel": nil, "url": "https://login.tailscale.com/a/example",
		"component": "example.invalid/.Activity", "args": []string{"--user", "0"}, "env": map[string]string{"PATH": "/other"},
		"path": "/other", "controlURL": "https://example.invalid", "loginBrowser": true, "Action": "login"} {
		if _, err := decodeRequest(encode(map[string]any{"action": "login", key: value})); err == nil {
			t.Errorf("login action accepted extra field %q", key)
		}
	}
	for _, raw := range []string{`{"action":"login","command":null}`, `{"action":"login","settings":{}}`, `{"action":"login","channel":"stable"}`, `{"action":"login","locale":"../../other"}`} {
		if _, err := decodeRequest(base64.StdEncoding.EncodeToString([]byte(raw))); err == nil {
			t.Error("mixed login request accepted")
		}
	}
}

func TestLoginIntentHasOfficialDestination(t *testing.T) {
	for _, tc := range []struct{ target, control string }{
		{"https://login.tailscale.com/a/exampleToken_123", ""},
		{"https://login.tailscale.com:443/a/example-token", ""},
		{"https://controlplane.tailscale.com/a/example", "https://controlplane.tailscale.com"},
		{"https://controlplane.tailscale.com/a/example", ""},
	} {
		cmd, err := loginCommand(context.Background(), tc.target, tc.control)
		want := []string{"/system/bin/am", "start", "-W", "--user", "current", "-a", "android.intent.action.VIEW", "-c", "android.intent.category.BROWSABLE", "-d", tc.target}
		if err != nil || cmd == nil || cmd.Path != want[0] || !reflect.DeepEqual(cmd.Args, want) ||
			cmd.Stdout != nil || cmd.Stderr != nil || cmd.WaitDelay != time.Second || cmd.Cancel == nil {
			t.Fatal("login must use a validated, fixed external-browser intent without output")
		}
		// Inspect only. Never launch a real browser or initiate authentication.
	}
	for _, target := range []string{"", "http://login.tailscale.com/a/example", "https://example.invalid/a/example", "https://login.tailscale.com.example.invalid/a/example",
		"https://login.tailscale.com./a/example", "https://user@login.tailscale.com/a/example", "https://login.tailscale.com:444/a/example", "https://login.tailscale.com:/a/example",
		"https://login.tailscale.com/a/example?next=https://example.invalid", "https://login.tailscale.com/a/example?", "https://login.tailscale.com/a/example#fragment", "https://login.tailscale.com/a/example#",
		"https://login.tailscale.com/", "https://login.tailscale.com/a/", "https://login.tailscale.com/a/../example", "https://login.tailscale.com/a/example/more",
		"https://login.tailscale.com/a/%65xample", "https://login.tailscale.com/a/example%2fother", "https://login.tailscale.com/a/example\n--user 0", " https://login.tailscale.com/a/example",
		"intent://login.tailscale.com/a/example", "file:///data/local/tmp/example", "javascript:example()"} {
		if cmd, err := loginCommand(context.Background(), target, ""); err == nil || cmd != nil {
			t.Fatal("an unsafe login destination produced a browser command")
		}
	}
	if allowedLoginURL("https://controlplane.tailscale.com/a/example", "https://example.invalid") {
		t.Fatal("control-plane login URL escaped its official control origin")
	}
	for _, locale := range []string{"zh-CN", "zh-TW", "en"} {
		for _, key := range []string{"登录链接无效，请刷新后重试", "登录链接尚未准备好，请稍后重试", "无法打开登录页面，请安装或启用浏览器后重试"} {
			if translated := localizedError(key, locale); translated == "" || (locale == "en" && !strings.Contains(translated, "sign-in")) {
				t.Fatal("login error lacks a native translation")
			}
		}
	}
}

type loginFixture struct {
	b                  *backend
	state, authURL     string
	controlURL         string
	patches            []map[string]any
	interactive, reads int
	opened             []string
	onInteractive      func()
	onStatus           func()
	browserError       error
}

func newLoginFixture(t *testing.T, state, authURL string) *loginFixture {
	t.Helper()
	root := t.TempDir()
	f := &loginFixture{state: state, authURL: authURL, controlURL: "https://controlplane.tailscale.com"}
	f.b = &backend{module: filepath.Join(root, "module"), stateDir: filepath.Join(root, "state"), locale: "en"}
	f.b.client = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Scheme != "http" || req.URL.Host != "local-tailscaled.sock" {
			t.Fatal("login escaped the fake fixed LocalAPI")
		}
		var body any = map[string]any{}
		switch req.Method + " " + req.URL.Path {
		case "GET /localapi/v0/status":
			if req.URL.RawQuery != "peers=false" {
				t.Fatal("login requested peer inventory")
			}
			f.reads++
			if f.onStatus != nil {
				f.onStatus()
			}
			body = localStatus{BackendState: f.state, Version: "1.104.2", AuthURL: f.authURL, TailscaleIPs: []string{}}
		case "GET /localapi/v0/prefs":
			body = map[string]any{"ControlURL": f.controlURL, "RouteAll": true, "ShieldsUp": true, "ExitNodeAllowLANAccess": true,
				"ExitNodeID": "synthetic-exit", "Persist": map[string]string{"PrivateNodeKey": "synthetic-login-private-canary"}}
		case "PATCH /localapi/v0/prefs":
			var patch map[string]any
			if json.NewDecoder(req.Body).Decode(&patch) != nil {
				t.Fatal("invalid login preference patch")
			}
			f.patches = append(f.patches, patch)
		case "POST /localapi/v0/login-interactive":
			f.interactive++
			if f.onInteractive != nil {
				f.onInteractive()
			}
		default:
			t.Fatal("unexpected fake login LocalAPI action")
		}
		encoded, _ := json.Marshal(body)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(encoded))), Header: make(http.Header), Request: req}, nil
	})}
	f.b.loginBrowser = func(ctx context.Context, target string) error {
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 10*time.Second {
			t.Fatal("browser operation has no short deadline")
		}
		f.opened = append(f.opened, target)
		return f.browserError
	}
	return f
}

func (f *loginFixture) assertNoPrivateState(t *testing.T, r response) {
	t.Helper()
	encoded, _ := json.Marshal(r)
	if strings.Contains(string(encoded), "synthetic-login-private-canary") || strings.Contains(string(encoded), "PrivateNodeKey") || strings.Contains(string(encoded), "Persist") {
		t.Fatal("login response included private preferences")
	}
	if r.OK && r.Settings != (settings{true, true, true}) {
		t.Fatal("login changed unrelated displayed settings")
	}
	for _, patch := range f.patches {
		if !reflect.DeepEqual(patch, map[string]any{"WantRunning": true, "WantRunningSet": true}) {
			t.Fatal("login modified account data or unrelated preferences")
		}
	}
}

func TestLoginExistingAndPendingStates(t *testing.T) {
	for _, state := range []string{"NeedsLogin", "Running", "Stopped", "Starting", "NeedsMachineAuth"} {
		t.Run(state, func(t *testing.T) {
			f := newLoginFixture(t, state, "https://login.tailscale.com/a/synthetic-existing")
			r := f.b.login(context.Background())
			if !r.OK || r.State != state || !r.DaemonRunning || len(f.patches) != 0 || f.interactive != 0 {
				t.Fatal("existing or approved account state was mutated")
			}
			wantOpen := 0
			if state == "NeedsLogin" {
				wantOpen = 1
			}
			if len(f.opened) != wantOpen || (wantOpen == 1 && f.opened[0] != f.authURL) {
				t.Fatal("login opened a browser for an authenticated or approval-pending device")
			}
			f.assertNoPrivateState(t, r)
		})
	}
}

func TestLoginGeneratesMissingURL(t *testing.T) {
	f := newLoginFixture(t, "NeedsLogin", "")
	f.onInteractive = func() { f.authURL = "https://login.tailscale.com/a/synthetic-generated" }
	r := f.b.login(context.Background())
	if !r.OK || r.State != "NeedsLogin" || r.LoginURL != f.authURL || f.interactive != 1 || len(f.patches) != 1 ||
		!reflect.DeepEqual(f.opened, []string{f.authURL}) {
		t.Fatal("one-click login did not generate and open its fake LocalAPI URL")
	}
	f.assertNoPrivateState(t, r)
}

func TestLoginRejectsUnsafeURLsAndBrowserFailures(t *testing.T) {
	for _, target := range []string{"http://login.tailscale.com/a/synthetic", "https://example.invalid/a/synthetic", "javascript:synthetic", "https://login.tailscale.com/a/synthetic?redirect=example"} {
		f := newLoginFixture(t, "NeedsLogin", target)
		r := f.b.login(context.Background())
		if r.OK || len(f.opened) != 0 || len(f.patches) != 0 || f.interactive != 0 || strings.Contains(r.Error, target) {
			t.Fatal("unsafe LocalAPI URL opened a browser, restarted authentication, or leaked into errors")
		}
		f.assertNoPrivateState(t, r)
	}
	f := newLoginFixture(t, "NeedsLogin", "https://login.tailscale.com/a/synthetic-browser-failure")
	f.browserError = errors.New("synthetic-private-browser-output")
	r := f.b.login(context.Background())
	if r.OK || r.Error != "无法打开登录页面，请安装或启用浏览器后重试" || strings.Contains(r.Error, "synthetic") || f.interactive != 0 {
		t.Fatal("browser failure was not reported privately")
	}
	f.assertNoPrivateState(t, r)
}

func TestLoginGenerationTransitionsAndTimeout(t *testing.T) {
	for _, state := range []string{"Running", "NeedsMachineAuth"} {
		t.Run(state, func(t *testing.T) {
			f := newLoginFixture(t, "NeedsLogin", "")
			f.onInteractive = func() { f.state = state }
			r := f.b.login(context.Background())
			if !r.OK || r.State != state || len(f.opened) != 0 || f.interactive != 1 || len(f.patches) != 1 {
				t.Fatal("authentication transition triggered another login or browser")
			}
			f.assertNoPrivateState(t, r)
		})
	}
	f := newLoginFixture(t, "NeedsLogin", "")
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	r := f.b.login(ctx)
	if r.OK || len(f.opened) != 0 || f.interactive > 1 {
		t.Fatal("missing login URL did not stop within the request deadline")
	}
	f.assertNoPrivateState(t, r)
}
