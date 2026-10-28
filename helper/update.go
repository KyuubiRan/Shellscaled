// Copyright (c) 2026 KyuubiRan
// SPDX-License-Identifier: BSD-3-Clause
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const releaseAPI = "https://api.github.com/repos/KyuubiRan/tailscale-android-cli/releases?per_page=20"
const latestStableReleaseAPI = "https://api.github.com/repos/KyuubiRan/tailscale-android-cli/releases/latest"
const binaryCompatibility = 1
const maxPackageSize = 64 << 20
const maxManifestSize = 16 << 10

var stableVersionPattern = regexp.MustCompile(`^[0-9]{1,6}\.[0-9]{1,6}\.[0-9]{1,6}$`)
var binaryAssetPattern = regexp.MustCompile(`^Tailscale-([0-9]{1,6}\.[0-9]{1,6}\.[0-9]{1,6})-r([1-9][0-9]{0,9})-(arm64|arm|amd64)\.binary\.json$`)
var releaseTagPattern = regexp.MustCompile(`^android-v([0-9]{1,6}\.[0-9]{1,6}\.[0-9]{1,6})-(dev\.|r)([1-9][0-9]{0,9})$`)
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

var errUpdateBusy = errors.New("二进制更新正在进行，请稍后重试")
var errUpdateCheck = errors.New("无法检查二进制更新，请稍后重试")
var errUpdateInvalid = errors.New("更新文件校验失败，未替换当前二进制")
var errUpdateInstall = errors.New("二进制更新失败，已尝试恢复原服务")
var errUpdateRollback = errors.New("二进制恢复失败，请重新安装模块")

// Binary updates deliberately have no module versionCode or UI/script version.
type binaryStatus struct {
	CurrentVersion  string `json:"currentVersion"`
	LatestVersion   string `json:"latestVersion"`
	UpdateAvailable bool   `json:"updateAvailable"`
	Checked         bool   `json:"checked"`
	Supported       bool   `json:"supported"`
	Reason          string `json:"reason,omitempty"`
	Channel         string `json:"channel"`
}

type binaryMetadata struct {
	TailscaleVersion string `json:"tailscaleVersion"`
	SourceCommit     string `json:"sourceCommit"`
	Architecture     string `json:"architecture"`
	BinarySHA256     string `json:"binarySHA256"`
	Compatibility    int    `json:"binaryCompatibility"`
	ReleaseChannel   string `json:"releaseChannel"`
}

type binaryManifest struct {
	Schema           int    `json:"schema"`
	Compatibility    int    `json:"compatibility"`
	TailscaleVersion string `json:"tailscaleVersion"`
	SourceCommit     string `json:"sourceCommit"`
	Architecture     string `json:"architecture"`
	BinarySHA256     string `json:"binarySHA256"`
	PackageName      string `json:"packageName"`
	PackageSHA256    string `json:"packageSHA256"`
	ReleaseChannel   string `json:"releaseChannel"`
}

type releaseAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

type binaryRelease struct {
	Tag         string         `json:"tag_name"`
	HTMLURL     string         `json:"html_url"`
	Draft       bool           `json:"draft"`
	Prerelease  bool           `json:"prerelease"`
	Assets      []releaseAsset `json:"assets"`
	PublishedAt time.Time      `json:"published_at"`
}

type binaryPlan struct {
	Status      binaryStatus
	Manifest    binaryManifest
	Package     releaseAsset
	CurrentHash string
}

// The dependency hooks are internal test seams. RPC callers supply none of them.
type binaryUpdater struct {
	module       string
	stateDir     string
	arch         string
	client       *http.Client
	running      func(context.Context) (bool, error)
	control      func(context.Context, string) error
	version      func(context.Context, string, binaryMetadata) error
	controlOwner string
}

func newBinaryUpdater(b *backend) *binaryUpdater {
	dialer := &net.Dialer{Timeout: 15 * time.Second, Control: func(_, _ string, raw syscall.RawConn) error {
		var socketErr error
		if err := raw.Control(func(fd uintptr) {
			// Match the daemon's owned bypass bit and Android protectedFromVpn bit.
			socketErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, 0x10020000)
		}); err != nil {
			return err
		}
		return socketErr
	}}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: dialer.DialContext,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 15 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second, IdleConnTimeout: 15 * time.Second}
	u := &binaryUpdater{module: b.module, stateDir: b.updateDataDir(), arch: runtime.GOARCH,
		client: &http.Client{Transport: transport, Timeout: 80 * time.Second, CheckRedirect: updateRedirect}, version: checkBinaryVersion}
	u.running = func(ctx context.Context) (bool, error) {
		ctx, cancel := context.WithTimeout(ctx, 7*time.Second)
		defer cancel()
		out, _, _, err := run(ctx, filepath.Join(b.module, "ctl.sh"), "status")
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if strings.Contains(out, "Daemon: running (") {
			return true, nil
		}
		if strings.Contains(out, "Daemon: stopped") {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return false, errUpdateInstall
	}
	u.control = func(ctx context.Context, action string) error {
		if action != "start" && action != "stop" {
			return errUpdateInstall
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, filepath.Join(b.module, "ctl.sh"), action)
		cmd.WaitDelay = time.Second
		cmd.Env = append(os.Environ(), "TS_BE_CLI=0", "SC_BINARY_UPDATE_OWNER="+u.controlOwner)
		return cmd.Run()
	}
	return u
}

func allowedUpdateURL(value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "api.github.com" || host == "github.com" || strings.HasSuffix(host, ".githubusercontent.com")
}

func updateRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 || !allowedUpdateURL(req.URL.String()) {
		return errUpdateInvalid
	}
	return nil
}

func readBounded(reader io.Reader, maximum int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil || int64(len(data)) > maximum {
		return nil, errUpdateInvalid
	}
	return data, nil
}

func readSmallFile(path string, maximum int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() > maximum {
		return nil, errUpdateInvalid
	}
	return readBounded(file, maximum)
}

func hashFile(path string) (string, error) {
	stat, err := os.Lstat(path)
	if err != nil || !stat.Mode().IsRegular() || stat.Size() <= 0 || stat.Size() > maxPackageSize {
		return "", errUpdateInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errUpdateInvalid
	}
	defer file.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(file, maxPackageSize+1))
	if err != nil || n != stat.Size() {
		return "", errUpdateInvalid
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func decodeUpdateJSON(data []byte, result any, strict bool) error {
	d := json.NewDecoder(bytes.NewReader(data))
	if strict {
		d.DisallowUnknownFields()
	}
	if err := d.Decode(result); err != nil {
		return errUpdateInvalid
	}
	if d.Decode(new(any)) != io.EOF {
		return errUpdateInvalid
	}
	return nil
}

func (u *binaryUpdater) fetch(ctx context.Context, value string, maximum int64) ([]byte, error) {
	data, _, err := u.fetchResponse(ctx, value, maximum)
	return data, err
}

func (u *binaryUpdater) fetchResponse(ctx context.Context, value string, maximum int64) ([]byte, int, error) {
	if !allowedUpdateURL(value) {
		return nil, 0, errUpdateInvalid
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, value, nil)
	if err != nil {
		return nil, 0, errUpdateInvalid
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Shellscaled-binary-updater")
	response, err := u.client.Do(req)
	if err != nil {
		return nil, 0, errUpdateCheck
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > maximum {
		return nil, response.StatusCode, errUpdateCheck
	}
	data, err := readBounded(response.Body, maximum)
	return data, response.StatusCode, err
}

func assetMatches(asset releaseAsset, tag string, maximum int64) bool {
	if asset.Size <= 0 || asset.Size > maximum || !strings.HasPrefix(asset.Digest, "sha256:") || !digestPattern.MatchString(strings.TrimPrefix(asset.Digest, "sha256:")) {
		return false
	}
	expected := "https://github.com/KyuubiRan/tailscale-android-cli/releases/download/" + tag + "/" + asset.Name
	return releaseTagPattern.MatchString(tag) && asset.URL == expected
}

func verifyAsset(data []byte, asset releaseAsset) bool {
	digest := sha256.Sum256(data)
	return int64(len(data)) == asset.Size && "sha256:"+hex.EncodeToString(digest[:]) == asset.Digest
}

func compareVersions(a, b string) (int, error) {
	if !stableVersionPattern.MatchString(a) || !stableVersionPattern.MatchString(b) {
		return 0, errUpdateInvalid
	}
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		x, _ := strconv.Atoi(left[i])
		y, _ := strconv.Atoi(right[i])
		if x < y {
			return -1, nil
		}
		if x > y {
			return 1, nil
		}
	}
	return 0, nil
}

func (u *binaryUpdater) current(ctx context.Context) (binaryMetadata, string, error) {
	var installed binaryMetadata
	data, err := readSmallFile(filepath.Join(u.module, "build-info.json"), maxManifestSize)
	if err != nil || decodeUpdateJSON(data, &installed, false) != nil {
		return installed, "", errUpdateInvalid
	}
	hash, err := hashFile(filepath.Join(u.module, "bin/tailscaled"))
	if err != nil {
		return installed, "", err
	}
	runtimeData, err := readSmallFile(filepath.Join(u.module, "runtime-binary.json"), maxManifestSize)
	if err == nil {
		var updated binaryMetadata
		if decodeUpdateJSON(runtimeData, &updated, true) == nil && updated.BinarySHA256 == hash && updated.Architecture == installed.Architecture && updated.Compatibility == installed.Compatibility && stableVersionPattern.MatchString(updated.TailscaleVersion) && commitPattern.MatchString(updated.SourceCommit) {
			return updated, hash, nil
		}
	}
	if installed.BinarySHA256 == hash && stableVersionPattern.MatchString(installed.TailscaleVersion) {
		return installed, hash, nil
	}
	// Old module builds may lack a binary digest. Read the executable's actual
	// version instead of believing stale installation metadata after a manual edit.
	version, err := readBinaryVersion(ctx, filepath.Join(u.module, "bin/tailscaled"))
	if err != nil {
		return installed, "", errUpdateInvalid
	}
	installed.TailscaleVersion = version
	return installed, hash, nil
}

func (b *backend) updateDataDir() string {
	if b.stateDir != "" {
		return b.stateDir
	}
	return dataDir
}

func readUpdateChannel(directory, module string) string {
	data, err := readSmallFile(filepath.Join(directory, "ui.update-channel"), 32)
	if err == nil {
		if value := strings.TrimSpace(string(data)); value == "stable" || value == "preview" {
			return value
		}
	}
	data, err = readSmallFile(filepath.Join(module, "build-info.json"), maxManifestSize)
	var installed struct {
		ReleaseChannel string `json:"releaseChannel"`
	}
	if err == nil && decodeUpdateJSON(data, &installed, false) == nil && installed.ReleaseChannel == "prerelease" {
		return "preview"
	}
	return "stable"
}

func (b *backend) binaryChannel(channel string) response {
	if channel != "stable" && channel != "preview" {
		return failed("不支持此更新频道")
	}
	// Serialize against checks/updates so changing a selector cannot race an
	// already approved plan in another WebUI process. There is no network call.
	updater := &binaryUpdater{module: b.module, stateDir: b.updateDataDir()}
	lock, err := updater.lock()
	if err != nil {
		return failed(err.Error())
	}
	defer unlockUpdate(lock)
	if err := atomicMetadata(filepath.Join(updater.stateDir, "ui.update-channel"), []byte(channel+"\n")); err != nil {
		return failed("无法保存更新频道，请重试")
	}
	return response{OK: true, UpdateChannel: channel}
}

func newerBinaryRelease(a, b binaryRelease) bool {
	left, right := releaseTagPattern.FindStringSubmatch(a.Tag), releaseTagPattern.FindStringSubmatch(b.Tag)
	if len(left) != 4 || len(right) != 4 {
		return false
	}
	comparison, _ := compareVersions(left[1], right[1])
	if comparison != 0 {
		return comparison > 0
	}
	revisionA, _ := strconv.ParseUint(left[3], 10, 64)
	revisionB, _ := strconv.ParseUint(right[3], 10, 64)
	if revisionA != revisionB {
		return revisionA > revisionB
	}
	if a.Prerelease != b.Prerelease {
		return !a.Prerelease
	}
	return a.PublishedAt.After(b.PublishedAt)
}

func selectBinaryRelease(releases []binaryRelease, channel string) (binaryRelease, bool) {
	var selected binaryRelease
	if channel != "stable" && channel != "preview" {
		return selected, false
	}
	found := false
	for _, release := range releases {
		parts := releaseTagPattern.FindStringSubmatch(release.Tag)
		if release.Draft || release.PublishedAt.IsZero() || len(parts) != 4 || (parts[2] == "dev.") != release.Prerelease || (channel == "stable" && release.Prerelease) {
			continue
		}
		if !found || newerBinaryRelease(release, selected) {
			selected = release
			found = true
		}
	}
	return selected, found
}

func (u *binaryUpdater) releaseCandidates(ctx context.Context) ([]binaryRelease, error) {
	data, err := u.fetch(ctx, releaseAPI, 1<<20)
	if err != nil {
		return nil, err
	}
	var releases []binaryRelease
	if decodeUpdateJSON(data, &releases, false) != nil || len(releases) > 20 {
		return nil, errUpdateInvalid
	}
	// Recent previews can push a higher-version stable release out of the
	// bounded list. The main-branch publisher maintains this fixed stable entry.
	data, status, err := u.fetchResponse(ctx, latestStableReleaseAPI, 1<<20)
	if status == http.StatusNotFound {
		return releases, nil
	}
	if err != nil {
		return nil, err
	}
	var latest struct {
		binaryRelease
		Draft      *bool `json:"draft"`
		Prerelease *bool `json:"prerelease"`
	}
	if decodeUpdateJSON(data, &latest, false) != nil {
		return nil, errUpdateInvalid
	}
	parts := releaseTagPattern.FindStringSubmatch(latest.Tag)
	if latest.Draft == nil || *latest.Draft || latest.Prerelease == nil || *latest.Prerelease ||
		len(parts) != 4 || parts[2] != "r" || latest.PublishedAt.IsZero() ||
		latest.HTMLURL != "https://github.com/KyuubiRan/tailscale-android-cli/releases/tag/"+latest.Tag {
		return nil, errUpdateInvalid
	}
	stable := latest.binaryRelease
	stable.Draft, stable.Prerelease = *latest.Draft, *latest.Prerelease
	merged := make([]binaryRelease, 0, len(releases)+1)
	seen := make(map[string]bool, len(releases)+1)
	for _, release := range releases {
		if release.Tag != stable.Tag && !seen[release.Tag] {
			merged = append(merged, release)
			seen[release.Tag] = true
		}
	}
	// Prefer the dedicated endpoint's copy over a stale list entry with the
	// same tag. All candidates still use the normal version/run/channel order.
	return append(merged, stable), nil
}

func (u *binaryUpdater) check(ctx context.Context) (binaryPlan, error) {
	var plan binaryPlan
	plan.Status.Channel = readUpdateChannel(u.stateDir, u.module)
	current, hash, err := u.current(ctx)
	if err != nil {
		return plan, err
	}
	plan.CurrentHash = hash
	plan.Status.CurrentVersion = current.TailscaleVersion
	if u.arch != "arm64" && u.arch != "arm" && u.arch != "amd64" {
		plan.Status.Reason = "unsupported-architecture"
		return plan, nil
	}
	if current.Architecture != u.arch || current.Compatibility != binaryCompatibility {
		plan.Status.Reason = "module-upgrade-required"
		return plan, nil
	}
	releases, err := u.releaseCandidates(ctx)
	if err != nil {
		return plan, err
	}
	plan.Status.Checked = true
	release, found := selectBinaryRelease(releases, plan.Status.Channel)
	if !found {
		plan.Status.Reason = "no-compatible-release"
		return plan, nil
	}
	if !releaseTagPattern.MatchString(release.Tag) {
		return plan, errUpdateInvalid
	}
	var selected *releaseAsset
	for _, asset := range release.Assets {
		parts := binaryAssetPattern.FindStringSubmatch(asset.Name)
		if parts == nil || parts[3] != u.arch {
			continue
		}
		if selected != nil || !assetMatches(asset, release.Tag, maxManifestSize) {
			return plan, errUpdateInvalid
		}
		value := asset
		selected = &value
	}
	if selected == nil {
		plan.Status.Reason = "no-compatible-release"
		return plan, nil
	}
	data, err := u.fetch(ctx, selected.URL, maxManifestSize)
	if err != nil {
		return plan, err
	}
	if !verifyAsset(data, *selected) || decodeUpdateJSON(data, &plan.Manifest, true) != nil {
		return plan, errUpdateInvalid
	}
	m := plan.Manifest
	channel := "stable"
	if release.Prerelease {
		channel = "prerelease"
	}
	if m.ReleaseChannel != channel {
		return plan, errUpdateInvalid
	}
	parts := binaryAssetPattern.FindStringSubmatch(selected.Name)
	tagParts := releaseTagPattern.FindStringSubmatch(release.Tag)
	assetRevision, _ := strconv.ParseUint(parts[2], 10, 64)
	tagRevision, _ := strconv.ParseUint(tagParts[3], 10, 64)
	if m.TailscaleVersion != tagParts[1] || assetRevision != tagRevision {
		return plan, errUpdateInvalid
	}
	if m.Schema != 1 || m.Architecture != u.arch || m.TailscaleVersion != parts[1] || !commitPattern.MatchString(m.SourceCommit) || !digestPattern.MatchString(m.BinarySHA256) || !digestPattern.MatchString(m.PackageSHA256) || m.PackageName != strings.TrimSuffix(selected.Name, ".binary.json")+".zip" {
		return plan, errUpdateInvalid
	}
	plan.Status.LatestVersion = m.TailscaleVersion
	if m.Compatibility != binaryCompatibility {
		plan.Status.Reason = "module-upgrade-required"
		return plan, nil
	}
	matches := 0
	for _, asset := range release.Assets {
		if asset.Name == m.PackageName {
			if !assetMatches(asset, release.Tag, maxPackageSize) || asset.Digest != "sha256:"+m.PackageSHA256 {
				return plan, errUpdateInvalid
			}
			plan.Package = asset
			matches++
		}
	}
	if matches != 1 {
		return plan, errUpdateInvalid
	}
	plan.Status.Supported = true
	comparison, err := compareVersions(m.TailscaleVersion, current.TailscaleVersion)
	if err != nil {
		return plan, err
	}
	if comparison < 0 {
		plan.Status.Reason = "downgrade-blocked"
		return plan, nil
	}
	plan.Status.UpdateAvailable = hash != m.BinarySHA256
	return plan, nil
}

func (u *binaryUpdater) lock() (*os.File, error) {
	dir := filepath.Join(u.stateDir, "run")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, errUpdateInstall
	}
	file, err := os.OpenFile(filepath.Join(dir, "binary-update.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, errUpdateInstall
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, errUpdateBusy
	}
	return file, nil
}

func unlockUpdate(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}

// Share the runtime's mkdir mutex, not a second unrelated service lock.
// The child ctl verifies this owner against its actual parent and executable.
type binaryControlLock struct{ directory, owner, record string }

func processStartTime(pid string) (string, error) {
	if n, err := strconv.Atoi(pid); err != nil || n <= 1 {
		return "", errUpdateBusy
	}
	data, err := os.ReadFile("/proc/" + pid + "/stat")
	if err != nil {
		return "", err
	}
	end := bytes.LastIndex(data, []byte(") "))
	if end < 0 {
		return "", errUpdateBusy
	}
	fields := strings.Fields(string(data[end+2:]))
	if len(fields) <= 19 {
		return "", errUpdateBusy
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "", errUpdateBusy
	}
	return fields[19], nil
}

func staleControlOwner(path, name string) bool {
	pieces := strings.Split(name, ".")
	if len(pieces) != 2 {
		return false
	}
	for _, value := range pieces {
		if _, err := strconv.ParseUint(value, 10, 64); err != nil {
			return false
		}
	}
	data, err := readSmallFile(path, 128)
	if err != nil || string(data) != pieces[0]+" "+pieces[1]+"\n" {
		return false
	}
	start, err := processStartTime(pieces[0])
	return os.IsNotExist(err) || (err == nil && start != pieces[1])
}

func (u *binaryUpdater) acquireControlLock(ctx context.Context) (*binaryControlLock, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	directory := filepath.Join(u.stateDir, "run/control.lock")
	if err := os.MkdirAll(filepath.Dir(directory), 0700); err != nil {
		return nil, errUpdateInstall
	}
	pid := strconv.Itoa(os.Getpid())
	start, err := processStartTime(pid)
	if err != nil {
		return nil, errUpdateInstall
	}
	lock := &binaryControlLock{directory: directory, owner: pid + "." + start, record: pid + " " + start + "\n"}
	for {
		if err := os.Mkdir(directory, 0700); err == nil {
			file, err := os.OpenFile(filepath.Join(directory, lock.owner), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				_ = os.Remove(directory)
				return nil, errUpdateInstall
			}
			_, writeErr := file.WriteString(lock.record)
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				lock.release()
				return nil, errUpdateInstall
			}
			return lock, nil
		} else if !os.IsExist(err) {
			return nil, errUpdateInstall
		}
		stat, err := os.Lstat(directory)
		if err != nil || !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 {
			return nil, errUpdateBusy
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			return nil, errUpdateBusy
		}
		removed := false
		for _, entry := range entries {
			path := filepath.Join(directory, entry.Name())
			if entry.Type().IsRegular() && staleControlOwner(path, entry.Name()) {
				if os.Remove(path) == nil {
					removed = true
				}
			}
		}
		if removed && os.Remove(directory) == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return nil, errUpdateBusy
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (lock *binaryControlLock) release() {
	path := filepath.Join(lock.directory, lock.owner)
	data, err := readSmallFile(path, 128)
	if err != nil || string(data) != lock.record {
		return
	}
	if os.Remove(path) == nil {
		_ = os.Remove(lock.directory)
	}
}

func validateAndroidELF(path, arch string) error {
	stat, err := os.Lstat(path)
	if err != nil || !stat.Mode().IsRegular() || stat.Size() <= 0 || stat.Size() > maxPackageSize {
		return errUpdateInvalid
	}
	file, err := elf.Open(path)
	if err != nil {
		return errUpdateInvalid
	}
	defer file.Close()
	machine := map[string]elf.Machine{"arm64": elf.EM_AARCH64, "arm": elf.EM_ARM, "amd64": elf.EM_X86_64}[arch]
	class, linker := elf.ELFCLASS64, "/system/bin/linker64\x00"
	if arch == "arm" {
		class, linker = elf.ELFCLASS32, "/system/bin/linker\x00"
	}
	if machine == elf.EM_NONE || file.Type != elf.ET_DYN || file.Machine != machine || file.Class != class || file.Data != elf.ELFDATA2LSB || file.Entry == 0 {
		return errUpdateInvalid
	}
	interpreter, loads := 0, 0
	for _, program := range file.Progs {
		if program.Type == elf.PT_INTERP {
			data, err := readBounded(program.Open(), 128)
			if err != nil || string(data) != linker {
				return errUpdateInvalid
			}
			interpreter++
		}
		if program.Type == elf.PT_LOAD {
			if program.Align < 16384 || program.Align&(program.Align-1) != 0 || program.Off%program.Align != program.Vaddr%program.Align {
				return errUpdateInvalid
			}
			loads++
		}
	}
	if interpreter != 1 || loads == 0 {
		return errUpdateInvalid
	}
	return nil
}

func readBinaryVersion(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, _, truncated, err := run(ctx, path, "--version")
	if err != nil || truncated {
		return "", errUpdateInvalid
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "", errUpdateInvalid
	}
	version, _, _ := strings.Cut(fields[0], "-")
	if !stableVersionPattern.MatchString(version) {
		return "", errUpdateInvalid
	}
	return version, nil
}

func checkBinaryVersion(ctx context.Context, path string, metadata binaryMetadata) error {
	if !commitPattern.MatchString(metadata.SourceCommit) || !stableVersionPattern.MatchString(metadata.TailscaleVersion) {
		return errUpdateInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, _, truncated, err := run(ctx, path, "--version")
	if err != nil || truncated {
		return errUpdateInvalid
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != metadata.TailscaleVersion {
		return errUpdateInvalid
	}
	expected := metadata.TailscaleVersion + "-shellscaled-" + metadata.SourceCommit[:12]
	seen := make(map[string]bool)
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !found || value == "" || seen[key] {
			return errUpdateInvalid
		}
		seen[key] = true
		switch key {
		case "tailscale commit":
			if value != metadata.SourceCommit {
				return errUpdateInvalid
			}
		case "long version":
			if value != expected {
				return errUpdateInvalid
			}
		case "go version", "track", "other commit":
			// Upstream diagnostics are not identity bindings. Still reject
			// duplicate labels rather than accepting contradictory output.
		default:
			return errUpdateInvalid
		}
	}
	if !seen["tailscale commit"] || !seen["long version"] {
		return errUpdateInvalid
	}
	return nil
}

func (u *binaryUpdater) download(ctx context.Context, asset releaseAsset, path string) error {
	if !allowedUpdateURL(asset.URL) {
		return errUpdateInvalid
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return errUpdateInvalid
	}
	req.Header.Set("User-Agent", "Shellscaled-binary-updater")
	res, err := u.client.Do(req)
	if err != nil {
		return errUpdateCheck
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || res.ContentLength > maxPackageSize {
		return errUpdateCheck
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errUpdateInstall
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(file, h), io.LimitReader(res.Body, maxPackageSize+1))
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || n != asset.Size || n > maxPackageSize || "sha256:"+hex.EncodeToString(h.Sum(nil)) != asset.Digest {
		return errUpdateInvalid
	}
	return nil
}

func extractBinary(path, candidate string, m binaryManifest) (binaryMetadata, error) {
	var info binaryMetadata
	archive, err := zip.OpenReader(path)
	if err != nil {
		return info, errUpdateInvalid
	}
	defer archive.Close()
	if len(archive.File) > 256 {
		return info, errUpdateInvalid
	}
	binaryCount, infoCount := 0, 0
	for _, entry := range archive.File {
		if entry.Name != "bin/tailscaled" && entry.Name != "build-info.json" {
			continue
		}
		if !entry.Mode().IsRegular() {
			return info, errUpdateInvalid
		}
		limit := int64(maxManifestSize)
		if entry.Name == "bin/tailscaled" {
			limit = maxPackageSize
			binaryCount++
		} else {
			infoCount++
		}
		if binaryCount > 1 || infoCount > 1 || entry.UncompressedSize64 == 0 || entry.UncompressedSize64 > uint64(limit) {
			return info, errUpdateInvalid
		}
		reader, err := entry.Open()
		if err != nil {
			return info, errUpdateInvalid
		}
		if entry.Name == "build-info.json" {
			data, readErr := readBounded(reader, limit)
			closeErr := reader.Close()
			if readErr != nil || closeErr != nil || decodeUpdateJSON(data, &info, false) != nil {
				return info, errUpdateInvalid
			}
		} else {
			file, openErr := os.OpenFile(candidate, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
			if openErr != nil {
				reader.Close()
				return info, errUpdateInstall
			}
			n, copyErr := io.Copy(file, io.LimitReader(reader, limit+1))
			readCloseErr := reader.Close()
			syncErr := file.Sync()
			closeErr := file.Close()
			if copyErr != nil || readCloseErr != nil || syncErr != nil || closeErr != nil || n <= 0 || n > limit {
				return info, errUpdateInvalid
			}
		}
	}
	if binaryCount != 1 || infoCount != 1 || info.TailscaleVersion != m.TailscaleVersion || info.SourceCommit != m.SourceCommit || info.Architecture != m.Architecture || info.Compatibility != m.Compatibility || info.BinarySHA256 != m.BinarySHA256 || info.ReleaseChannel != m.ReleaseChannel {
		return info, errUpdateInvalid
	}
	hash, err := hashFile(candidate)
	if err != nil || hash != m.BinarySHA256 {
		return info, errUpdateInvalid
	}
	if validateAndroidELF(candidate, m.Architecture) != nil {
		return info, errUpdateInvalid
	}
	return info, nil
}

func syncDirectory(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func atomicMetadata(path string, data []byte) error {
	temporary := path + ".update"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer os.Remove(temporary)
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return errUpdateInstall
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func (u *binaryUpdater) update(ctx context.Context) (binaryPlan, error) {
	ctx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	reserve := 70 * time.Second
	if remaining := time.Until(deadline); remaining < reserve+10*time.Second {
		reserve = remaining / 3
	}
	work, cancelWork := context.WithDeadline(ctx, deadline.Add(-reserve))
	defer cancelWork()
	var plan binaryPlan
	bin := filepath.Join(u.module, "bin")
	candidate, previous, archive := filepath.Join(bin, ".tailscaled-update.candidate"), filepath.Join(bin, ".tailscaled-update.previous"), filepath.Join(bin, ".tailscaled-update.zip")
	// With the global flock held, orphaned downloads are safe to discard. An
	// old inode is a recovery point and must never be silently overwritten.
	if _, err := os.Lstat(previous); !os.IsNotExist(err) {
		return plan, errUpdateRollback
	}
	for _, path := range []string{candidate, archive} {
		if stat, err := os.Lstat(path); err == nil {
			if !stat.Mode().IsRegular() || os.Remove(path) != nil {
				return plan, errUpdateInstall
			}
		} else if !os.IsNotExist(err) {
			return plan, errUpdateInstall
		}
	}
	plan, err := u.check(work)
	if err != nil || !plan.Status.Supported || !plan.Status.UpdateAvailable {
		return plan, err
	}
	defer os.Remove(candidate)
	defer os.Remove(archive)
	downloadCtx, cancelDownload := context.WithTimeout(work, 80*time.Second)
	err = u.download(downloadCtx, plan.Package, archive)
	cancelDownload()
	if err != nil {
		return plan, err
	}
	info, err := extractBinary(archive, candidate, plan.Manifest)
	if err != nil {
		return plan, err
	}
	if err := u.version(work, candidate, info); err != nil {
		return plan, errUpdateInvalid
	}
	if err := os.Chmod(candidate, 0755); err != nil {
		return plan, errUpdateInstall
	}
	control, err := u.acquireControlLock(work)
	if err != nil {
		return plan, err
	}
	defer control.release()
	u.controlOwner = control.owner
	defer func() { u.controlOwner = "" }()
	core := filepath.Join(bin, "tailscaled")
	hash, err := hashFile(core)
	if err != nil || hash != plan.CurrentHash {
		return plan, errUpdateInstall
	}
	wasRunning, err := u.running(work)
	if err != nil {
		return plan, errUpdateInstall
	}
	metadataPath := filepath.Join(u.module, "runtime-binary.json")
	oldMetadata, metadataErr := readSmallFile(metadataPath, maxManifestSize)
	if metadataErr != nil && !os.IsNotExist(metadataErr) {
		return plan, errUpdateInstall
	}
	// A hard link keeps exactly one old inode on the same filesystem. Stop must
	// precede replacement because ctl.sh verifies /proc/PID/exe ownership.
	if err := os.Link(core, previous); err != nil {
		return plan, errUpdateInstall
	}
	keepPrevious := false
	defer func() {
		if !keepPrevious {
			_ = os.Remove(previous)
		}
	}()
	stopped, replaced := false, false
	restore := func() error {
		if replaced {
			if err := u.control(ctx, "stop"); err != nil {
				keepPrevious = true
				return errUpdateRollback
			}
			if err := os.Rename(previous, core); err != nil {
				keepPrevious = true
				return errUpdateRollback
			}
			_ = syncDirectory(bin)
		}
		if metadataErr == nil {
			_ = atomicMetadata(metadataPath, oldMetadata)
		} else {
			_ = os.Remove(metadataPath)
		}
		if wasRunning && stopped {
			if err := u.control(ctx, "start"); err != nil {
				return errUpdateInstall
			}
		}
		return errUpdateInstall
	}
	stopped = true
	if err := u.control(work, "stop"); err != nil {
		return plan, restore()
	}
	if err := os.Rename(candidate, core); err != nil {
		return plan, restore()
	}
	replaced = true
	if err := syncDirectory(bin); err != nil {
		return plan, restore()
	}
	if wasRunning {
		if err := u.control(work, "start"); err != nil {
			return plan, restore()
		}
		running, err := u.running(work)
		if err != nil || !running {
			return plan, restore()
		}
	}
	metadata, _ := json.Marshal(info)
	if err := atomicMetadata(metadataPath, append(metadata, '\n')); err != nil {
		return plan, restore()
	}
	plan.Status.CurrentVersion = info.TailscaleVersion
	plan.Status.UpdateAvailable = false
	return plan, nil
}

func (b *backend) binary(ctx context.Context, update bool) response {
	updater := newBinaryUpdater(b)
	defer updater.client.CloseIdleConnections()
	lock, err := updater.lock()
	if err != nil {
		return failed(err.Error())
	}
	defer unlockUpdate(lock)
	var plan binaryPlan
	if update {
		plan, err = updater.update(ctx)
	} else {
		plan, err = updater.check(ctx)
	}
	r := response{OK: err == nil, Binary: &plan.Status}
	if err != nil {
		r.Error = err.Error()
		r.ExitCode = 1
	}
	return r
}

func runtimeBinaryVersion(module string) string {
	data, err := readSmallFile(filepath.Join(module, "runtime-binary.json"), maxManifestSize)
	if err != nil {
		return ""
	}
	var metadata binaryMetadata
	if decodeUpdateJSON(data, &metadata, true) != nil || !stableVersionPattern.MatchString(metadata.TailscaleVersion) {
		return ""
	}
	hash, err := hashFile(filepath.Join(module, "bin/tailscaled"))
	if err != nil || hash != metadata.BinarySHA256 {
		return ""
	}
	return metadata.TailscaleVersion
}
