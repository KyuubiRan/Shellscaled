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
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const dataDir = "/data/adb/shellscaled"
const socketPath = dataDir + "/run/tailscaled.sock"

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
	Action   string         `json:"action"`
	Channel  string         `json:"channel,omitempty"`
	Locale   string         `json:"locale,omitempty"`
	Command  string         `json:"command,omitempty"`
	Settings *settingsPatch `json:"settings,omitempty"`
}

type response struct {
	OK                 bool          `json:"ok"`
	State              string        `json:"state,omitempty"`
	Version            string        `json:"version,omitempty"`
	IPs                []string      `json:"ips,omitempty"`
	Settings           settings      `json:"settings"`
	DaemonRunning      bool          `json:"daemonRunning"`
	ExitNodeConfigured bool          `json:"exitNodeConfigured"`
	LoginURL           string        `json:"loginURL,omitempty"`
	Error              string        `json:"error,omitempty"`
	ExitCode           int           `json:"exitCode"`
	Stdout             string        `json:"stdout,omitempty"`
	Stderr             string        `json:"stderr,omitempty"`
	Truncated          bool          `json:"truncated,omitempty"`
	Devices            *[]device     `json:"devices,omitempty"`
	Binary             *binaryStatus `json:"binary,omitempty"`
	UpdateChannel      string        `json:"updateChannel,omitempty"`
}

// Deliberately decode only UI fields. Never return prefs.Persist or account data.
type localStatus struct {
	BackendState string
	Version      string
	TailscaleIPs []string
	AuthURL      string
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
	ID           string
	HostName     string
	DNSName      string
	TailscaleIPs []string
	Online       bool
	OS           string
	LastSeen     time.Time
}

type device struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	DNSName  string     `json:"dnsName"`
	IPs      []string   `json:"ips"`
	Online   bool       `json:"online"`
	OS       string     `json:"os"`
	Self     bool       `json:"self"`
	LastSeen *time.Time `json:"lastSeen,omitempty"`
}

type localPrefs struct {
	RouteAll               bool
	ShieldsUp              bool
	ExitNodeAllowLANAccess bool
	ExitNodeID             string
	ExitNodeIP             string
}

type backend struct {
	stateDir string
	module   string
	client   *http.Client
	locale   string
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
		return errors.New("后台服务未响应，请先启动或重启 tailscaled")
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
	r := response{OK: true, State: "ServiceStopped", Version: b.moduleVersion(), IPs: []string{}, UpdateChannel: readUpdateChannel(b.updateDataDir(), b.module)}
	var st localStatus
	if err := b.api(ctx, "GET", "status?peers=false", nil, &st); err != nil {
		if _, statErr := os.Stat(socketPath); statErr == nil {
			r.State = "ServiceError"
			r.Error = err.Error()
		}
		return r
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
		return r
	}
	r.Settings = settings{p.RouteAll, p.ShieldsUp, p.ExitNodeAllowLANAccess}
	r.ExitNodeConfigured = p.ExitNodeID != "" || (p.ExitNodeIP != "" && p.ExitNodeIP != "invalid IP")
	return r
}

func (b *backend) devices(ctx context.Context) response {
	var status localDevices
	if err := b.api(ctx, "GET", "status?peers=true", nil, &status); err != nil {
		return failed(err.Error())
	}
	devices := make([]device, 0, len(status.Peer)+1)
	add := func(peer *localDevice, self bool) {
		if peer == nil {
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
		entry := device{ID: boundedText(peer.ID, 128), Name: name, DNSName: dnsName, IPs: ips, Online: peer.Online, OS: boundedText(peer.OS, 32), Self: self}
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
	case "about", "binary-check", "binary-update":
		// These operations have no caller-selectable URL, path, version, or
		// command, including otherwise-known fields with empty/null values.
		for key := range inputFields {
			if key != "action" && key != "locale" {
				return r, errors.New("请求参数不匹配")
			}
		}
	case "status", "devices", "console", "up", "down":
		if r.Command != "" || r.Settings != nil {
			return r, errors.New("请求参数不匹配")
		}
	case "settings":
		if r.Settings == nil || r.Command != "" {
			return r, errors.New("请求参数不匹配")
		}
	case "command":
		if r.Command == "" || r.Settings != nil {
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
			case "console":
				r = b.console(ctx)
			case "about":
				r = b.about(ctx)
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
