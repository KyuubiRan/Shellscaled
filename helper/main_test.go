package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
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
	for _, raw := range []string{`{"action":"status"}`, `{"action":"status","locale":"system"}`, `{"action":"status","locale":"en"}`, `{"action":"status","locale":"zh-CN"}`, `{"action":"status","locale":"zh-TW"}`, `{"action":"binary-channel","channel":"stable"}`, `{"action":"binary-channel","channel":"preview","locale":"en"}`, `{"action":"binary-check"}`, `{"action":"binary-update","locale":"en"}`, `{"action":"devices"}`, `{"action":"console","locale":"en"}`, `{"action":"about"}`, `{"action":"about","locale":"en"}`, `{"action":"up"}`, `{"action":"down"}`, `{"action":"settings","settings":{"acceptRoutes":false}}`, `{"action":"command","command":"tailscale status"}`} {
		if _, err := decodeRequest(encode(raw)); err != nil {
			t.Errorf("valid request rejected: %v", err)
		}
	}
	for _, raw := range []string{`{"action":"binary-channel"}`, `{"action":"binary-channel","channel":"prerelease"}`, `{"action":"binary-channel","channel":"../../other"}`, `{"action":"binary-channel","channel":"preview","command":""}`, `{"action":"binary-channel","channel":"stable","settings":null}`, `{"action":"binary-update","channel":"preview"}`, `{"action":"binary-check","channel":null}`, `{"action":"status","channel":"stable"}`, `{"action":"status","Channel":"stable"}`, `{"action":"binary-check","command":""}`, `{"action":"binary-update","settings":null}`, `{"action":"binary-check","url":"https://example.test"}`, `{"action":"binary-update","command":"id"}`, `{"action":"binary-update","settings":{"shieldsUp":true}}`, `{"action":"binary-check","version":"1.2.3"}`, `{"action":"console","command":"id"}`, `{"action":"console","url":"https://example.test"}`, `{"action":"console","settings":{"shieldsUp":true}}`, `{"action":"devices","command":"tailscale status"}`, `{"action":"devices","settings":{"shieldsUp":true}}`, `{"action":"devices","peers":true}`, `{"action":"exec","command":"id"}`, `{"action":"status","locale":"../../other"}`, `{"action":"status","locale":"fr"}`, `{"action":"status","command":"id"}`, `{"action":"settings","settings":{"acceptDNS":true}}`, `{"action":"settings"}`, `{"action":"command","command":"tailscale status","settings":{"shieldsUp":true}}`, `{"action":"status","env":{"PATH":"/evil"}}`, `{"action":"status"} {"action":"up"}`, `null`, `[]`} {
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
