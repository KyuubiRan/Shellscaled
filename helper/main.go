// Copyright (c) 2026 KyuubiRan
// SPDX-License-Identifier: BSD-3-Clause
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const dataDir = "/data/adb/shellscaled"
const socketPath = dataDir + "/run/tailscaled.sock"

const taildropPathFile = dataDir + "/taildrop-path"
const taildropStagingDir = dataDir + "/taildrop-staging"

const defaultTaildropPath = "/storage/emulated/0/Download/Tailscale"
const mediaStoragePath = "/data/media/0"

const sharedStorageRoot = "/storage/emulated/0"

const taildropBrowseLimit = 128

func validTaildropBrowsePath(value string) bool {
	value, ok := normalizeTaildropPath(value)
	if !ok {
		return false
	}
	if value == sharedStorageRoot {
		return true
	}
	return validTaildropPath(value)
}

func normalizeTaildropPath(value string) (string, bool) {
	if value == "" || len(value) > 512 || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return "", false
	}
	if value == "/sdcard" {
		return "/storage/emulated/0", true
	}
	if strings.HasPrefix(value, "/sdcard/") {
		return "/storage/emulated/0" + strings.TrimPrefix(value, "/sdcard"), true
	}
	return value, true
}

func taildropDirectories(value string) response {
	if value == "" {
		value = "/storage/emulated/0"
	}
	canonical, ok := normalizeTaildropPath(value)
	if !ok || !validTaildropBrowsePath(canonical) {
		return failed("浏览目录无效")
	}
	value = canonical
	info, err := os.Lstat(value)
	if err != nil || !info.IsDir() {
		return failed("浏览目录不存在或不可用")
	}
	resolved, err := filepath.EvalSymlinks(value)
	if err != nil || resolved != value {
		return failed("浏览目录不能是符号链接")
	}
	directory, err := os.Open(value)
	if err != nil {
		return failed("无法读取目录")
	}
	defer directory.Close()
	entries, err := directory.Readdir(taildropBrowseLimit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return failed("无法读取目录")
	}
	truncated := len(entries) > taildropBrowseLimit
	if truncated {
		entries = entries[:taildropBrowseLimit]
	}
	result := make([]taildropDirectory, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || entry.Mode()&os.ModeSymlink != 0 {
			continue
		}
		path := filepath.Join(value, entry.Name())
		if !validTaildropPath(path) {
			continue
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || !validTaildropPath(resolved) || resolved != path {
			continue
		}
		result = append(result, taildropDirectory{Name: boundedText(entry.Name(), 128), Path: path})
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name) })
	return response{OK: true, TaildropPath: value, TaildropDirectories: result, TaildropDirectoriesTruncated: truncated}
}

func validTaildropPath(value string) bool {
	canonical, ok := normalizeTaildropPath(value)
	if !ok {
		return false
	}
	if !strings.HasPrefix(canonical, sharedStorageRoot+string(os.PathSeparator)) {
		return false
	}
	relative := strings.TrimPrefix(canonical, sharedStorageRoot+string(os.PathSeparator))
	for _, privateRoot := range []string{"Android", "Android/data", "Android/obb", "Android/sandbox"} {
		if relative == privateRoot || strings.HasPrefix(relative, privateRoot+string(os.PathSeparator)) {
			return false
		}
	}
	return true
}

func readTaildropPath() string {
	// This is shared Android storage. The module staging directory remains
	// private; the user-facing directory needs ordinary media-readable mode.
	_ = os.MkdirAll(defaultTaildropPath, 0775)
	data, err := os.ReadFile(taildropPathFile)
	if err != nil {
		return defaultTaildropPath
	}
	value := strings.TrimSpace(string(data))
	if validTaildropPath(value) {
		return value
	}
	return defaultTaildropPath
}

func saveTaildropPath(value string) error {
	canonical, ok := normalizeTaildropPath(value)
	if !ok || !validTaildropPath(canonical) {
		return errors.New("接收目录必须位于 Android 共享存储，且不能位于 Android/data、Android/obb 或 Android/sandbox")
	}
	if info, err := os.Lstat(canonical); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("接收目录不存在或不可用")
	}
	if resolved, err := filepath.EvalSymlinks(canonical); err != nil || resolved != canonical {
		return errors.New("接收目录不能通过符号链接指向其他位置")
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return errors.New("无法保存接收目录")
	}
	tmp := taildropPathFile + ".new"
	if err := os.WriteFile(tmp, []byte(canonical+"\n"), 0600); err != nil {
		return errors.New("无法保存接收目录")
	}
	if err := os.Rename(tmp, taildropPathFile); err != nil {
		_ = os.Remove(tmp)
		return errors.New("无法保存接收目录")
	}
	return nil
}

type settings struct {
	AcceptRoutes           bool `json:"acceptRoutes"`
	ShieldsUp              bool `json:"shieldsUp"`
	ExitNodeAllowLANAccess bool `json:"exitNodeAllowLanAccess"`
}

type settingsPatch struct {
	AcceptRoutes           *bool `json:"acceptRoutes"`
	ShieldsUp              *bool `json:"shieldsUp"`
	ExitNodeAllowLANAccess *bool `json:"exitNodeAllowLanAccess"`
}

type request struct {
	Action   string `json:"action"`
	Channel  string `json:"channel,omitempty"`
	Locale   string `json:"locale,omitempty"`
	Command  string `json:"command,omitempty"`
	Path     string `json:"path,omitempty"`
	ExitNode string `json:"exitNode,omitempty"`
	// Routes are accepted as a comma separated list and parsed at the RPC boundary.
	AdvertiseRoutes   *string        `json:"advertiseRoutes,omitempty"`
	AdvertiseExitNode *bool          `json:"advertiseExitNode,omitempty"`
	Settings          *settingsPatch `json:"settings,omitempty"`
}

type response struct {
	OK                           bool                `json:"ok"`
	State                        string              `json:"state,omitempty"`
	Version                      string              `json:"version,omitempty"`
	IPs                          []string            `json:"ips,omitempty"`
	Settings                     settings            `json:"settings"`
	DaemonRunning                bool                `json:"daemonRunning"`
	ExitNodeConfigured           bool                `json:"exitNodeConfigured"`
	ExitNodeOnline               *bool               `json:"exitNodeOnline,omitempty"`
	ExitNodeIPv6                 *bool               `json:"exitNodeIPv6,omitempty"`
	LoginURL                     string              `json:"loginURL,omitempty"`
	Error                        string              `json:"error,omitempty"`
	ExitCode                     int                 `json:"exitCode"`
	Stdout                       string              `json:"stdout,omitempty"`
	Stderr                       string              `json:"stderr,omitempty"`
	Truncated                    bool                `json:"truncated,omitempty"`
	Devices                      *[]device           `json:"devices,omitempty"`
	ExitNodeTarget               string              `json:"exitNodeTarget,omitempty"`
	AdvertiseExitNode            bool                `json:"advertiseExitNode"`
	AdvertiseRoutes              []string            `json:"advertiseRoutes,omitempty"`
	Binary                       *binaryStatus       `json:"binary,omitempty"`
	UpdateChannel                string              `json:"updateChannel,omitempty"`
	TaildropPath                 string              `json:"taildropPath,omitempty"`
	TaildropReceived             bool                `json:"taildropReceived,omitempty"`
	TaildropDirectories          []taildropDirectory `json:"taildropDirectories,omitempty"`
	TaildropDirectoriesTruncated bool                `json:"taildropDirectoriesTruncated,omitempty"`
	MagicDNS                     *magicDNSState      `json:"magicDNS,omitempty"`
}

type taildropDirectory struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// Deliberately decode only UI fields. Never return prefs.Persist or account data.
type localStatus struct {
	BackendState   string
	Version        string
	TailscaleIPs   []string
	AuthURL        string
	ExitNodeStatus *struct {
		ID     string
		Online bool
	}
	controlURL string // Set only from the same LocalAPI preferences snapshot.
}

// Keep this projection separate from ordinary status polling, which omits peers.
// Peer map keys are public node keys; decode only the values and discard the keys.
type localDevices struct {
	Self *localDevice
	Peer localDeviceList
}

type localDeviceList []*localDevice

func (peers *localDeviceList) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		*peers = nil
		return nil
	}
	if token != json.Delim('{') {
		return errors.New("invalid peer list")
	}
	var result localDeviceList
	for decoder.More() {
		if _, err := decoder.Token(); err != nil { // Discard the public key.
			return err
		}
		var peer *localDevice
		if err := decoder.Decode(&peer); err != nil {
			return err
		}
		if peer != nil {
			result = append(result, peer)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	*peers = result
	return nil
}

type localDevice struct {
	ID             string
	HostName       string
	DNSName        string
	TailscaleIPs   []string
	Online         bool
	OS             string
	LastSeen       time.Time
	ExitNode       bool
	ExitNodeOption bool
}

type device struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	DNSName        string     `json:"dnsName"`
	IPs            []string   `json:"ips"`
	Online         bool       `json:"online"`
	OS             string     `json:"os"`
	Self           bool       `json:"self"`
	LastSeen       *time.Time `json:"lastSeen,omitempty"`
	ExitNode       bool       `json:"exitNode,omitempty"`
	ExitNodeOption bool       `json:"exitNodeOption,omitempty"`
}

type localPrefs struct {
	ControlURL             string
	RouteAll               bool
	ShieldsUp              bool
	ExitNodeAllowLANAccess bool
	ExitNodeID             string
	ExitNodeIP             string
	AdvertiseRoutes        []string
}

type backend struct {
	stateDir string
	module   string
	client   *http.Client
	locale   string
	// Internal browser test hook; RPC callers cannot configure it.
	loginBrowser func(context.Context, string) error
}

func newBackend(module string) *backend {
	return &backend{module: module, locale: savedLocale(), client: &http.Client{
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
			if address != "local-tailscaled.sock:80" {
				return nil, errors.New("unexpected LocalAPI address")
			}
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		}},
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

var errDaemonUnreachable = errors.New("后台服务未响应，请先启动或重启 tailscaled")

func (b *backend) api(ctx context.Context, method, path string, body any, result any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://local-tailscaled.sock/localapi/v0/"+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := b.client.Do(req)
	if err != nil {
		return errDaemonUnreachable
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return fmt.Errorf("LocalAPI %d: %s", res.StatusCode, strings.TrimSpace(string(msg)))
	}
	if result != nil {
		if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(result); err != nil {
			return errors.New("无法读取后台状态")
		}
	}
	return nil
}

func (b *backend) status(ctx context.Context) response {
	r, _ := b.statusWithLocal(ctx)
	return r
}

func (b *backend) statusWithLocal(ctx context.Context) (response, localStatus) {
	r := response{OK: true, State: "ServiceStopped", Version: b.moduleVersion(), IPs: []string{}, UpdateChannel: readUpdateChannel(b.updateDataDir(), b.module), TaildropPath: readTaildropPath(), MagicDNS: readMagicDNSState(magicDNSStatePath, time.Now())}
	var st localStatus
	if err := b.api(ctx, "GET", "status?peers=false", nil, &st); err != nil {
		if _, statErr := os.Stat(socketPath); statErr == nil {
			r.State = "ServiceError"
			r.Error = err.Error()
		}
		return r, st
	}
	r.DaemonRunning, r.State, r.IPs = true, st.BackendState, st.TailscaleIPs
	if st.Version != "" {
		r.Version = strings.Split(st.Version, "-shellscaled-")[0]
	}
	if u, err := url.Parse(st.AuthURL); err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil {
		r.LoginURL = st.AuthURL
	}
	var p localPrefs
	if err := b.api(ctx, "GET", "prefs", nil, &p); err != nil {
		r.OK = false
		r.Error = err.Error()
		return r, st
	}
	st.controlURL = p.ControlURL
	r.Settings = settings{p.RouteAll, p.ShieldsUp, p.ExitNodeAllowLANAccess}
	// The proxy refreshes every 5 s; until then its mode reflects older state.
	if m := r.MagicDNS; m != nil && m.Running && (m.Daemon != st.BackendState || (st.BackendState == "Running" && m.ExitNode != p.ExitNodeID)) {
		m.Pending = true
	}
	r.ExitNodeConfigured = p.ExitNodeID != "" || (p.ExitNodeIP != "" && p.ExitNodeIP != "invalid IP")
	if p.ExitNodeID != "" {
		r.ExitNodeTarget = boundedText(p.ExitNodeID, 128)
	} else if p.ExitNodeIP != "" && p.ExitNodeIP != "invalid IP" {
		r.ExitNodeTarget = boundedText(p.ExitNodeIP, 80)
	}
	if r.ExitNodeConfigured {
		if st.ExitNodeStatus != nil && st.ExitNodeStatus.ID == p.ExitNodeID {
			online := st.ExitNodeStatus.Online
			r.ExitNodeOnline = &online
		} else if p.ExitNodeID != "" && st.BackendState == "Running" {
			// The selected node is missing from the netmap; tailscaled blackholes traffic.
			online := false
			r.ExitNodeOnline = &online
		}
		r.ExitNodeIPv6 = tunnelIPv6Default()
	}
	for _, route := range p.AdvertiseRoutes {
		if route == "0.0.0.0/0" || route == "::/0" {
			r.AdvertiseExitNode = true
			continue
		}
		if route != "" {
			r.AdvertiseRoutes = append(r.AdvertiseRoutes, boundedText(route, 80))
		}
	}
	return r, st
}

func (b *backend) devices(ctx context.Context) response {
	return b.deviceList(ctx, false)
}

func (b *backend) deviceList(ctx context.Context, exitsOnly bool) response {
	var status localDevices
	if err := b.api(ctx, "GET", "status?peers=true", nil, &status); err != nil {
		return failed(err.Error())
	}
	devices := make([]device, 0, len(status.Peer)+1)
	add := func(peer *localDevice, self bool) {
		if peer == nil || (exitsOnly && (self || !peer.ExitNodeOption)) {
			return
		}
		dnsName := boundedText(strings.TrimRight(peer.DNSName, "."), 253)
		name := boundedText(peer.HostName, 128)
		if dnsName != "" {
			name, _, _ = strings.Cut(dnsName, ".")
			name = boundedText(name, 128)
		}
		ips := []string{}
		seen := map[netip.Addr]bool{}
		for _, value := range peer.TailscaleIPs {
			if len(value) > 45 || len(ips) == 4 {
				continue
			}
			ip, err := netip.ParseAddr(value)
			if err != nil || ip.Zone() != "" {
				continue
			}
			ip = ip.Unmap()
			if !seen[ip] {
				seen[ip] = true
				ips = append(ips, ip.String())
			}
		}
		entry := device{ID: boundedText(peer.ID, 128), Name: name, DNSName: dnsName, IPs: ips, Online: peer.Online, OS: boundedText(peer.OS, 32), Self: self, ExitNode: peer.ExitNode, ExitNodeOption: peer.ExitNodeOption}
		if !peer.Online && !peer.LastSeen.IsZero() {
			lastSeen := peer.LastSeen
			entry.LastSeen = &lastSeen
		}
		devices = append(devices, entry)
	}
	add(status.Self, true)
	for _, peer := range status.Peer {
		// The current node normally appears only in Self. Do not duplicate it.
		if status.Self != nil && status.Self.ID != "" && peer.ID == status.Self.ID {
			continue
		}
		add(peer, false)
	}
	sort.Slice(devices, func(i, j int) bool {
		a, b := devices[i], devices[j]
		if a.Self != b.Self {
			return a.Self
		}
		if a.Online != b.Online {
			return a.Online
		}
		if x, y := strings.ToLower(a.Name), strings.ToLower(b.Name); x != y {
			return x < y
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.DNSName != b.DNSName {
			return a.DNSName < b.DNSName
		}
		return a.ID < b.ID
	})
	const limit = 1000
	truncated := len(devices) > limit
	if truncated {
		devices = devices[:limit]
	}
	// Count actual JSON bytes, including escapes, rather than assuming that
	// bounded strings imply bounded encoded data. Reserve ample room below
	// the WebUI's 1.5 MB response limit for the response envelope.
	const payloadLimit = 1 << 20
	size := 2 // Array brackets.
	for i, entry := range devices {
		encoded, err := json.Marshal(entry)
		if err != nil {
			return failed("无法读取后台状态")
		}
		if size+len(encoded)+1 > payloadLimit {
			devices, truncated = devices[:i], true
			break
		}
		size += len(encoded) + 1
	}
	return response{OK: true, Devices: &devices, Truncated: truncated}
}

func boundedText(value string, limit int) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	if len(value) <= limit {
		return value
	}
	for !utf8.ValidString(value[:limit]) {
		limit--
	}
	return value[:limit]
}

func consoleCommand(ctx context.Context) *exec.Cmd {
	// No caller-supplied URL, component, package, user, or command arguments.
	cmd := exec.CommandContext(ctx, "/system/bin/am", "start", "-W", "--user", "current",
		"-a", "android.intent.action.VIEW", "-c", "android.intent.category.BROWSABLE",
		"-d", "https://console.tailscale.com/admin/machines")
	cmd.WaitDelay = time.Second
	return cmd
}

func (b *backend) console(ctx context.Context) response {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Discard ActivityManager output: it is neither shell output nor UI data.
	if err := consoleCommand(ctx).Run(); err != nil {
		return failed("无法打开控制台，请安装或启用浏览器后重试")
	}
	return response{OK: true}
}

func aboutCommand(ctx context.Context) *exec.Cmd {
	// The project link is code-owned; RPC data never selects the destination.
	cmd := exec.CommandContext(ctx, "/system/bin/am", "start", "-W", "--user", "current",
		"-a", "android.intent.action.VIEW", "-c", "android.intent.category.BROWSABLE",
		"-d", "https://github.com/KyuubiRan/Shellscaled")
	cmd.WaitDelay = time.Second
	return cmd
}

func (b *backend) about(ctx context.Context) response {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// ActivityManager output is intentionally discarded, as for the console.
	if err := aboutCommand(ctx).Run(); err != nil {
		return failed("无法打开项目主页，请安装或启用浏览器后重试")
	}
	return response{OK: true}
}

var loginTokenPath = regexp.MustCompile(`^/a/[A-Za-z0-9_-]{1,512}$`)

func allowedLoginURL(value, controlURL string) bool {
	if value == "" || len(value) > 2048 || strings.TrimSpace(value) != value || strings.Contains(value, "#") {
		return false
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" ||
		u.Fragment != "" || u.RawQuery != "" || u.ForceQuery || u.RawPath != "" ||
		(u.Port() != "" && u.Port() != "443") || !loginTokenPath.MatchString(u.Path) {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if !strings.EqualFold(u.Host, host) && !strings.EqualFold(u.Host, host+":443") {
		return false
	}
	switch host {
	case "login.tailscale.com":
		// The documented SaaS interactive-login destination.
		return true
	case "controlplane.tailscale.com":
		// Upstream TestValidPopBrowserURL also supports this same-origin SaaS
		// form. An empty ControlURL selects the official default control plane.
		if controlURL == "" {
			return true
		}
		control, err := url.Parse(controlURL)
		return err == nil && control.Scheme == "https" && control.User == nil &&
			(strings.EqualFold(control.Host, "controlplane.tailscale.com") || strings.EqualFold(control.Host, "controlplane.tailscale.com:443")) &&
			(control.Path == "" || control.Path == "/") && control.RawQuery == "" &&
			!control.ForceQuery && control.Fragment == "" && control.Opaque == "" && control.RawPath == ""
	default:
		return false
	}
}

func loginCommand(ctx context.Context, target, controlURL string) (*exec.Cmd, error) {
	if !allowedLoginURL(target, controlURL) {
		return nil, errors.New("登录链接无效，请刷新后重试")
	}
	cmd := exec.CommandContext(ctx, "/system/bin/am", "start", "-W", "--user", "current",
		"-a", "android.intent.action.VIEW", "-c", "android.intent.category.BROWSABLE", "-d", target)
	cmd.WaitDelay = time.Second
	return cmd, nil
}

func (b *backend) login(ctx context.Context) response {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	r, local := b.statusWithLocal(ctx)
	if !r.OK {
		return r
	}
	// A stale login button must never switch accounts or restart authorization.
	switch r.State {
	case "Running", "Stopped", "Starting", "NeedsMachineAuth":
		return r
	}
	if !r.DaemonRunning {
		return failed("请先启动后台服务")
	}
	if r.State != "NeedsLogin" {
		return failed("登录链接尚未准备好，请稍后重试")
	}
	if local.AuthURL == "" {
		// The ordinary up path changes only WantRunning, then requests an
		// interactive URL for an unauthenticated backend and polls briefly.
		r = b.connection(ctx, true)
		if !r.OK {
			return r
		}
		r, local = b.statusWithLocal(ctx)
		if !r.OK {
			return r
		}
		if r.State == "Running" || r.State == "NeedsMachineAuth" {
			return r
		}
		if r.State != "NeedsLogin" || local.AuthURL == "" {
			return failed("登录链接尚未准备好，请稍后重试")
		}
	}
	if !allowedLoginURL(local.AuthURL, local.controlURL) {
		return failed("登录链接无效，请刷新后重试")
	}
	openCtx, cancelOpen := context.WithTimeout(ctx, 10*time.Second)
	defer cancelOpen()
	if b.loginBrowser != nil {
		if err := b.loginBrowser(openCtx, local.AuthURL); err != nil {
			return failed("无法打开登录页面，请安装或启用浏览器后重试")
		}
	} else {
		cmd, err := loginCommand(openCtx, local.AuthURL, local.controlURL)
		// ActivityManager output may contain a private login token. Discard it.
		if err != nil || cmd.Run() != nil {
			return failed("无法打开登录页面，请安装或启用浏览器后重试")
		}
	}
	return r
}

func (b *backend) moduleVersion() string {
	if version := runtimeBinaryVersion(b.module); version != "" {
		return version
	}
	data, _ := os.ReadFile(filepath.Join(b.module, "module.prop"))
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "version="); ok {
			return v
		}
	}
	return ""
}

func (b *backend) connection(ctx context.Context, up bool) response {
	if up {
		if _, err := os.Stat(filepath.Join(b.module, "disable")); err == nil {
			return failed("模块已停用")
		}
		if _, err := os.Stat(filepath.Join(b.module, "remove")); err == nil {
			return failed("模块正在等待移除")
		}
		if !b.status(ctx).DaemonRunning {
			_, stderr, _, err := run(ctx, filepath.Join(b.module, "ctl.sh"), "start")
			if err != nil {
				return failed("后台服务启动失败：" + stderr)
			}
		}
	} else if !b.status(ctx).DaemonRunning {
		return b.status(ctx)
	}
	// A masked edit preserves every unrelated preference; bare CLI up can reset them.
	if err := b.api(ctx, "PATCH", "prefs", map[string]any{"WantRunning": up, "WantRunningSet": true}, nil); err != nil {
		return failed(err.Error())
	}
	r := b.status(ctx)
	if up && r.State == "NeedsLogin" {
		if err := b.api(ctx, "POST", "login-interactive", nil, nil); err != nil {
			return failed(err.Error())
		}
	}
	for i := 0; i < 16; i++ {
		r = b.status(ctx)
		if (up && (r.State == "Running" || r.LoginURL != "" || r.State == "NeedsMachineAuth")) || (!up && r.State != "Running" && r.State != "Starting") {
			break
		}
		select {
		case <-ctx.Done():
			return failed("操作超时，请刷新状态后再确认")
		case <-time.After(500 * time.Millisecond):
		}
	}
	nudgeMagicDNS()
	return r
}

func (b *backend) setSettings(ctx context.Context, s *settingsPatch) response {
	if s == nil || (s.AcceptRoutes == nil && s.ShieldsUp == nil && s.ExitNodeAllowLANAccess == nil) {
		return failed("设置不能为空")
	}
	current := b.status(ctx)
	if !current.DaemonRunning {
		return failed("请先启动后台服务")
	}
	if s.ExitNodeAllowLANAccess != nil && *s.ExitNodeAllowLANAccess && !current.ExitNodeConfigured {
		return failed("请先通过 tailscale set --exit-node=目标 选择出口节点")
	}
	p := map[string]any{}
	if s.AcceptRoutes != nil {
		p["RouteAll"], p["RouteAllSet"] = *s.AcceptRoutes, true
	}
	if s.ShieldsUp != nil {
		p["ShieldsUp"], p["ShieldsUpSet"] = *s.ShieldsUp, true
	}
	if s.ExitNodeAllowLANAccess != nil {
		p["ExitNodeAllowLANAccess"], p["ExitNodeAllowLANAccessSet"] = *s.ExitNodeAllowLANAccess, true
	}
	if err := b.api(ctx, "PATCH", "prefs", p, nil); err != nil {
		return failed(err.Error())
	}
	return b.status(ctx)
}

func (b *backend) taildropPath(ctx context.Context, value string) response {
	if value == "" {
		_ = os.MkdirAll(defaultTaildropPath, 0775)
		return response{OK: true, TaildropPath: readTaildropPath()}
	}
	if err := saveTaildropPath(value); err != nil {
		return failed(err.Error())
	}
	return response{OK: true, TaildropPath: value}
}

func (b *backend) taildropReceive(ctx context.Context, value string) response {
	lock, err := acquireTaildropLock()
	if err != nil {
		return failed(err.Error())
	}
	defer func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}()
	if value == "" {
		value = readTaildropPath()
		if value == defaultTaildropPath {
			_ = os.MkdirAll(value, 0775)
		}
	} else if err := saveTaildropPath(value); err != nil {
		return failed(err.Error())
	}
	if !validTaildropPath(value) {
		return failed("接收目录无效")
	}
	info, err := os.Stat(value)
	if err != nil || !info.IsDir() {
		return failed("接收目录不存在或不可用")
	}
	if err := prepareSharedTaildropDirectory(value); err != nil {
		return failed(err.Error())
	}
	if err := os.MkdirAll(taildropStagingDir, 0700); err != nil {
		return failed("无法准备 Taildrop 临时目录")
	}
	if info, err := os.Stat(taildropStagingDir); err != nil || !info.IsDir() {
		return failed("Taildrop 临时目录不可用")
	}
	commandCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	out, stderr, truncated, err := run(commandCtx, filepath.Join(b.module, "bin", "tailscale"), "file", "get", "--conflict=rename", taildropStagingDir)
	r := response{OK: err == nil, TaildropPath: value, TaildropReceived: err == nil, Stdout: out, Stderr: stderr, Truncated: truncated}
	if err != nil {
		r.ExitCode = 1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			r.ExitCode = exit.ExitCode()
		}
		if commandCtx.Err() != nil {
			r.Error = "接收文件超时，请稍后重试"
		} else if stderr == "" {
			r.Error = "当前没有可接收的文件，或接收失败"
		}
	}
	published, publishErr := publishTaildropStaging(taildropStagingDir, value)
	if publishErr != nil {
		if err != nil && published == 0 {
			return r
		}
		r.OK = false
		r.TaildropReceived = false
		r.ExitCode = 1
		r.Error = publishErr.Error()
	} else if published > 0 {
		r.OK = true
		r.TaildropReceived = true
		r.Error = ""
	}
	return r
}

func acquireTaildropLock() (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(taildropPathFile), 0700); err != nil {
		return nil, errors.New("无法准备 Taildrop 锁")
	}
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(taildropPathFile), "taildrop-receive.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, errors.New("无法打开 Taildrop 锁")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, errors.New("已有文件接收操作正在进行")
	}
	return lock, nil
}

func (b *backend) awaitTaildrop(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Hour+10*time.Second)
	defer cancel()
	// The ordinary RPC client has a five-second timeout. File arrival waits
	// use their own request deadline and wake as soon as the daemon has files.
	client := *b.client
	client.Timeout = 0
	waiter := *b
	waiter.client = &client
	var files []json.RawMessage
	err := waiter.api(ctx, "GET", "files/?waitsec=3600", nil, &files)
	return len(files) > 0, err
}

func (b *backend) taildropAuto(ctx context.Context) {
	lastError := ""
	for ctx.Err() == nil {
		staged := false
		if directory, openErr := os.Open(taildropStagingDir); openErr == nil {
			names, _ := directory.Readdirnames(1)
			staged = len(names) > 0
			_ = directory.Close()
		}
		pending := staged
		var err error
		if !pending {
			pending, err = b.awaitTaildrop(ctx)
		}
		if ctx.Err() != nil {
			return
		}
		if pending {
			result := b.taildropReceive(ctx, "")
			if result.OK {
				lastError = ""
			} else {
				err = errors.New(result.Error)
			}
		}
		if err != nil {
			// The supervisor logs daemon restarts; an unreachable daemon at boot is routine.
			if err.Error() != lastError && !errors.Is(err, errDaemonUnreachable) {
				fmt.Fprintln(os.Stderr, localizedError(err.Error(), b.locale))
				lastError = err.Error()
			}
			timer := time.NewTimer(30 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}

func sharedTaildropBacking(path string) (string, bool) {
	if !validTaildropPath(path) {
		return "", false
	}
	canonical, ok := normalizeTaildropPath(path)
	if !ok || !strings.HasPrefix(canonical, sharedStorageRoot+string(os.PathSeparator)) {
		return "", false
	}
	relative := strings.TrimPrefix(canonical, sharedStorageRoot+string(os.PathSeparator))
	backing := filepath.Join(mediaStoragePath, relative)
	return backing, true
}

func mediaOwner(path string) (uint32, uint32, bool) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return 0, 0, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return stat.Uid, stat.Gid, true
}

func verifyMediaOwner(path string) error {
	uid, gid, ok := mediaOwner(path)
	if !ok || uid != 1023 || gid != 1023 {
		return errors.New("Android 媒体存储权限验证失败，文件已保留")
	}
	return nil
}

func restoreMediaLabel(path string) error {
	if _, err := os.Stat("/system/bin/restorecon"); err != nil {
		return errors.New("Android 存储标签工具不可用，文件已保留")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/system/bin/restorecon", "-F", path)
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return errors.New("无法恢复 Android 存储标签，文件已保留")
	}
	return nil
}

func prepareSharedTaildropDirectory(directory string) error {
	backing, ok := sharedTaildropBacking(directory)
	if !ok {
		return nil
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("接收目录不可用，文件已保留")
	}
	backingInfo, err := os.Lstat(backing)
	if err != nil || !backingInfo.IsDir() || backingInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("Android 媒体存储映射不可用，文件已保留")
	}
	if filepath.Clean(directory) == defaultTaildropPath {
		if err := os.Chown(backing, 1023, 1023); err != nil {
			return errors.New("无法设置 Android 接收目录属主，文件已保留")
		}
		if err := os.Chmod(backing, 0775); err != nil {
			return errors.New("无法设置 Android 接收目录权限，文件已保留")
		}
		if err := verifyMediaOwner(backing); err != nil {
			return err
		}
		if err := restoreMediaLabel(backing); err != nil {
			return err
		}
	}
	return nil
}

func normalizePublishedTaildropFile(path string) error {
	backing, ok := sharedTaildropBacking(path)
	if !ok {
		return nil
	}
	info, err := os.Lstat(backing)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Android 媒体存储文件映射不可用，文件已保留")
	}
	if err := os.Chown(backing, 1023, 1023); err != nil {
		return errors.New("无法设置 Android 接收文件属主，文件已保留")
	}
	if err := os.Chmod(backing, 0664); err != nil {
		return errors.New("无法设置 Android 接收文件权限，文件已保留")
	}
	if err := verifyMediaOwner(backing); err != nil {
		return err
	}
	if err := restoreMediaLabel(backing); err != nil {
		return err
	}
	return nil
}

// publishTaildropStaging copies files from the private module directory into
// shared storage. The source remains in staging until its copy is synced and
// closed successfully, so a failed publish can be retried without data loss.
func publishTaildropStaging(staging, directory string) (int, error) {
	return publishTaildropStagingWith(staging, directory, normalizePublishedTaildropFile)
}

func publishTaildropStagingWith(staging, directory string, normalize func(string) error) (int, error) {
	entries, err := os.ReadDir(staging)
	if err != nil {
		return 0, errors.New("无法读取 Taildrop 临时目录")
	}
	if len(entries) == 0 {
		return 0, errors.New("当前没有可接收的文件")
	}
	count := 0
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return count, errors.New("Taildrop 临时目录包含符号链接，文件已保留")
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return count, errors.New("Taildrop 临时目录包含非普通文件，文件已保留")
		}
		source := filepath.Join(staging, entry.Name())
		if resolved, err := filepath.EvalSymlinks(source); err != nil || resolved != source {
			return count, errors.New("Taildrop 文件路径无效，文件已保留")
		}
		target, file, err := openTaildropTarget(directory, entry.Name())
		if err != nil {
			return count, err
		}
		ok := false
		func() {
			in, openErr := os.Open(source)
			if openErr != nil {
				_ = file.Close()
				return
			}
			if _, copyErr := io.Copy(file, in); copyErr != nil {
				_ = in.Close()
				_ = file.Close()
				return
			}
			if closeErr := in.Close(); closeErr != nil {
				_ = file.Close()
				return
			}
			if chmodErr := file.Chmod(0664); chmodErr != nil {
				_ = file.Close()
				return
			}
			if syncErr := file.Sync(); syncErr != nil {
				_ = file.Close()
				return
			}
			ok = file.Close() == nil
		}()
		if !ok {
			_ = os.Remove(target)
			return count, errors.New("无法完整写入接收文件，文件已保留")
		}
		if err := normalize(target); err != nil {
			_ = os.Remove(target)
			return count, err
		}
		if err := os.Remove(source); err != nil {
			return count, errors.New("接收文件已写入但临时文件未清理")
		}
		count++
	}
	return count, nil
}

func openTaildropTarget(directory, name string) (string, *os.File, error) {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return "", nil, errors.New("接收文件名无效，文件已保留")
	}
	for attempt := 0; attempt < 100; attempt++ {
		candidate := name
		if attempt > 0 {
			ext := filepath.Ext(name)
			candidate = strings.TrimSuffix(name, ext) + fmt.Sprintf(" (%d)", attempt) + ext
		}
		target := filepath.Join(directory, candidate)
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0664)
		if err == nil {
			return target, file, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", nil, errors.New("无法在接收目录创建文件，文件已保留")
		}
	}
	return "", nil, errors.New("接收目录中没有可用文件名，文件已保留")
}

func validExitNodeTarget(value string) bool {
	if len(value) > 256 || (value != "" && !targetCharacters.MatchString(value)) {
		return false
	}
	return value == "" || (value != "." && value != "..")
}

func parseAdvertiseRoutes(value string) ([]string, error) {
	if len(value) > 1024 {
		return nil, errors.New("子网路由过长")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return []string{}, nil
	}
	parts := strings.Split(value, ",")
	if len(parts) > 16 {
		return nil, errors.New("最多发布 16 条子网路由")
	}
	routes := make([]string, 0, len(parts))
	seen := make(map[string]bool)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		prefix, err := netip.ParsePrefix(part)
		if err != nil || prefix != prefix.Masked() || prefix.Bits() == 0 {
			return nil, fmt.Errorf("无效的子网路由：%s", boundedText(part, 80))
		}
		canonical := prefix.String()
		if !seen[canonical] {
			seen[canonical] = true
			routes = append(routes, canonical)
		}
	}
	return routes, nil
}

func (b *backend) setExitNode(ctx context.Context, target string) response {
	if !validExitNodeTarget(target) {
		return failed("出口节点名称或地址无效")
	}
	cur := b.status(ctx)
	if !cur.DaemonRunning {
		return failed("请先启动后台服务")
	}
	p := map[string]any{
		"ExitNodeIDSet": true, "ExitNodeID": "",
		"ExitNodeIPSet": true, "ExitNodeIP": "",
		"NetfilterModeSet": true, "NetfilterMode": 0,
	}
	if target != "" {
		if cur.AdvertiseExitNode {
			return failed("本机正在发布出口节点，不能同时使用出口节点")
		}
		list := b.deviceList(ctx, true)
		if !list.OK {
			return list
		}
		var found *device
		for i, d := range *list.Devices {
			if d.ID == target || slices.Contains(d.IPs, target) {
				found = &(*list.Devices)[i]
				break
			}
		}
		if found == nil || !found.Online {
			return failed("所选出口节点离线或不可用")
		}
		p["ExitNodeID"] = found.ID
		// Netfilter only adds the connmark workaround for strict reverse-path filtering.
		if strictReversePath() {
			p["NetfilterMode"] = 2
		}
	}
	if err := b.api(ctx, "PATCH", "prefs", p, nil); err != nil {
		return failed(err.Error())
	}
	nudgeMagicDNS()
	return b.status(ctx)
}

var procRoot = "/proc"

func readProcInt(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	v, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return v
}

// strictReversePath reports whether any IPv4 interface uses strict rp_filter (max(all, iface) == 1).
func strictReversePath() bool {
	conf := filepath.Join(procRoot, "sys/net/ipv4/conf")
	all := readProcInt(filepath.Join(conf, "all/rp_filter"))
	entries, _ := os.ReadDir(conf)
	for _, e := range entries {
		switch e.Name() {
		case "all", "default", "lo", "tailscale0":
			continue
		}
		if max(all, readProcInt(filepath.Join(conf, e.Name(), "rp_filter"))) == 1 {
			return true
		}
	}
	return false
}

// tunnelIPv6Default reports whether ::/0 is routed via tailscale0 in any fib6
// table, or nil when the route table cannot be read.
func tunnelIPv6Default() *bool {
	data, err := os.ReadFile(filepath.Join(procRoot, "net/ipv6_route"))
	if err != nil {
		return nil
	}
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) == 10 && f[0] == strings.Repeat("0", 32) && f[1] == "00" && f[9] == "tailscale0" {
			found = true
			break
		}
	}
	return &found
}

func (b *backend) setAdvertisement(ctx context.Context, exitNode *bool, routes *string) response {
	if !b.status(ctx).DaemonRunning {
		return failed("请先启动后台服务")
	}
	var parsedRoutes []string
	if routes != nil {
		var err error
		parsedRoutes, err = parseAdvertiseRoutes(*routes)
		if err != nil {
			return failed(err.Error())
		}
	}
	var current localPrefs
	if err := b.api(ctx, "GET", "prefs", nil, &current); err != nil {
		return failed(err.Error())
	}
	const ipv4Default, ipv6Default = "0.0.0.0/0", "::/0"
	kept := make([]string, 0, len(current.AdvertiseRoutes)+len(parsedRoutes)+2)
	for _, route := range current.AdvertiseRoutes {
		isDefault := route == ipv4Default || route == ipv6Default
		if (routes == nil && !isDefault) || (isDefault && exitNode == nil) {
			kept = append(kept, route)
		}
	}
	if routes != nil {
		kept = append(kept, parsedRoutes...)
	}
	if exitNode != nil && *exitNode {
		kept = append(kept, ipv4Default, ipv6Default)
	}
	seen := make(map[string]bool, len(kept))
	unique := kept[:0]
	for _, route := range kept {
		if !seen[route] {
			seen[route] = true
			unique = append(unique, route)
		}
	}
	if exitNode == nil && routes == nil {
		return failed("发布设置不能为空")
	}
	if err := b.api(ctx, "PATCH", "prefs", map[string]any{
		"AdvertiseRoutes": unique, "AdvertiseRoutesSet": true,
	}, nil); err != nil {
		return failed(err.Error())
	}
	return b.status(ctx)
}

func failed(message string) response { return response{OK: false, Error: message, ExitCode: 1} }

type limitedBuffer struct {
	bytes.Buffer
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if left := 65536 - b.Len(); left > 0 {
		if len(p) > left {
			p = p[:left]
		}
		_, _ = b.Buffer.Write(p)
	}
	if b.Len() == 65536 {
		b.truncated = true
	}
	return n, nil
}

func run(ctx context.Context, file string, args ...string) (string, string, bool, error) {
	cmd := exec.CommandContext(ctx, file, args...)
	cmd.WaitDelay = time.Second
	cmd.Env = append(os.Environ(), "TS_BE_CLI=0")
	var stdout, stderr limitedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), stdout.truncated || stderr.truncated, err
}

// This is a command vocabulary, not a shell. No eval, sh -c, PATH lookup,
// expansion, quoting, operators, arbitrary socket/path overrides or subprocess features.
var commandCharacters = regexp.MustCompile(`^[a-zA-Z0-9 ._:/=@,+%\-]+$`)
var targetCharacters = regexp.MustCompile(`^[a-zA-Z0-9._:%\-]+$`)

func validateCommand(text string) ([]string, error) {
	if len(text) > 2048 || !commandCharacters.MatchString(text) {
		return nil, errors.New("仅支持普通参数；不允许 shell 语法、引号、换行或变量")
	}
	args := strings.Fields(text)
	if len(args) < 2 || len(args) > 48 {
		return nil, errors.New("请输入 tailscale 或 tailscaled 命令及受支持的参数")
	}
	if args[0] == "tailscaled" {
		if len(args) == 2 && (args[1] == "--help" || args[1] == "--version") {
			return args, nil
		}
		return nil, errors.New("tailscaled 仅开放 --help / --version；后台进程由模块管理")
	}
	if args[0] != "tailscale" {
		return nil, errors.New("仅允许 tailscale / tailscaled")
	}
	allowed := map[string]bool{"status": true, "ip": true, "version": true, "netcheck": true, "ping": true, "up": true, "down": true, "set": true, "login": true, "--help": true, "--version": true}
	if !allowed[args[1]] {
		return nil, errors.New("未开放此子命令；不允许 ssh、文件读写、debug、更新或调用其他程序")
	}
	positionals := 0
	for _, arg := range args[2:] {
		if !strings.HasPrefix(arg, "-") {
			if (args[1] == "ping" || args[1] == "ip") && targetCharacters.MatchString(arg) {
				positionals++
				continue
			}
			return nil, errors.New("未开放此位置参数；选项值请使用 --选项=值")
		}
		key, value, hasValue := strings.Cut(arg, "=")
		boolValue := !hasValue || value == "true" || value == "false"
		valid := key == "--help" && !hasValue
		switch args[1] {
		case "status":
			valid = valid || (key == "--json" || key == "--active" || key == "--peers" || key == "--self") && boolValue
		case "version":
			valid = valid || (key == "--json" || key == "--daemon") && boolValue
		case "ip":
			valid = valid || (key == "-4" || key == "-6" || key == "--4" || key == "--6") && !hasValue
		case "netcheck":
			valid = valid || key == "--verbose" && boolValue || key == "--format" && value == "json"
		case "ping":
			valid = valid || (key == "--icmp" || key == "--tsmp" || key == "--peerapi" || key == "--until-direct" || key == "--verbose") && boolValue
			if key == "--c" {
				n, err := strconv.Atoi(value)
				valid = err == nil && n >= 1 && n <= 5
			}
			if key == "--timeout" {
				d, err := time.ParseDuration(value)
				valid = err == nil && d > 0 && d <= 5*time.Second
			}
		case "up", "set":
			valid = valid || (key == "--accept-routes" || key == "--shields-up" || key == "--exit-node-allow-lan-access") && boolValue
			valid = valid || key == "--accept-dns" && value == "false" || key == "--netfilter-mode" && value == "off"
			valid = valid || (key == "--exit-node" || key == "--hostname") && hasValue && (value == "" || targetCharacters.MatchString(value))
		}
		if !valid {
			return nil, fmt.Errorf("未开放参数 %s", key)
		}
	}
	help := false
	for _, arg := range args[2:] {
		help = help || arg == "--help"
	}
	if positionals > 1 || args[1] == "ping" && positionals != 1 && !help {
		return nil, errors.New("仅支持一个目标地址")
	}
	return args, nil
}

func (b *backend) command(ctx context.Context, text string) response {
	args, err := validateCommand(text)
	if err != nil {
		return failed(err.Error())
	}
	if args[0] == "tailscale" && len(args) == 2 && (args[1] == "up" || args[1] == "down" || args[1] == "login") {
		r := b.connection(ctx, args[1] != "down")
		r.Stdout = stateLabel(r.State, b.locale)
		if r.LoginURL != "" {
			r.Stdout += "\n" + r.LoginURL
		}
		return r
	}
	file := filepath.Join(b.module, "bin", args[0])
	if args[0] == "tailscale" && args[1] == "ping" {
		args = append([]string{"tailscale", "ping", "--c=3", "--timeout=3s", "--until-direct=false"}, args[2:]...)
	}
	if args[0] == "tailscale" && args[1] == "up" {
		args = append(args, "--timeout=10s")
	}
	commandCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, stderr, truncated, err := run(commandCtx, file, args[1:]...)
	r := response{OK: err == nil, Stdout: out, Stderr: stderr, Truncated: truncated}
	if err != nil {
		r.ExitCode = 1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			r.ExitCode = exit.ExitCode()
		}
		if commandCtx.Err() != nil {
			r.Error = "命令超时，已停止；请刷新核对状态"
		} else if stderr == "" {
			r.Error = "命令执行失败"
		}
	}
	return r
}

func decodeRequest(encoded string) (request, error) {
	var r request
	if len(encoded) > 8192 {
		return r, errors.New("请求过长")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return r, errors.New("请求编码无效")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil {
		return r, errors.New("请求格式无效")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return r, errors.New("仅允许一个请求")
	}
	switch r.Locale {
	case "", "system", "en", "zh-CN", "zh-TW":
	default:
		return r, errors.New("不支持此语言")
	}
	var inputFields map[string]json.RawMessage
	if json.Unmarshal(data, &inputFields) != nil {
		return r, errors.New("请求格式无效")
	}
	for key := range inputFields {
		if strings.EqualFold(key, "channel") && r.Action != "binary-channel" {
			return r, errors.New("请求参数不匹配")
		}
	}
	allowedFields := map[string]map[string]bool{
		"status": {"action": true, "locale": true}, "devices": {"action": true, "locale": true},
		"exit-nodes": {"action": true, "locale": true},
		"console":    {"action": true, "locale": true}, "up": {"action": true, "locale": true}, "down": {"action": true, "locale": true},
		"about": {"action": true, "locale": true}, "login": {"action": true, "locale": true},
		"binary-check": {"action": true, "locale": true}, "binary-update": {"action": true, "locale": true},
		"binary-channel": {"action": true, "locale": true, "channel": true},
		"settings":       {"action": true, "locale": true, "settings": true}, "command": {"action": true, "locale": true, "command": true},
		"exit-node":     {"action": true, "locale": true, "exitNode": true},
		"advertise":     {"action": true, "locale": true, "advertiseRoutes": true, "advertiseExitNode": true},
		"taildrop-list": {"action": true, "locale": true, "path": true}, "taildrop-path": {"action": true, "locale": true, "path": true}, "taildrop-receive": {"action": true, "locale": true, "path": true},
	}
	if fields, ok := allowedFields[r.Action]; ok {
		for key := range inputFields {
			if !fields[key] {
				return r, errors.New("请求参数不匹配")
			}
		}
	}
	switch r.Action {
	case "binary-channel":
		for key := range inputFields {
			if key != "action" && key != "locale" && key != "channel" {
				return r, errors.New("请求参数不匹配")
			}
		}
		if r.Channel != "stable" && r.Channel != "preview" {
			return r, errors.New("不支持此更新频道")
		}
	case "about", "login", "binary-check", "binary-update":
		// These operations have no caller-selectable URL, path, version, or
		// command, including otherwise-known fields with empty/null values.
		for key := range inputFields {
			if key != "action" && key != "locale" {
				return r, errors.New("请求参数不匹配")
			}
		}
	case "status", "devices", "exit-nodes", "console", "up", "down":
		if r.Command != "" || r.Settings != nil || r.Path != "" || r.ExitNode != "" || r.AdvertiseRoutes != nil || r.AdvertiseExitNode != nil {
			return r, errors.New("请求参数不匹配")
		}
	case "exit-node":
		for key := range inputFields {
			if key != "action" && key != "locale" && key != "exitNode" {
				return r, errors.New("请求参数不匹配")
			}
		}
		if !validExitNodeTarget(r.ExitNode) {
			return r, errors.New("出口节点名称或地址无效")
		}
	case "advertise":
		for key := range inputFields {
			if key != "action" && key != "locale" && key != "advertiseRoutes" && key != "advertiseExitNode" {
				return r, errors.New("请求参数不匹配")
			}
		}
		if r.AdvertiseRoutes == nil && r.AdvertiseExitNode == nil {
			return r, errors.New("发布设置不能为空")
		}
		if r.AdvertiseRoutes != nil {
			if _, err := parseAdvertiseRoutes(*r.AdvertiseRoutes); err != nil {
				return r, err
			}
		}
	case "taildrop-list":
		if r.Command != "" || r.Settings != nil || len(r.Path) > 512 || (r.Path != "" && !validTaildropBrowsePath(r.Path)) {
			return r, errors.New("浏览目录无效")
		}
	case "taildrop-path", "taildrop-receive":
		if r.Command != "" || r.Settings != nil || len(r.Path) > 512 || (r.Path != "" && !validTaildropPath(r.Path)) {
			return r, errors.New("接收目录无效")
		}
	case "settings":
		if r.Settings == nil || r.Command != "" || r.Path != "" || r.ExitNode != "" || r.AdvertiseRoutes != nil || r.AdvertiseExitNode != nil {
			return r, errors.New("请求参数不匹配")
		}
	case "command":
		if r.Command == "" || r.Settings != nil || r.Path != "" || r.ExitNode != "" || r.AdvertiseRoutes != nil || r.AdvertiseExitNode != nil {
			return r, errors.New("请求参数不匹配")
		}
	default:
		return r, errors.New("不支持此操作")
	}
	return r, nil
}

func (b *backend) refreshDescription(ctx context.Context) {
	lock, err := os.OpenFile(dataDir+"/run/description.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return
	}
	defer lock.Close()
	for {
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	// UI polls, action presses and the supervisor run in different processes.
	// Fetch after acquiring the lock so an earlier poll cannot overwrite a later transition.
	value := description(b.status(ctx).State, savedLocale())
	boot, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	stamp := "dual-description-v1\n" + string(boot) + value
	cache := dataDir + "/run/description.cache"
	previous, _ := os.ReadFile(cache)
	if string(previous) == stamp {
		return
	}
	// KernelSU hides its module sysfs directory, and WebUI/adb shells do not
	// reliably inherit KSU=true. Keep both module-owned description sources in
	// sync instead of guessing the manager from either of those signals.
	if _, err := os.Stat("/data/adb/ksud"); err == nil {
		configCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(configCtx, "/data/adb/ksud", "module", "config", "set", "--temp", "override.description", value)
		cmd.Env = append(os.Environ(), "KSU_MODULE=shellscaled")
		_ = cmd.Run()
	}
	// Magisk and older KernelSU use module.prop. Updating it also avoids stale
	// status when moving the same installation between the two test managers.
	{
		path := filepath.Join(b.module, "module.prop")
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		lines := strings.Split(string(data), "\n")
		found := false
		for i, line := range lines {
			if strings.HasPrefix(line, "description=") {
				lines[i] = "description=" + value
				found = true
			}
		}
		if !found {
			return
		}
		if err := os.WriteFile(path+".status", []byte(strings.Join(lines, "\n")), 0644); err != nil {
			return
		}
		if err := os.Rename(path+".status", path); err != nil {
			return
		}
	}
	_ = os.WriteFile(cache, []byte(stamp), 0600)
}

func main() {
	var r response
	if os.Geteuid() != 0 {
		_ = json.NewEncoder(os.Stdout).Encode(failed(tr("需要 root 权限", savedLocale())))
		os.Exit(1)
	}
	executable, err := os.Executable()
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(failed(tr("无法定位模块", savedLocale())))
		os.Exit(1)
	}
	b := newBackend(filepath.Dir(filepath.Dir(executable)))
	if len(os.Args) == 2 && os.Args[1] == "magicdns-proxy" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
		defer stop()
		if err := b.magicDNSProxy(ctx); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "taildrop-auto" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
		defer stop()
		b.taildropAuto(ctx)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if len(os.Args) == 2 && os.Args[1] == "refresh-description" {
		b.refreshDescription(ctx)
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "toggle" {
		st := b.status(ctx)
		r = b.connection(ctx, st.State != "Running" && st.State != "Starting")
	} else if len(os.Args) == 3 && os.Args[1] == "rpc" {
		req, err := decodeRequest(os.Args[2])
		if err != nil {
			r = failed(err.Error())
		} else {
			b.selectLocale(req.Locale)
			if req.Action == "binary-update" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 180*time.Second)
				defer cancel()
			}
			switch req.Action {
			case "status":
				r = b.status(ctx)
			case "devices":
				r = b.devices(ctx)
			case "exit-nodes":
				r = b.deviceList(ctx, true)
			case "console":
				r = b.console(ctx)
			case "about":
				r = b.about(ctx)
			case "login":
				r = b.login(ctx)
			case "binary-check", "binary-update":
				r = b.binary(ctx, req.Action == "binary-update")
			case "binary-channel":
				r = b.binaryChannel(req.Channel)
			case "up":
				r = b.connection(ctx, true)
			case "down":
				r = b.connection(ctx, false)
			case "settings":
				r = b.setSettings(ctx, req.Settings)
			case "exit-node":
				r = b.setExitNode(ctx, req.ExitNode)
			case "advertise":
				r = b.setAdvertisement(ctx, req.AdvertiseExitNode, req.AdvertiseRoutes)
			case "taildrop-path":
				r = b.taildropPath(ctx, req.Path)
			case "taildrop-list":
				r = taildropDirectories(req.Path)
			case "taildrop-receive":
				r = b.taildropReceive(ctx, req.Path)
			case "command":
				r = b.command(ctx, req.Command)
			}
		}
	} else {
		r = failed("仅支持固定 RPC 或模块快捷操作")
	}
	if r.State != "" {
		b.refreshDescription(ctx)
	}
	r.Error = localizedError(r.Error, b.locale)
	if len(os.Args) == 2 && os.Args[1] == "toggle" {
		if r.OK {
			fmt.Println(description(r.State, b.locale))
		} else {
			fmt.Println(tr("操作失败", b.locale) + r.Error)
		}
		if r.LoginURL != "" {
			fmt.Println(tr("浏览器授权", b.locale) + "\n" + r.LoginURL)
		}
		if !r.OK {
			os.Exit(1)
		}
		return
	}
	_ = json.NewEncoder(os.Stdout).Encode(r)
	if !r.OK {
		os.Exit(1)
	}
}
