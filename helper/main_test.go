package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
		"", "tailscale", "id", "am start https://example.test", "tailscale console", "tailscale devices", "sh -c tailscale", "tailscalex status", "/system/bin/tailscale status", "env tailscale status", "TS_BE_CLI=1 tailscale status",
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
	for _, raw := range []string{`{"action":"status"}`, `{"action":"status","locale":"system"}`, `{"action":"status","locale":"en"}`, `{"action":"status","locale":"zh-CN"}`, `{"action":"status","locale":"zh-TW"}`, `{"action":"binary-channel","channel":"stable"}`, `{"action":"binary-channel","channel":"preview","locale":"en"}`, `{"action":"binary-check"}`, `{"action":"binary-update","locale":"en"}`, `{"action":"devices"}`, `{"action":"console","locale":"en"}`, `{"action":"up"}`, `{"action":"down"}`, `{"action":"settings","settings":{"acceptRoutes":false}}`, `{"action":"command","command":"tailscale status"}`} {
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
