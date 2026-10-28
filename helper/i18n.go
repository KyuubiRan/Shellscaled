// Copyright (c) 2026 KyuubiRan
// SPDX-License-Identifier: BSD-3-Clause
package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

func normalizeLocale(value string) string {
	value = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "_", "-"))
	if strings.HasPrefix(value, "zh") {
		if strings.Contains(value, "hant") {
			return "zh-TW"
		}
		if strings.Contains(value, "hans") {
			return "zh-CN"
		}
		if strings.Contains(value, "-tw") || strings.Contains(value, "-hk") || strings.Contains(value, "-mo") {
			return "zh-TW"
		}
		return "zh-CN"
	}
	return "en"
}

func systemLocale() string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, property := range []string{"persist.sys.locales", "persist.sys.locale", "ro.product.locale"} {
		value, err := exec.CommandContext(ctx, "/system/bin/getprop", property).Output()
		if err == nil && strings.TrimSpace(string(value)) != "" {
			for _, tag := range strings.Split(string(value), ",") {
				lower := strings.ToLower(strings.TrimSpace(tag))
				if strings.HasPrefix(lower, "zh") || strings.HasPrefix(lower, "en") {
					return normalizeLocale(tag)
				}
			}
			return "en"
		}
	}
	return "en"
}

func savedLocale() string {
	value, _ := os.ReadFile(dataDir + "/ui.locale")
	switch locale := strings.TrimSpace(string(value)); locale {
	case "en", "zh-CN", "zh-TW":
		return locale
	default:
		return systemLocale()
	}
}

func (b *backend) selectLocale(preference string) {
	if preference == "" {
		return
	}
	b.locale = preference
	if preference == "system" {
		b.locale = systemLocale()
	}
	path := dataDir + "/ui.locale"
	previous, _ := os.ReadFile(path)
	if strings.TrimSpace(string(previous)) == preference {
		return
	}
	file, err := os.CreateTemp(dataDir, ".locale-*")
	if err != nil {
		return
	}
	defer os.Remove(file.Name())
	_, writeErr := file.WriteString(preference + "\n")
	closeErr := file.Close()
	if writeErr == nil && closeErr == nil {
		_ = os.Rename(file.Name(), path)
	}
}

// CLI output remains untouched. Only application messages are translated.
var messages = map[string][3]string{
	"无法打开项目主页，请安装或启用浏览器后重试": {"无法打开项目主页，请安装或启用浏览器后重试。", "無法開啟專案首頁，請安裝或啟用瀏覽器後重試。", "Could not open the project page. Install or enable a browser and try again."},
	"不支持此更新频道":                                    {"不支持此更新频道。", "不支援此更新頻道。", "This update channel is not supported."},
	"无法保存更新频道，请重试":                                {"无法保存更新频道，请重试。", "無法儲存更新頻道，請重試。", "Could not save the update channel. Try again."},
	"二进制更新正在进行，请稍后重试":                             {"二进制更新正在进行，请稍后重试。", "二進位更新正在進行，請稍後重試。", "A binary update is in progress. Try again later."},
	"无法检查二进制更新，请稍后重试":                             {"无法检查二进制更新，请稍后重试。", "無法檢查二進位更新，請稍後重試。", "Could not check for a binary update. Try again later."},
	"更新文件校验失败，未替换当前二进制":                           {"更新文件校验失败，未替换当前二进制。", "更新檔案驗證失敗，未替換目前的二進位。", "Update verification failed. The current binary was not replaced."},
	"二进制更新失败，已尝试恢复原服务":                            {"二进制更新失败，已尝试恢复原服务。", "二進位更新失敗，已嘗試還原原有服務。", "The binary update failed. Recovery of the previous service was attempted."},
	"二进制恢复失败，请重新安装模块":                             {"二进制恢复失败，请重新安装模块。", "二進位還原失敗，請重新安裝模組。", "Binary recovery failed. Reinstall the module."},
	"需要 root 权限":                                  {"请授予 root 权限后重试。", "請授予 root 權限後重試。", "Grant root access and try again."},
	"无法定位模块":                                      {"模块不可用，请重新安装。", "模組無法使用，請重新安裝。", "Module unavailable. Reinstall it and try again."},
	"后台服务未响应，请先启动或重启 tailscaled":                  {"服务未响应，请重新连接。", "服務未回應，請重新連線。", "Service unavailable. Reconnect and try again."},
	"无法读取后台状态":                                    {"无法读取状态，请重试。", "無法讀取狀態，請重試。", "Could not read the status. Try again."},
	"模块已停用":                                       {"请先启用模块。", "請先啟用模組。", "Enable the module first."},
	"模块正在等待移除":                                    {"模块正在等待卸载。", "模組正在等待解除安裝。", "The module is pending removal."},
	"操作超时，请刷新状态后再确认":                              {"操作超时，请刷新状态。", "操作逾時，請重新整理狀態。", "The operation timed out. Refresh the status."},
	"设置不能为空":                                      {"请选择需要修改的设置。", "請選擇要變更的設定。", "Choose a setting to change."},
	"请先启动后台服务":                                    {"请先连接 Tailscale。", "請先連線 Tailscale。", "Connect Tailscale first."},
	"请先通过 tailscale set --exit-node=目标 选择出口节点":    {"请先选择出口节点。", "請先選擇出口節點。", "Select an exit node first."},
	"仅支持普通参数；不允许 shell 语法、引号、换行或变量":               {"命令格式不受支持，请查看使用帮助。", "不支援此命令格式，請查看使用說明。", "Unsupported command format. See the command help."},
	"请输入 tailscale 或 tailscaled 命令及受支持的参数":        {"请输入 tailscale 或 tailscaled 命令。", "請輸入 tailscale 或 tailscaled 命令。", "Enter a tailscale or tailscaled command."},
	"tailscaled 仅开放 --help / --version；后台进程由模块管理": {"tailscaled 仅支持 --help 和 --version。", "tailscaled 僅支援 --help 和 --version。", "tailscaled supports only --help and --version."},
	"仅允许 tailscale / tailscaled":                  {"仅支持 tailscale 和 tailscaled 命令。", "僅支援 tailscale 和 tailscaled 命令。", "Only tailscale and tailscaled commands are supported."},
	"未开放此子命令；不允许 ssh、文件读写、debug、更新或调用其他程序":        {"不支持此命令，请查看使用帮助。", "不支援此命令，請查看使用說明。", "Unsupported command. See the command help."},
	"未开放此位置参数；选项值请使用 --选项=值":                      {"参数格式不正确，请使用 --选项=值。", "參數格式不正確，請使用 --選項=值。", "Use --option=value for option values."},
	"仅支持一个目标地址":                                   {"请输入一个目标地址。", "請輸入一個目標位址。", "Enter one target address."},
	"命令超时，已停止；请刷新核对状态":                            {"命令已超时，请刷新状态。", "命令已逾時，請重新整理狀態。", "The command timed out. Refresh the status."},
	"命令执行失败":                                      {"命令执行失败。", "命令執行失敗。", "The command failed."},
	"请求过长":                                        {"操作内容过长，请缩短后重试。", "操作內容過長，請縮短後重試。", "The request is too long. Shorten it and try again."},
	"请求编码无效":                                      {"无法处理此操作，请重试。", "無法處理此操作，請重試。", "Could not process the request. Try again."},
	"请求格式无效":                                      {"无法处理此操作，请重试。", "無法處理此操作，請重試。", "Could not process the request. Try again."},
	"仅允许一个请求":                                     {"无法处理此操作，请重试。", "無法處理此操作，請重試。", "Could not process the request. Try again."},
	"请求参数不匹配":                                     {"无法处理此操作，请重试。", "無法處理此操作，請重試。", "Could not process the request. Try again."},
	"不支持此操作":                                      {"不支持此操作。", "不支援此操作。", "This operation is not supported."},
	"仅支持固定 RPC 或模块快捷操作":                           {"请从模块页面操作。", "請從模組頁面操作。", "Open the module page to continue."},
	"不支持此语言":                                      {"不支持此语言。", "不支援此語言。", "This language is not supported."},
	"后台服务启动失败":                                    {"服务启动失败，请重试。", "服務啟動失敗，請重試。", "Could not start the service. Try again."},
	"服务请求失败":                                      {"服务暂时不可用，请重试。", "服務暫時無法使用，請重試。", "Service temporarily unavailable. Try again."},
	"未开放参数":                                       {"不支持此选项：", "不支援此選項：", "Unsupported option: "},
	"操作失败":                                        {"操作失败：", "操作失敗：", "Operation failed: "},
	"无法打开控制台，请安装或启用浏览器后重试":                        {"无法打开控制台，请安装或启用浏览器后重试。", "無法開啟控制台，請安裝或啟用瀏覽器後重試。", "Could not open the console. Install or enable a browser and try again."},
	"浏览器授权":                                       {"请在浏览器中登录：", "請在瀏覽器中登入：", "Sign in using your browser:"},
}

func tr(key, locale string) string {
	if values, ok := messages[key]; ok {
		switch locale {
		case "zh-CN":
			return values[0]
		case "zh-TW":
			return values[1]
		default:
			return values[2]
		}
	}
	return key
}

func localizedError(value, locale string) string {
	if flag, ok := strings.CutPrefix(value, "未开放参数 "); ok {
		return tr("未开放参数", locale) + flag
	}
	if strings.HasPrefix(value, "后台服务启动失败：") {
		return tr("后台服务启动失败", locale)
	}
	if strings.HasPrefix(value, "LocalAPI ") || value == "unexpected LocalAPI address" {
		return tr("服务请求失败", locale)
	}
	return tr(value, locale)
}

func stateLabel(state, locale string) string {
	labels := map[string][3]string{
		"Running": {"已连接", "已連線", "Connected"}, "Stopped": {"已断开", "已中斷連線", "Disconnected"},
		"NeedsLogin": {"需要登录", "需要登入", "Sign in required"}, "NeedsMachineAuth": {"等待设备批准", "等待裝置核准", "Waiting for device approval"},
		"Starting": {"连接中", "連線中", "Connecting"}, "NoState": {"正在启动", "正在啟動", "Starting"},
		"ServiceStopped": {"服务未启动", "服務未啟動", "Service stopped"}, "ServiceError": {"服务未响应", "服務未回應", "Service unavailable"},
	}
	values, ok := labels[state]
	if !ok {
		values = [3]string{"等待刷新", "等待重新整理", "Refresh required"}
	}
	switch locale {
	case "zh-CN":
		return values[0]
	case "zh-TW":
		return values[1]
	default:
		return values[2]
	}
}

func description(state, locale string) string {
	hint := [3]string{" · 点击运行连接", " · 點選執行以連線", " · Tap Run to connect"}
	if state == "Running" || state == "Starting" {
		hint = [3]string{" · 点击运行断开", " · 點選執行以中斷連線", " · Tap Run to disconnect"}
	}
	switch locale {
	case "zh-CN":
		return stateLabel(state, locale) + hint[0]
	case "zh-TW":
		return stateLabel(state, locale) + hint[1]
	default:
		return stateLabel(state, locale) + hint[2]
	}
}
