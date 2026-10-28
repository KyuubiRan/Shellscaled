// Copyright (c) 2026 KyuubiRan
// SPDX-License-Identifier: BSD-3-Clause
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testHash(data []byte) string { hash := sha256.Sum256(data); return hex.EncodeToString(hash[:]) }

// A tiny ELF fixture is parsed, never executed. Candidate execution has its own
// fixed-argument test and an injected version probe in transaction tests.
func testAndroidELF(arch string) []byte {
	machine := uint16(183)
	linker := "/system/bin/linker64\x00"
	if arch == "amd64" {
		machine = 62
	}
	if arch == "arm" {
		linker = "/system/bin/linker\x00"
		data := make([]byte, 116+len(linker))
		copy(data, "\x7fELF\x01\x01\x01")
		put16 := binary.LittleEndian.PutUint16
		put32 := binary.LittleEndian.PutUint32
		put16(data[16:], 3)
		put16(data[18:], 40)
		put32(data[20:], 1)
		put32(data[24:], 0x4000)
		put32(data[28:], 52)
		put16(data[40:], 52)
		put16(data[42:], 32)
		put16(data[44:], 2)
		put32(data[52:], 1)
		put32(data[68:], uint32(len(data)))
		put32(data[72:], uint32(len(data)))
		put32(data[76:], 5)
		put32(data[80:], 16384)
		put32(data[84:], 3)
		put32(data[88:], 116)
		put32(data[100:], uint32(len(linker)))
		put32(data[104:], uint32(len(linker)))
		put32(data[112:], 1)
		copy(data[116:], linker)
		return data
	}
	data := make([]byte, 176+len(linker))
	copy(data, "\x7fELF\x02\x01\x01")
	put16 := binary.LittleEndian.PutUint16
	put32 := binary.LittleEndian.PutUint32
	put64 := binary.LittleEndian.PutUint64
	put16(data[16:], 3)
	put16(data[18:], machine)
	put32(data[20:], 1)
	put64(data[24:], 0x4000)
	put64(data[32:], 64)
	put16(data[52:], 64)
	put16(data[54:], 56)
	put16(data[56:], 2)
	put32(data[64:], 1)
	put32(data[68:], 5)
	put64(data[96:], uint64(len(data)))
	put64(data[104:], uint64(len(data)))
	put64(data[112:], 16384)
	put32(data[120:], 3)
	put64(data[128:], 176)
	put64(data[152:], uint64(len(linker)))
	put64(data[160:], uint64(len(linker)))
	put64(data[168:], 1)
	copy(data[176:], linker)
	return data
}

type updateFixture struct {
	u         *binaryUpdater
	old, new  []byte
	info      binaryMetadata
	manifest  binaryManifest
	release   binaryRelease
	routes    map[string][]byte
	calls     []string
	active    bool
	failStart bool
	statePath string
}

func newUpdateFixture(t *testing.T, active bool) *updateFixture {
	t.Helper()
	root := t.TempDir()
	module := filepath.Join(root, "module")
	state := filepath.Join(root, "persistent")
	if err := os.MkdirAll(filepath.Join(module, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(state, "state"), 0700); err != nil {
		t.Fatal(err)
	}
	f := &updateFixture{old: append(testAndroidELF("arm64"), 1), new: append(testAndroidELF("arm64"), 2), active: active, routes: map[string][]byte{}}
	oldInfo := binaryMetadata{TailscaleVersion: "1.104.2", SourceCommit: strings.Repeat("a", 40), Architecture: "arm64", Compatibility: 1, ReleaseChannel: "prerelease", BinarySHA256: testHash(f.old)}
	encoded, _ := json.Marshal(oldInfo)
	mustWrite(t, filepath.Join(module, "build-info.json"), encoded, 0600)
	mustWrite(t, filepath.Join(module, "bin/tailscaled"), f.old, 0755)
	f.statePath = filepath.Join(state, "state/tailscaled.state")
	mustWrite(t, f.statePath, []byte("anonymous-account-and-prefs-canary"), 0600)
	f.info = binaryMetadata{TailscaleVersion: "1.104.2", SourceCommit: strings.Repeat("b", 40), Architecture: "arm64", Compatibility: 1, ReleaseChannel: "prerelease", BinarySHA256: testHash(f.new)}
	f.manifest = binaryManifest{Schema: 1, Compatibility: 1, TailscaleVersion: f.info.TailscaleVersion, SourceCommit: f.info.SourceCommit, Architecture: "arm64", ReleaseChannel: "prerelease", BinarySHA256: f.info.BinarySHA256, PackageName: "Tailscale-1.104.2-r5-arm64.zip"}
	f.release = binaryRelease{Tag: "android-v1.104.2-dev.5", Prerelease: true, PublishedAt: time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)}
	f.u = &binaryUpdater{module: module, stateDir: state, arch: "arm64"}
	f.u.client = &http.Client{CheckRedirect: updateRedirect, Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Scheme != "https" || req.Header.Get("Authorization") != "" {
			t.Error("unexpected updater request")
		}
		data, ok := f.routes[req.URL.String()]
		if !ok && req.URL.String() == latestStableReleaseAPI {
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`{"message":"Not Found"}`)), Header: make(http.Header), Request: req}, nil
		}
		if !ok {
			return nil, errors.New("unexpected fixture request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data)), Header: make(http.Header), Request: req}, nil
	})}
	f.u.running = func(context.Context) (bool, error) { return f.active, nil }
	f.u.control = func(_ context.Context, action string) error {
		owner := f.u.controlOwner
		record, err := os.ReadFile(filepath.Join(state, "run/control.lock", owner))
		if owner == "" || err != nil || len(record) == 0 {
			t.Fatal("service mutation escaped the shared control lock")
		}
		f.calls = append(f.calls, action)
		if action == "stop" {
			f.active = false
			return nil
		}
		if action == "start" {
			if f.failStart {
				f.failStart = false
				return errors.New("anonymous startup failure")
			}
			f.active = true
			return nil
		}
		return errors.New("unexpected action")
	}
	f.u.version = func(_ context.Context, path string, metadata binaryMetadata) error {
		if path != filepath.Join(module, "bin/.tailscaled-update.candidate") || metadata != f.info {
			return errors.New("wrong candidate")
		}
		core, _ := os.ReadFile(filepath.Join(module, "bin/tailscaled"))
		if !bytes.Equal(core, f.old) {
			t.Error("binary replaced before candidate validation")
		}
		return nil
	}
	f.rebuild(t)
	return f
}

func mustWrite(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func (f *updateFixture) rebuild(t *testing.T) {
	t.Helper()
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	info, _ := json.Marshal(f.info)
	for _, entry := range []struct {
		name string
		data []byte
	}{{"bin/tailscaled", f.new}, {"build-info.json", info}, {"../../state/tailscaled.state", []byte("must never be extracted")}} {
		writer, err := archive.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	packageBytes := buffer.Bytes()
	f.manifest.PackageSHA256 = testHash(packageBytes)
	manifestBytes, _ := json.Marshal(f.manifest)
	base := "https://github.com/KyuubiRan/tailscale-android-cli/releases/download/" + f.release.Tag + "/"
	name := strings.TrimSuffix(f.manifest.PackageName, ".zip") + ".binary.json"
	f.release.Assets = []releaseAsset{{Name: name, URL: base + name, Digest: "sha256:" + testHash(manifestBytes), Size: int64(len(manifestBytes))}, {Name: f.manifest.PackageName, URL: base + f.manifest.PackageName, Digest: "sha256:" + testHash(packageBytes), Size: int64(len(packageBytes))}}
	releaseBytes, _ := json.Marshal([]binaryRelease{f.release})
	f.routes[releaseAPI] = releaseBytes
	f.routes[base+name] = manifestBytes
	f.routes[base+f.manifest.PackageName] = packageBytes
}

func (f *updateFixture) assertClean(t *testing.T, want []byte, active bool) {
	t.Helper()
	core, err := os.ReadFile(filepath.Join(f.u.module, "bin/tailscaled"))
	if err != nil || !bytes.Equal(core, want) {
		t.Fatal("unexpected installed core")
	}
	canary, err := os.ReadFile(f.statePath)
	if err != nil || string(canary) != "anonymous-account-and-prefs-canary" {
		t.Fatal("persistent account or preferences changed")
	}
	if f.u.controlOwner != "" {
		t.Fatal("controller token retained after transaction")
	}
	if _, err := os.Stat(filepath.Join(f.u.stateDir, "run/control.lock")); !os.IsNotExist(err) {
		t.Fatal("service mutex retained after transaction")
	}
	if f.active != active {
		t.Fatal("previous service state was not restored")
	}
	leftovers, _ := filepath.Glob(filepath.Join(f.u.module, "bin/.tailscaled-update.*"))
	if len(leftovers) != 0 {
		t.Fatal("transaction files were not cleaned")
	}
	if _, err := os.Stat(filepath.Join(f.u.module, "runtime-binary.json.update")); !os.IsNotExist(err) {
		t.Fatal("metadata staging file retained")
	}
}

func TestBinaryCheckFixedReleaseAndNoManifest(t *testing.T) {
	f := newUpdateFixture(t, false)
	plan, err := f.u.check(context.Background())
	if err != nil || !plan.Status.Checked || !plan.Status.Supported || !plan.Status.UpdateAvailable || plan.Status.CurrentVersion != "1.104.2" {
		t.Fatal("same-version patched core should be available")
	}
	f.release.Assets = nil
	data, _ := json.Marshal([]binaryRelease{f.release})
	f.routes[releaseAPI] = data
	plan, err = f.u.check(context.Background())
	if err != nil || !plan.Status.Checked || plan.Status.Supported || plan.Status.Reason != "no-compatible-release" {
		t.Fatal("older releases must be reported as unsupported")
	}
	if len(f.calls) != 0 {
		t.Fatal("check changed service state")
	}
}

func TestBinaryReleaseValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*updateFixture)
	}{
		{"wrong manifest digest", func(f *updateFixture) { f.release.Assets[0].Digest = "sha256:" + strings.Repeat("0", 64) }},
		{"missing API digest", func(f *updateFixture) { f.release.Assets[0].Digest = "" }},
		{"oversized manifest", func(f *updateFixture) { f.release.Assets[0].Size = maxManifestSize + 1 }},
		{"foreign repository", func(f *updateFixture) {
			f.release.Assets[0].URL = strings.Replace(f.release.Assets[0].URL, "KyuubiRan/tailscale-android-cli", "other/repo", 1)
		}},
		{"duplicate manifest", func(f *updateFixture) { f.release.Assets = append(f.release.Assets, f.release.Assets[0]) }},
		{"forged stable channel", func(f *updateFixture) {
			f.release.Prerelease = false
			f.release.Tag = "android-v1.104.2-r5"
			f.rebuild(t)
		}},

		{"package digest mismatch", func(f *updateFixture) { f.release.Assets[1].Digest = "sha256:" + strings.Repeat("0", 64) }},
		{"wrong asset name", func(f *updateFixture) {
			f.manifest.PackageName = "../../other.zip"
			data, _ := json.Marshal(f.manifest)
			asset := &f.release.Assets[0]
			asset.Digest, asset.Size = "sha256:"+testHash(data), int64(len(data))
			f.routes[asset.URL] = data
		}},
		{"release tag mismatch", func(f *updateFixture) { f.release.Tag = "android-v1.106.0-dev.5"; f.rebuild(t) }},
		{"wrong architecture", func(f *updateFixture) { f.manifest.Architecture = "amd64"; f.rebuild(t) }},
		{"untrusted source", func(f *updateFixture) { f.manifest.SourceCommit = "not-a-commit"; f.rebuild(t) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpdateFixture(t, false)
			tc.mutate(f)
			data, _ := json.Marshal([]binaryRelease{f.release})
			f.routes[releaseAPI] = data
			if _, err := f.u.check(context.Background()); err == nil {
				t.Error("invalid release accepted")
			}
			if len(f.calls) != 0 {
				t.Error("invalid release stopped service")
			}
		})
	}
	f := newUpdateFixture(t, false)
	f.manifest.Compatibility = 2
	f.rebuild(t)
	plan, err := f.u.check(context.Background())
	if err != nil || plan.Status.Supported || plan.Status.Reason != "module-upgrade-required" {
		t.Fatal("incompatible release accepted")
	}
	f = newUpdateFixture(t, false)
	f.manifest.TailscaleVersion = "1.102.0"
	f.release.Tag = "android-v1.102.0-dev.5"
	f.manifest.PackageName = "Tailscale-1.102.0-r5-arm64.zip"
	f.rebuild(t)
	plan, err = f.u.check(context.Background())
	if err != nil || plan.Status.UpdateAvailable || plan.Status.Reason != "downgrade-blocked" {
		t.Fatal("downgrade offered")
	}
}

func TestBinaryHTTPSBoundary(t *testing.T) {
	for _, value := range []string{"https://github.com/KyuubiRan/tailscale-android-cli/releases/download/v1.2.3/a", "https://api.github.com/a", "https://release-assets.githubusercontent.com/a?signature=anonymous"} {
		if !allowedUpdateURL(value) {
			t.Error("expected GitHub HTTPS URL")
		}
	}
	for _, value := range []string{"http://github.com/a", "https://github.com.evil.test/a", "https://evilgithubusercontent.com/a", "https://user:password@github.com/a", "https://github.com:444/a", "file:///tmp/a", "https://github.com/a#fragment", "https://githubusercontent.com/a"} {
		if allowedUpdateURL(value) {
			t.Error("untrusted redirect allowed")
		}
	}
	req, _ := http.NewRequest(http.MethodGet, "https://evil.test/a", nil)
	if updateRedirect(req, nil) == nil {
		t.Error("foreign redirect accepted")
	}
	req, _ = http.NewRequest(http.MethodGet, "https://release-assets.githubusercontent.com/a", nil)
	if updateRedirect(req, make([]*http.Request, 5)) == nil {
		t.Error("redirect loop allowed")
	}
	u := newBinaryUpdater(&backend{module: t.TempDir()})
	defer u.client.CloseIdleConnections()
	transport := u.client.Transport.(*http.Transport)
	if transport.Proxy == nil || transport.DialContext == nil || transport.TLSClientConfig.InsecureSkipVerify || transport.TLSHandshakeTimeout <= 0 || u.client.CheckRedirect == nil {
		t.Error("TLS, proxy, timeout, or redirect protection missing")
	}
	if _, err := readBounded(strings.NewReader(strings.Repeat("x", 20)), 16); err == nil {
		t.Error("oversized streamed body accepted")
	}
}

func TestBinaryAndroidELFValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "candidate")
	for _, arch := range []string{"arm64", "arm", "amd64"} {
		mustWrite(t, path, testAndroidELF(arch), 0700)
		if validateAndroidELF(path, arch) != nil {
			t.Errorf("valid Android ELF rejected for %s", arch)
		}
	}
	mutations := []struct {
		name   string
		change func([]byte)
	}{
		{"wrong ABI", func(b []byte) { binary.LittleEndian.PutUint16(b[18:], 62) }},
		{"not PIE", func(b []byte) { binary.LittleEndian.PutUint16(b[16:], 2) }},
		{"Linux interpreter", func(b []byte) { copy(b[176:], "/lib/ld-linux.so.2\x00") }},
		{"4K-only alignment", func(b []byte) { binary.LittleEndian.PutUint64(b[112:], 4096) }},
		{"no interpreter", func(b []byte) { binary.LittleEndian.PutUint32(b[120:], 0) }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			data := testAndroidELF("arm64")
			tc.change(data)
			mustWrite(t, path, data, 0700)
			if validateAndroidELF(path, "arm64") == nil {
				t.Error("invalid ELF accepted")
			}
		})
	}
	mustWrite(t, path, []byte("not ELF"), 0700)
	if validateAndroidELF(path, "arm64") == nil {
		t.Error("non-ELF accepted")
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxPackageSize + 1); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if validateAndroidELF(path, "arm64") == nil {
		t.Error("oversized candidate accepted")
	}
}

func TestBinaryUpdateTransactionAndRollback(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(map[bool]string{false: "stopped", true: "running"}[active], func(t *testing.T) {
			f := newUpdateFixture(t, active)
			originalBuild, _ := os.ReadFile(filepath.Join(f.u.module, "build-info.json"))
			plan, err := f.u.update(context.Background())
			if err != nil || plan.Status.UpdateAvailable {
				t.Fatal("update failed")
			}
			want := []string{"stop"}
			if active {
				want = append(want, "start")
			}
			if !reflect.DeepEqual(f.calls, want) {
				t.Fatalf("wrong service transitions: %v", f.calls)
			}
			f.assertClean(t, f.new, active)
			if runtimeBinaryVersion(f.u.module) != "1.104.2" {
				t.Error("runtime metadata missing or unverified")
			}
			currentBuild, _ := os.ReadFile(filepath.Join(f.u.module, "build-info.json"))
			if !bytes.Equal(currentBuild, originalBuild) {
				t.Error("installation metadata was rewritten")
			}
			second, err := f.u.check(context.Background())
			if err != nil || second.Status.UpdateAvailable {
				t.Error("same core hash must be current")
			}
		})
	}
	t.Run("start failure", func(t *testing.T) {
		f := newUpdateFixture(t, true)
		f.failStart = true
		oldRuntime, _ := json.Marshal(binaryMetadata{TailscaleVersion: "1.104.2", SourceCommit: strings.Repeat("a", 40), Architecture: "arm64", Compatibility: 1, ReleaseChannel: "prerelease", BinarySHA256: testHash(f.old)})
		mustWrite(t, filepath.Join(f.u.module, "runtime-binary.json"), oldRuntime, 0600)
		if _, err := f.u.update(context.Background()); err == nil {
			t.Fatal("startup failure ignored")
		}
		f.assertClean(t, f.old, true)
		if !reflect.DeepEqual(f.calls, []string{"stop", "start", "stop", "start"}) {
			t.Fatalf("rollback did not restart prior service: %v", f.calls)
		}
		restored, _ := os.ReadFile(filepath.Join(f.u.module, "runtime-binary.json"))
		if !bytes.Equal(restored, oldRuntime) {
			t.Fatal("runtime metadata not restored")
		}
	})
	t.Run("candidate failure before stop", func(t *testing.T) {
		f := newUpdateFixture(t, true)
		f.u.version = func(context.Context, string, binaryMetadata) error { return errors.New("bad candidate") }
		if _, err := f.u.update(context.Background()); err == nil {
			t.Fatal("candidate rejected too late")
		}
		f.assertClean(t, f.old, true)
		if len(f.calls) != 0 {
			t.Fatal("service stopped before verification")
		}
	})
	t.Run("package corruption before stop", func(t *testing.T) {
		f := newUpdateFixture(t, true)
		f.routes[f.release.Assets[1].URL] = []byte("corrupted")
		if _, err := f.u.update(context.Background()); err == nil {
			t.Fatal("corrupted package accepted")
		}
		f.assertClean(t, f.old, true)
		if len(f.calls) != 0 {
			t.Fatal("service stopped before verification")
		}
	})
}

func TestBinaryUpdateGlobalLock(t *testing.T) {
	f := newUpdateFixture(t, false)
	first, err := f.u.lock()
	if err != nil {
		t.Fatal(err)
	}
	if second, err := f.u.lock(); err == nil {
		unlockUpdate(second)
		unlockUpdate(first)
		t.Fatal("concurrent update acquired lock")
	}
	unlockUpdate(first)
	next, err := f.u.lock()
	if err != nil {
		t.Fatal("lock was not released")
	}
	unlockUpdate(next)
}

func TestBinaryVersionProbeAndActualHash(t *testing.T) {
	f := newUpdateFixture(t, false)
	path := filepath.Join(f.u.module, "bin/tailscaled")
	expected := f.info.TailscaleVersion + "-shellscaled-" + f.info.SourceCommit[:12]
	script := "#!/system/bin/sh\n[ \"$#\" = 1 ] && [ \"$1\" = --version ] && [ \"$TS_BE_CLI\" = 0 ] || exit 1\nprintf '%s\\n' '" + expected + "'\n"
	mustWrite(t, path, []byte(script), 0755)
	if err := checkBinaryVersion(context.Background(), path, f.info); err != nil {
		t.Fatal("fixed candidate version probe failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	plan, err := f.u.check(ctx)
	if err != nil || !plan.Status.UpdateAvailable || plan.CurrentHash != testHash([]byte(script)) {
		t.Fatal("actual executable hash was ignored")
	}
	if runtimeBinaryVersion(f.u.module) != "" {
		t.Fatal("missing runtime metadata was trusted")
	}
	bad := f.info
	bad.TailscaleVersion = "1.2.3"
	if checkBinaryVersion(context.Background(), path, bad) == nil {
		t.Fatal("wrong candidate version accepted")
	}
}

func TestBinarySharedControlLock(t *testing.T) {
	f := newUpdateFixture(t, false)
	owner, err := f.u.acquireControlLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Ordinary ctl.sh uses exactly this mkdir primitive. It cannot acquire the
	// service mutex during any part of stop/replacement/start or rollback.
	if err := os.Mkdir(owner.directory, 0700); !os.IsExist(err) {
		t.Fatal("ordinary controller stole the service mutex")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if other, err := f.u.acquireControlLock(ctx); err == nil {
		other.release()
		t.Fatal("second controller stole a live owner lock")
	}
	path := filepath.Join(owner.directory, owner.owner)
	original, _ := os.ReadFile(path)
	mustWrite(t, path, []byte("different owner token"), 0600)
	owner.release()
	if _, err := os.Stat(path); err != nil {
		t.Fatal("release removed a lock it no longer owns")
	}
	mustWrite(t, path, original, 0600)
	owner.release()
	if err := os.Mkdir(owner.directory, 0700); err != nil {
		t.Fatal("ordinary controller could not acquire released mutex")
	}
	_ = os.Remove(owner.directory)
	if err := os.Mkdir(owner.directory, 0700); err != nil {
		t.Fatal(err)
	}
	// A reused PID with a different kernel start time is safely reclaimed.
	pid := strings.Split(owner.owner, ".")[0]
	mustWrite(t, filepath.Join(owner.directory, pid+".0"), []byte(pid+" 0\n"), 0600)
	next, err := f.u.acquireControlLock(context.Background())
	if err != nil {
		t.Fatal("stale service mutex was not reclaimed")
	}
	next.release()
}

func TestBinaryInterruptedStaging(t *testing.T) {
	f := newUpdateFixture(t, false)
	for _, name := range []string{".tailscaled-update.candidate", ".tailscaled-update.zip"} {
		mustWrite(t, filepath.Join(f.u.module, "bin", name), []byte("interrupted download"), 0600)
	}
	if _, err := f.u.update(context.Background()); err != nil {
		t.Fatal("orphaned staging prevented a safe retry")
	}
	f.assertClean(t, f.new, false)
	previous := filepath.Join(f.u.module, "bin/.tailscaled-update.previous")
	mustWrite(t, previous, f.old, 0600)
	if _, err := f.u.update(context.Background()); !errors.Is(err, errUpdateRollback) {
		t.Fatal("recovery inode was overwritten")
	}
	recovery, _ := os.ReadFile(previous)
	if !bytes.Equal(recovery, f.old) {
		t.Fatal("recovery inode was modified")
	}
}

func TestBinaryReleaseChannelSelection(t *testing.T) {
	early := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	releases := []binaryRelease{
		{Tag: "android-v1.104.0-r5", PublishedAt: early},
		{Tag: "android-v1.104.2-r5", PublishedAt: early.Add(time.Hour)},
		{Tag: "android-v1.104.2-dev.6", Prerelease: true, PublishedAt: early.Add(2 * time.Hour)},
		{Tag: "android-v1.106.0-r1", Draft: true, PublishedAt: early.Add(3 * time.Hour)},
		{Tag: "android-v1.108.0-dev.1", Draft: true, Prerelease: true, PublishedAt: early.Add(4 * time.Hour)},
	}
	for _, tc := range []struct{ channel, want string }{{"stable", "android-v1.104.2-r5"}, {"preview", "android-v1.104.2-dev.6"}} {
		release, ok := selectBinaryRelease(releases, tc.channel)
		if !ok || release.Tag != tc.want {
			t.Fatal("wrong channel or publication ordering")
		}
	}
	if _, ok := selectBinaryRelease([]binaryRelease{releases[2], releases[3], releases[4]}, "stable"); ok {
		t.Fatal("stable channel accepted prerelease or draft")
	}
	f := newUpdateFixture(t, false)
	// Simulate a prerelease-installed module whose selected core came from a
	// stable release. Runtime metadata must not change the installed channel.
	f.release.Prerelease = false
	f.release.Tag = "android-v1.104.2-r5"
	f.info.ReleaseChannel = "stable"
	f.manifest.ReleaseChannel = "stable"
	f.rebuild(t)
	plan, err := f.u.update(context.Background())
	if err != nil || plan.Status.UpdateAvailable {
		t.Fatal("pre module could not install stable core")
	}
	if readUpdateChannel(f.u.stateDir, f.u.module) != "preview" {
		t.Fatal("runtime metadata changed installed channel")
	}
	// A stable-installed module must never offer an RC, even if it is newer.
	data, err := os.ReadFile(filepath.Join(f.u.module, "build-info.json"))
	if err != nil {
		t.Fatal(err)
	}
	var installed binaryMetadata
	if json.Unmarshal(data, &installed) != nil {
		t.Fatal("invalid fixture metadata")
	}
	installed.ReleaseChannel = "stable"
	data, _ = json.Marshal(installed)
	mustWrite(t, filepath.Join(f.u.module, "build-info.json"), data, 0600)
	// Runtime core hash matches but the installation channel remains stable.
	f.release.Prerelease = true
	f.release.Tag = "android-v1.104.2-dev.5"
	f.info.ReleaseChannel = "prerelease"
	f.manifest.ReleaseChannel = "prerelease"
	f.rebuild(t)
	plan, err = f.u.check(context.Background())
	if err != nil || plan.Status.Supported || plan.Status.Reason != "no-compatible-release" {
		t.Fatal("stable module offered RC")
	}
	f.release.Draft = true
	data, _ = json.Marshal([]binaryRelease{f.release})
	f.routes[releaseAPI] = data
	plan, err = f.u.check(context.Background())
	if err != nil || plan.Status.Supported {
		t.Fatal("draft release was offered")
	}
}

func TestBinaryManifestChannelAuthenticity(t *testing.T) {
	f := newUpdateFixture(t, false)
	f.manifest.ReleaseChannel = "stable"
	f.rebuild(t)
	if _, err := f.u.check(context.Background()); err == nil {
		t.Fatal("RC manifest falsely declared stable")
	}
	f = newUpdateFixture(t, false)
	f.info.ReleaseChannel = "stable"
	f.rebuild(t)
	if _, err := f.u.update(context.Background()); err == nil {
		t.Fatal("ZIP channel disagrees with release and manifest")
	}
	f.assertClean(t, f.old, false)
}

func TestBinaryReleaseVersionOrdering(t *testing.T) {
	timestamp := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	cases := []struct{ name, stableTag, previewTag, want string }{
		{"higher stable beats newer old preview", "android-v1.106.0-r1", "android-v1.104.2-dev.9", "android-v1.106.0-r1"},
		{"higher preview beats older stable", "android-v1.106.0-r8", "android-v1.108.0-dev.1", "android-v1.108.0-dev.1"},
		{"revision is numeric", "android-v1.104.2-r10", "android-v1.104.2-dev.9", "android-v1.104.2-r10"},
		{"higher preview revision", "android-v1.104.2-r4", "android-v1.104.2-dev.5", "android-v1.104.2-dev.5"},
		{"stable wins same version and revision", "android-v1.104.2-r5", "android-v1.104.2-dev.5", "android-v1.104.2-r5"},
		{"run exceeds 32 bit", "android-v1.104.2-r4294967296", "android-v1.104.2-dev.4294967295", "android-v1.104.2-r4294967296"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stable := binaryRelease{Tag: tc.stableTag, PublishedAt: timestamp}
			preview := binaryRelease{Tag: tc.previewTag, Prerelease: true, PublishedAt: timestamp.Add(time.Hour)}
			for _, list := range [][]binaryRelease{{preview, stable}, {stable, preview}} {
				selected, ok := selectBinaryRelease(list, "preview")
				if !ok || selected.Tag != tc.want || selected.Prerelease != strings.Contains(tc.want, "-dev.") {
					t.Fatal("wrong semantic release order")
				}
			}
		})
	}
	older := binaryRelease{Tag: "android-v1.104.2-r5", PublishedAt: timestamp}
	newer := older
	newer.PublishedAt = timestamp.Add(time.Hour)
	selected, ok := selectBinaryRelease([]binaryRelease{newer, older}, "stable")
	if !ok || !selected.PublishedAt.Equal(newer.PublishedAt) {
		t.Fatal("publication tie break failed")
	}
}

func TestBinaryChannelPreferenceIsLocalAndPersistent(t *testing.T) {
	f := newUpdateFixture(t, false)
	requests := 0
	b := &backend{module: f.u.module, stateDir: f.u.stateDir, client: &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.URL.Host != "local-tailscaled.sock" {
			t.Fatal("channel setting accessed the Internet")
		}
		body := `{}`
		if req.URL.Path == "/localapi/v0/status" {
			body = `{"BackendState":"Running","TailscaleIPs":[]}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}
	if readUpdateChannel(f.u.stateDir, f.u.module) != "preview" {
		t.Fatal("prerelease module must default to preview")
	}
	for _, channel := range []string{"stable", "preview"} {
		r := b.binaryChannel(channel)
		if !r.OK || r.UpdateChannel != channel || requests != 0 || readUpdateChannel(f.u.stateDir, f.u.module) != channel {
			t.Fatal("channel save was not local")
		}
		stat, err := os.Stat(filepath.Join(f.u.stateDir, "ui.update-channel"))
		if err != nil || stat.Mode().Perm() != 0600 {
			t.Fatal("channel preference is not private")
		}
	}
	installedData, _ := os.ReadFile(filepath.Join(f.u.module, "build-info.json"))
	var installed binaryMetadata
	if json.Unmarshal(installedData, &installed) != nil {
		t.Fatal("bad fixture metadata")
	}
	installed.ReleaseChannel = "stable"
	installedData, _ = json.Marshal(installed)
	mustWrite(t, filepath.Join(f.u.module, "build-info.json"), installedData, 0600)
	// A new whole-module installation must preserve the user's private choice.
	if readUpdateChannel(f.u.stateDir, f.u.module) != "preview" {
		t.Fatal("module update replaced the saved channel")
	}
	runtimeInfo := f.info
	runtimeInfo.ReleaseChannel = "stable"
	runtimeData, _ := json.Marshal(runtimeInfo)
	mustWrite(t, filepath.Join(f.u.module, "runtime-binary.json"), runtimeData, 0600)
	if readUpdateChannel(f.u.stateDir, f.u.module) != "preview" {
		t.Fatal("runtime metadata changed the user's choice")
	}
	status := b.status(context.Background())
	if !status.OK || status.UpdateChannel != "preview" || requests != 2 {
		t.Fatal("ordinary local status omitted the channel")
	}
	requests = 0
	if r := b.binaryChannel("../../preview"); r.OK || requests != 0 || readUpdateChannel(f.u.stateDir, f.u.module) != "preview" {
		t.Fatal("invalid channel changed settings")
	}
	lock, err := f.u.lock()
	if err != nil {
		t.Fatal(err)
	}
	r := b.binaryChannel("stable")
	unlockUpdate(lock)
	if r.OK || readUpdateChannel(f.u.stateDir, f.u.module) != "preview" {
		t.Fatal("channel changed during an active update")
	}
	if err := os.Remove(filepath.Join(f.u.stateDir, "ui.update-channel")); err != nil {
		t.Fatal(err)
	}
	if readUpdateChannel(f.u.stateDir, f.u.module) != "stable" {
		t.Fatal("stable module must default to stable")
	}
	canary, _ := os.ReadFile(f.statePath)
	if string(canary) != "anonymous-account-and-prefs-canary" {
		t.Fatal("channel setting modified account state")
	}
}

func TestBinaryCheckUsesSavedChannel(t *testing.T) {
	f := newUpdateFixture(t, false)
	b := &backend{module: f.u.module, stateDir: f.u.stateDir}
	if !b.binaryChannel("stable").OK {
		t.Fatal("could not select stable")
	}
	plan, err := f.u.check(context.Background())
	if err != nil || plan.Status.Channel != "stable" || plan.Status.Supported {
		t.Fatal("stable preference allowed RC")
	}
	if !b.binaryChannel("preview").OK {
		t.Fatal("could not select preview")
	}
	plan, err = f.u.check(context.Background())
	if err != nil || plan.Status.Channel != "preview" || !plan.Status.Supported || !plan.Status.UpdateAvailable {
		t.Fatal("preview preference did not allow RC")
	}
	if _, err := os.Stat(filepath.Join(f.u.stateDir, "ui.update-channel.update")); !os.IsNotExist(err) {
		t.Fatal("preference staging file retained")
	}
}

func TestBinaryCLIReleaseNamesAndFlags(t *testing.T) {
	timestamp := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	for _, release := range []binaryRelease{
		{Tag: "android-v1.104.2-dev.5", PublishedAt: timestamp},
		{Tag: "android-v1.104.2-r5", Prerelease: true, PublishedAt: timestamp},
		{Tag: "v1.104.2-r5", Prerelease: true, PublishedAt: timestamp},
		{Tag: "android-v1.104.2-dev.0", Prerelease: true, PublishedAt: timestamp},
		{Tag: "android-v1.104.2-dev.12345678901", Prerelease: true, PublishedAt: timestamp},
	} {
		if _, ok := selectBinaryRelease([]binaryRelease{release}, "preview"); ok {
			t.Fatal("invalid CLI tag/channel accepted")
		}
	}
	f := newUpdateFixture(t, false)
	f.routes[releaseAPI] = []byte(`[]`)
	plan, err := f.u.check(context.Background())
	if err != nil || !plan.Status.Checked || plan.Status.Supported || plan.Status.Reason != "no-compatible-release" {
		t.Fatal("new CLI repository without releases was not handled")
	}
	f = newUpdateFixture(t, false)
	f.release.Tag = "android-v1.104.2-dev.4294967296"
	f.manifest.PackageName = "Tailscale-1.104.2-r4294967296-arm64.zip"
	f.rebuild(t)
	plan, err = f.u.check(context.Background())
	if err != nil || !plan.Status.UpdateAvailable {
		t.Fatal("large valid CI run rejected")
	}
	f.manifest.PackageName = "Tailscale-1.104.2-r4294967295-arm64.zip"
	f.rebuild(t)
	if _, err := f.u.check(context.Background()); err == nil {
		t.Fatal("CI run was not bound to asset basename")
	}
	oldRepo := releaseAsset{Name: "Tailscale-1.104.2-r5-arm64.zip", URL: "https://github.com/KyuubiRan/Shellscaled/releases/download/android-v1.104.2-dev.5/Tailscale-1.104.2-r5-arm64.zip", Digest: "sha256:" + strings.Repeat("a", 64), Size: 100}
	if assetMatches(oldRepo, "android-v1.104.2-dev.5", maxPackageSize) {
		t.Fatal("module repository was still accepted as the updater source")
	}
}

func TestBinaryLatestStableBeyondRecentReleases(t *testing.T) {
	for _, tc := range []struct {
		name, channel string
		outsideList   bool
	}{
		{"preview includes higher stable outside twenty previews", "preview", true},
		{"stable finds its release outside twenty previews", "stable", true},
		{"latest refreshes a duplicate stale list entry", "preview", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpdateFixture(t, false)
			f.info.TailscaleVersion, f.info.ReleaseChannel = "1.106.0", "stable"
			f.manifest.TailscaleVersion, f.manifest.ReleaseChannel = "1.106.0", "stable"
			f.manifest.PackageName = "Tailscale-1.106.0-r25-arm64.zip"
			f.release.Tag, f.release.Prerelease = "android-v1.106.0-r25", false
			f.release.HTMLURL = "https://github.com/KyuubiRan/tailscale-android-cli/releases/tag/" + f.release.Tag
			f.release.PublishedAt = time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
			f.rebuild(t)
			stable, _ := json.Marshal(f.release)
			f.routes[latestStableReleaseAPI] = stable
			var recent []binaryRelease
			if tc.outsideList {
				for i := 0; i < 20; i++ {
					recent = append(recent, binaryRelease{Tag: "android-v1.104.2-dev." + strconv.Itoa(100+i), Prerelease: true,
						PublishedAt: f.release.PublishedAt.Add(time.Duration(i+1) * time.Hour)})
				}
			} else {
				stale := f.release
				stale.Assets = nil
				recent = []binaryRelease{stale, stale}
			}
			f.routes[releaseAPI], _ = json.Marshal(recent)
			mustWrite(t, filepath.Join(f.u.stateDir, "ui.update-channel"), []byte(tc.channel+"\n"), 0600)
			original := f.u.client.Transport
			var apiCalls []string
			f.u.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "api.github.com" {
					apiCalls = append(apiCalls, req.URL.String())
				}
				return original.RoundTrip(req)
			})
			plan, err := f.u.check(context.Background())
			if err != nil || !plan.Status.Checked || !plan.Status.Supported || !plan.Status.UpdateAvailable ||
				plan.Status.LatestVersion != "1.106.0" || plan.Manifest.ReleaseChannel != "stable" || plan.Manifest.SourceCommit != f.info.SourceCommit {
				t.Fatalf("higher stable release was not selected: status=%+v err=%v", plan.Status, err)
			}
			if !reflect.DeepEqual(apiCalls, []string{releaseAPI, latestStableReleaseAPI}) {
				t.Fatal("release discovery escaped the two fixed API requests")
			}
			f.assertClean(t, f.old, false)
		})
	}
}

func TestBinaryLatestStableNotFound(t *testing.T) {
	for _, channel := range []string{"stable", "preview"} {
		t.Run(channel, func(t *testing.T) {
			f := newUpdateFixture(t, false)
			mustWrite(t, filepath.Join(f.u.stateDir, "ui.update-channel"), []byte(channel+"\n"), 0600)
			original := f.u.client.Transport
			latestCalls := 0
			f.u.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() == latestStableReleaseAPI {
					latestCalls++
					// A 404 is absence, even if GitHub's error body is not release JSON.
					return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not release JSON")), Header: make(http.Header), Request: req}, nil
				}
				return original.RoundTrip(req)
			})
			plan, err := f.u.check(context.Background())
			if err != nil || !plan.Status.Checked || latestCalls != 1 {
				t.Fatalf("latest stable 404 did not fall back to the recent list: %v", err)
			}
			if channel == "preview" && (!plan.Status.Supported || !plan.Status.UpdateAvailable) {
				t.Fatal("404 hid a compatible preview")
			}
			if channel == "stable" && (plan.Status.Supported || plan.Status.Reason != "no-compatible-release") {
				t.Fatal("404 allowed a preview into the stable channel")
			}
			f.assertClean(t, f.old, false)
		})
	}
}

func TestBinaryLatestStableRequestFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		err    error
		want   error
	}{
		{"forbidden", http.StatusForbidden, `{}`, nil, errUpdateCheck},
		{"rate limit", http.StatusTooManyRequests, `{}`, nil, errUpdateCheck},
		{"server failure", http.StatusServiceUnavailable, `{}`, nil, errUpdateCheck},
		{"network failure", 0, "", errors.New("anonymous network failure"), errUpdateCheck},
		{"malformed release JSON", http.StatusOK, `{`, nil, errUpdateInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpdateFixture(t, false)
			original := f.u.client.Transport
			f.u.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != latestStableReleaseAPI {
					return original.RoundTrip(req)
				}
				if tc.err != nil {
					return nil, tc.err
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), ContentLength: int64(len(tc.body)), Header: make(http.Header), Request: req}, nil
			})
			plan, err := f.u.check(context.Background())
			if !errors.Is(err, tc.want) || plan.Status.Checked || plan.Status.UpdateAvailable {
				t.Fatalf("latest stable failure was silently ignored: status=%+v err=%v", plan.Status, err)
			}
			f.assertClean(t, f.old, false)
		})
	}
}

func TestBinaryLatestStableAuthenticity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"foreign repository", func(data map[string]any) {
			data["html_url"] = "https://github.com/other/repo/releases/tag/android-v1.102.0-r1"
		}},
		{"missing repository URL", func(data map[string]any) { delete(data, "html_url") }},
		{"preview", func(data map[string]any) { data["prerelease"] = true }},
		{"preview tag with stable flag", func(data map[string]any) {
			data["tag_name"] = "android-v1.102.0-dev.1"
			data["html_url"] = "https://github.com/KyuubiRan/tailscale-android-cli/releases/tag/android-v1.102.0-dev.1"
		}},
		{"draft", func(data map[string]any) { data["draft"] = true }},
		{"missing draft flag", func(data map[string]any) { delete(data, "draft") }},
		{"missing prerelease flag", func(data map[string]any) { delete(data, "prerelease") }},
		{"null prerelease flag", func(data map[string]any) { data["prerelease"] = nil }},
		{"wrong flag type", func(data map[string]any) { data["prerelease"] = "false" }},
		{"unpublished", func(data map[string]any) { data["published_at"] = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpdateFixture(t, false)
			// This stable is older than the valid list candidate, so silently
			// filtering a forged latest response would incorrectly succeed.
			base := binaryRelease{Tag: "android-v1.102.0-r1", HTMLURL: "https://github.com/KyuubiRan/tailscale-android-cli/releases/tag/android-v1.102.0-r1",
				PublishedAt: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)}
			encoded, _ := json.Marshal(base)
			var data map[string]any
			if json.Unmarshal(encoded, &data) != nil {
				t.Fatal("invalid fixture release")
			}
			tc.mutate(data)
			f.routes[latestStableReleaseAPI], _ = json.Marshal(data)
			if _, err := f.u.check(context.Background()); !errors.Is(err, errUpdateInvalid) {
				t.Fatalf("invalid latest stable response was ignored: %v", err)
			}
			f.assertClean(t, f.old, false)
		})
	}
}
