package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func dnsControlProxy(t *testing.T) *magicDNSProxy {
	t.Helper()
	p, _ := dnsTestProxy(t, func(r *http.Request) (int, string) {
		switch r.URL.Path {
		case "/localapi/v0/status":
			return 200, `{"BackendState":"Running","CurrentTailnet":{"MagicDNSSuffix":"tail1234.ts.net"}}`
		case "/localapi/v0/prefs":
			return 200, `{}`
		}
		return 404, ""
	})
	old := magicDNSStatePath
	magicDNSStatePath = p.state
	t.Cleanup(func() { magicDNSStatePath = old })
	return p
}

func TestMagicDNSOffRequestsHaveDistinctAcknowledgements(t *testing.T) {
	p := dnsControlProxy(t)
	// An explicit off must not reuse the absent-file acknowledgement, nor an
	// earlier off after a complete on/off cycle that the proxy hasn't seen.
	if err := requestMagicDNS(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	p.refresh(context.Background())
	first, err := readMagicDNSRequest()
	if err != nil || first.Enabled || first.Generation == "" {
		t.Fatalf("off request: %+v %v", first, err)
	}
	if err := requestMagicDNS(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := requestMagicDNS(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	current, _ := readMagicDNSRequest()
	if current.Enabled || current.Generation == first.Generation {
		t.Fatal("on/off reused the previous off generation")
	}
	status := p.backend.status(context.Background())
	if status.MagicDNS == nil || !status.MagicDNS.Pending || status.MagicDNS.ApplyFailed {
		t.Fatal("an old off acknowledgement incorrectly completed the new request")
	}
	p.refresh(context.Background())
	status = p.backend.status(context.Background())
	if status.MagicDNS.Pending || status.MagicDNS.ApplyFailed || status.MagicDNS.Enabled {
		t.Fatal("the new off request was not acknowledged after applying")
	}
}

func TestMagicDNSDisabledDuringDoHProbeDiscardsOldPlan(t *testing.T) {
	p, _ := dnsTestProxy(t, func(r *http.Request) (int, string) {
		switch r.URL.Path + "?" + r.URL.RawQuery {
		case "/localapi/v0/status?peers=false":
			return 200, `{"BackendState":"Running","CurrentTailnet":{"MagicDNSSuffix":"tail1234.ts.net"},"ExitNodeStatus":{"ID":"nExit"}}`
		case "/localapi/v0/prefs?":
			return 200, `{"ExitNodeID":"nExit"}`
		case "/localapi/v0/status?peers=true":
			return 200, `{"Peer":{"k":{"ID":"nExit","PeerAPIURL":["http://100.64.0.2:1234"]}}}`
		}
		return 404, ""
	})
	p.doh = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		// No apply lock may be held during a network probe: the off request
		// must be able to commit before the old probe completes.
		if err := requestMagicDNS(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("synthetic DNS reply")), Header: http.Header{}}, nil
	})}
	appends := 0
	p.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 3 && args[3] == "-A" {
			appends++
		}
		return nil, nil
	}
	p.refresh(context.Background())
	request, _ := readMagicDNSRequest()
	state := readMagicDNSState(p.state, time.Now())
	if appends != 0 || state == nil || state.Enabled || state.ApplyFailed || state.generation != request.Generation || state.Mode != "off" {
		t.Fatalf("superseded enabled plan survived: appends=%d state=%+v", appends, state)
	}
}

func TestMagicDNSFailedDisableRemainsPendingUntilRetry(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "IPv4", true: "IPv6-filter-fallback"}[fallback], func(t *testing.T) {
			p := dnsControlProxy(t)
			p.ipv6Fallback = fallback
			failedBinary := "/system/bin/iptables"
			if fallback {
				failedBinary = "/system/bin/ip6tables"
			}
			failFlush := false
			installed := map[string]int{}
			p.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if len(args) > 3 {
					switch args[3] {
					case "-F":
						if failFlush && name == failedBinary {
							return nil, errors.New("synthetic clear failure")
						}
						installed[name] = 0
					case "-A":
						installed[name]++
					}
				}
				return nil, nil
			}
			p.refresh(context.Background())
			before := readMagicDNSState(p.state, time.Now())
			if before == nil || !before.Enabled || installed[failedBinary] == 0 {
				t.Fatal("fixture did not apply initial enabled rules")
			}
			if err := requestMagicDNS(context.Background(), false); err != nil {
				t.Fatal(err)
			}
			failFlush = true
			p.refresh(context.Background())
			status := p.backend.status(context.Background())
			if !status.OK || status.MagicDNSEnabled || status.MagicDNS == nil || !status.MagicDNS.Enabled ||
				!status.MagicDNS.Pending || !status.MagicDNS.ApplyFailed || installed[failedBinary] == 0 {
				t.Fatal("failed cleanup was incorrectly acknowledged as off")
			}
			failFlush = false
			p.refresh(context.Background())
			status = p.backend.status(context.Background())
			if status.MagicDNS.Pending || status.MagicDNS.ApplyFailed || status.MagicDNS.Enabled ||
				installed["/system/bin/iptables"] != 0 || installed["/system/bin/ip6tables"] != 0 {
				t.Fatal("retry did not clear both families and acknowledge off")
			}
		})
	}
}

func TestMagicDNSApplyAndRequestAreSerialized(t *testing.T) {
	p := dnsControlProxy(t)
	writerStarted, writerDone := make(chan struct{}), make(chan error, 1)
	started := false
	p.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 3 && args[3] == "-A" {
			if !started {
				started = true
				go func() {
					close(writerStarted)
					writerDone <- requestMagicDNS(context.Background(), false)
				}()
				<-writerStarted
			}
			if !magicDNSEnabled() {
				t.Error("an old plan added a rule after the off request committed")
			}
		}
		return nil, nil
	}
	p.refresh(context.Background())
	select {
	case err := <-writerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("preference writer did not resume after the apply lock released")
	}
	if !p.backend.status(context.Background()).MagicDNS.Pending {
		t.Fatal("off request used the enabled plan's acknowledgement")
	}
	p.refresh(context.Background())
	if p.backend.status(context.Background()).MagicDNS.Pending || p.enabled {
		t.Fatal("serialized off request did not finish")
	}
}

func TestMagicDNSControlWaitHonorsCancellation(t *testing.T) {
	old := magicDNSEnabledFile
	magicDNSEnabledFile = filepath.Join(t.TempDir(), "enabled")
	t.Cleanup(func() { magicDNSEnabledFile = old })
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- withMagicDNSControl(context.Background(), func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := requestMagicDNS(ctx, true); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("waiting request ignored cancellation: %v", err)
	}
	if _, err := os.Stat(magicDNSEnabledFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled request changed the preference")
	}
}
