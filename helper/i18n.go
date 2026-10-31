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
	"所选出口节点离线或不可用":                    {"所选出口节点离线或不可用。", "所選出口節點離線或無法使用。", "The selected exit node is offline or unavailable."},
	"本机正在发布出口节点，不能同时使用出口节点":           {"本机正在发布出口节点，不能同时使用出口节点。", "本機正在發布出口節點，無法同時使用出口節點。", "This device is advertising itself as an exit node and cannot use one at the same time."},
	"出口节点名称或地址无效":                     {"出口节点名称或地址无效。", "出口節點名稱或位址無效。", "Choose a valid exit node."},
	"无法保存 IPv6 出口设置":                  {"无法保存 IPv6 出口设置。", "無法儲存 IPv6 出口設定。", "Could not save the exit node IPv6 setting."},
	"IPv6 出口设置已保存，需要随本版模块发布的后台服务才能生效": {"设置已保存，但需要随本版模块发布的服务版本才能生效。", "設定已儲存，但需要隨本版模組發布的服務版本才能生效。", "Saved, but this requires the daemon shipped with this module version to take effect."},
	"发布设置不能为空":                        {"请选择要发布的网络角色。", "請選擇要公布的網路角色。", "Choose the network role to advertise."},
	"子网路由过长":                          {"子网路由过长。", "子網路由過長。", "The subnet route list is too long."},
	"子网路由格式无效":                        {"请输入规范的 CIDR 子网路由；出口能力请使用出口节点开关。", "請輸入標準的 CIDR 子網路由；出口功能請使用出口節點開關。", "Enter canonical CIDR subnet routes. Use the exit node switch to advertise an exit node."},
	"最多发布 16 条子网路由":                   {"最多发布 16 条子网路由。", "最多公布 16 條子網路由。", "Advertise up to 16 subnet routes."},
	"接收目录必须位于 Android 共享存储，且不能位于 Android/data、Android/obb 或 Android/sandbox": {"请选择主外部存储中的普通目录。", "請選擇主外部儲存空間中的一般目錄。", "Choose an ordinary directory in primary shared storage."},
	"接收目录不存在或不可用":                                 {"接收目录不存在或不可用。", "接收目錄不存在或無法使用。", "The receive directory is unavailable."},
	"接收目录无效":                                      {"请选择有效的接收目录。", "請選擇有效的接收目錄。", "Choose a valid receive directory."},
	"接收目录不能通过符号链接指向其他位置":                          {"请选择直接位于共享存储的目录。", "請選擇直接位於共用儲存空間的目錄。", "Choose a directory directly within shared storage."},
	"无法保存接收目录":                                    {"无法保存接收目录。", "無法儲存接收目錄。", "Could not save the receive directory."},
	"浏览目录无效":                                      {"请选择有效的浏览目录。", "請選擇有效的瀏覽目錄。", "Choose a valid directory to browse."},
	"浏览目录不存在或不可用":                                 {"浏览目录不存在或不可用。", "瀏覽目錄不存在或無法使用。", "The directory is unavailable."},
	"浏览目录不能是符号链接":                                 {"请选择直接位于共享存储的目录。", "請選擇直接位於共用儲存空間的目錄。", "Choose a directory directly within shared storage."},
	"无法读取目录":                                      {"无法读取目录。", "無法讀取目錄。", "Could not read the directory."},
	"当前没有可接收的文件":                                  {"当前没有待接收文件。", "目前沒有待接收檔案。", "There are no files waiting to be received."},
	"Taildrop 临时目录不可用":                            {"Taildrop 暂存目录不可用。", "Taildrop 暫存目錄無法使用。", "The Taildrop staging directory is unavailable."},
	"无法准备 Taildrop 临时目录":                          {"无法准备 Taildrop 暂存目录。", "無法準備 Taildrop 暫存目錄。", "Could not prepare the Taildrop staging directory."},
	"无法读取 Taildrop 临时目录":                          {"无法读取 Taildrop 暂存目录。", "無法讀取 Taildrop 暫存目錄。", "Could not read the Taildrop staging directory."},
	"Taildrop 临时目录包含符号链接，文件已保留":                   {"暂存目录包含符号链接，文件已保留。", "暫存目錄包含符號連結，檔案已保留。", "Staged files were retained because a symbolic link was found."},
	"Taildrop 临时目录包含非普通文件，文件已保留":                  {"暂存目录包含特殊文件，文件已保留。", "暫存目錄包含特殊檔案，檔案已保留。", "Staged files were retained because an unsupported file type was found."},
	"Taildrop 文件路径无效，文件已保留":                       {"暂存文件路径无效，文件已保留。", "暫存檔案路徑無效，檔案已保留。", "Staged files were retained because a file path is invalid."},
	"接收文件名无效，文件已保留":                               {"接收文件名无效，文件已保留。", "接收檔案名稱無效，檔案已保留。", "Staged files were retained because a file name is invalid."},
	"无法在接收目录创建文件，文件已保留":                           {"无法在接收目录创建文件，暂存副本已保留。", "無法在接收目錄建立檔案，暫存副本已保留。", "Could not create the received file. Its staged copy was retained."},
	"无法完整写入接收文件，文件已保留":                            {"接收文件写入失败，暂存副本已保留。", "接收檔案寫入失敗，暫存副本已保留。", "Could not finish writing the received file. Its staged copy was retained."},
	"接收文件已写入但临时文件未清理":                             {"文件已保存，暂存副本清理失败。", "檔案已儲存，暫存副本清理失敗。", "The file was saved, but its staged copy could not be removed."},
	"接收目录中没有可用文件名，文件已保留":                          {"接收目录存在过多同名文件，暂存副本已保留。", "接收目錄存在過多同名檔案，暫存副本已保留。", "Too many files share this name. The staged copy was retained."},
	"无法准备 Taildrop 锁":                             {"无法准备接收操作锁。", "無法準備接收操作鎖。", "Could not prepare the receive operation lock."},
	"无法打开 Taildrop 锁":                             {"无法打开接收操作锁。", "無法開啟接收操作鎖。", "Could not open the receive operation lock."},
	"已有文件接收操作正在进行":                                {"正在接收文件，请稍候。", "正在接收檔案，請稍候。", "Files are being received. Please wait."},
	"Android 媒体存储权限验证失败，文件已保留":                    {"Android 存储权限验证失败，暂存文件已保留。", "Android 儲存權限驗證失敗，暫存檔案已保留。", "Android storage ownership verification failed. Staged files were retained."},
	"Android 存储标签工具不可用，文件已保留":                     {"Android 存储标签工具不可用，暂存文件已保留。", "Android 儲存標籤工具無法使用，暫存檔案已保留。", "The Android storage labeling tool is unavailable. Staged files were retained."},
	"无法恢复 Android 存储标签，文件已保留":                     {"存储标签恢复失败，暂存文件已保留。", "儲存標籤還原失敗，暫存檔案已保留。", "Android storage labeling failed. Staged files were retained."},
	"接收目录不可用，文件已保留":                               {"接收目录不可用，暂存文件已保留。", "接收目錄無法使用，暫存檔案已保留。", "The receive directory is unavailable. Staged files were retained."},
	"Android 媒体存储映射不可用，文件已保留":                     {"Android 存储映射不可用，暂存文件已保留。", "Android 儲存映射無法使用，暫存檔案已保留。", "The Android shared storage mapping is unavailable. Staged files were retained."},
	"Android 媒体存储文件映射不可用，文件已保留":                   {"接收文件的存储映射不可用，暂存文件已保留。", "接收檔案的儲存映射無法使用，暫存檔案已保留。", "The received file's storage mapping is unavailable. Its staged copy was retained."},
	"无法设置 Android 接收目录属主，文件已保留":                   {"接收目录属主设置失败，暂存文件已保留。", "接收目錄擁有者設定失敗，暫存檔案已保留。", "Could not set the receive directory owner. Staged files were retained."},
	"无法设置 Android 接收目录权限，文件已保留":                   {"接收目录权限设置失败，暂存文件已保留。", "接收目錄權限設定失敗，暫存檔案已保留。", "Could not set the receive directory permissions. Staged files were retained."},
	"无法设置 Android 接收文件属主，文件已保留":                   {"接收文件属主设置失败，暂存副本已保留。", "接收檔案擁有者設定失敗，暫存副本已保留。", "Could not set the received file owner. Its staged copy was retained."},
	"无法设置 Android 接收文件权限，文件已保留":                   {"接收文件权限设置失败，暂存副本已保留。", "接收檔案權限設定失敗，暫存副本已保留。", "Could not set the received file permissions. Its staged copy was retained."},
	"接收文件超时，请稍后重试":                                {"文件接收超时，请稍后重试。", "檔案接收逾時，請稍後重試。", "Receiving timed out. Try again later."},
	"当前没有可接收的文件，或接收失败":                            {"请检查待接收文件和连接状态后重试。", "請檢查待接收檔案及連線狀態後重試。", "Check pending files and the connection, then try again."},
	"登录链接无效，请刷新后重试":                               {"登录链接无效，请刷新后重试。", "登入連結無效，請重新整理後重試。", "The sign-in link is invalid. Refresh and try again."},
	"登录链接尚未准备好，请稍后重试":                             {"登录链接尚未准备好，请稍后重试。", "登入連結尚未準備好，請稍後重試。", "The sign-in link is not ready. Try again shortly."},
	"无法打开登录页面，请安装或启用浏览器后重试":                       {"无法打开登录页面，请安装或启用浏览器后重试。", "無法開啟登入頁面，請安裝或啟用瀏覽器後重試。", "Could not open the sign-in page. Install or enable a browser and try again."},
	"无法打开项目主页，请安装或启用浏览器后重试":                       {"无法打开项目主页，请安装或启用浏览器后重试。", "無法開啟專案首頁，請安裝或啟用瀏覽器後重試。", "Could not open the project page. Install or enable a browser and try again."},
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
	if strings.HasPrefix(value, "无效的子网路由：") {
		return tr("子网路由格式无效", locale)
	}
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
