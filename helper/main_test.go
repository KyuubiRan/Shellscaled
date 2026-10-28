package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCommandBoundary(t *testing.T) {
	allowed := []string{"tailscale status", "tailscale status --json", "tailscale ip -6", "tailscale version", "tailscale netcheck", "tailscale ping --help", "tailscale ping --c=2 --timeout=1s example-peer", "tailscale up", "tailscale down", "tailscale login", "tailscale set --accept-routes=false --shields-up=true", "tailscale set --exit-node= --netfilter-mode=off --accept-dns=false", "tailscaled --version", "tailscaled --help"}
	for _, text := range allowed {
		if _, err := validateCommand(text); err != nil {
			t.Errorf("allowed command rejected %q: %v", text, err)
		}
	}
	denied := []string{
		"", "tailscale", "id", "sh -c tailscale", "tailscalex status", "/system/bin/tailscale status", "env tailscale status", "TS_BE_CLI=1 tailscale status",
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
	for _, raw := range []string{`{"action":"status"}`, `{"action":"status","locale":"system"}`, `{"action":"status","locale":"en"}`, `{"action":"status","locale":"zh-CN"}`, `{"action":"status","locale":"zh-TW"}`, `{"action":"up"}`, `{"action":"down"}`, `{"action":"settings","settings":{"acceptRoutes":false}}`, `{"action":"command","command":"tailscale status"}`} {
		if _, err := decodeRequest(encode(raw)); err != nil {
			t.Errorf("valid request rejected: %v", err)
		}
	}
	for _, raw := range []string{`{"action":"exec","command":"id"}`, `{"action":"status","locale":"../../other"}`, `{"action":"status","locale":"fr"}`, `{"action":"status","command":"id"}`, `{"action":"settings","settings":{"acceptDNS":true}}`, `{"action":"settings"}`, `{"action":"command","command":"tailscale status","settings":{"shieldsUp":true}}`, `{"action":"status","env":{"PATH":"/evil"}}`, `{"action":"status"} {"action":"up"}`, `null`, `[]`} {
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
