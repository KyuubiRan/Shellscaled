/* Shellscaled WebUI. Uses KernelSU's documented exec(command, options, callback)
 * interface: https://github.com/tiann/KernelSU/tree/main/js
 * This narrow adapter is implemented here; the KernelSU SDK is not vendored.
 */
"use strict";

(() => {
  const RPC_COMMAND = "/data/adb/modules/shellscaled/bin/shellscaled-web rpc ";
  const RPC_TIMEOUT_MS = 60000;
  const MAX_COMMAND_LENGTH = 2048;
  const MAX_OUTPUT_LENGTH = 100000;
  const SETTING_KEYS = ["acceptRoutes", "shieldsUp", "exitNodeAllowLanAccess"];
  const STATES = {
    Running: { title: "已连接", description: "Tailscale 已连接到你的 tailnet。", tone: "success" },
    Stopped: { title: "已断开", description: "后台服务已运行，Tailscale 当前未连接。", tone: "neutral" },
    NeedsLogin: { title: "需要登录", description: "此设备需要完成账号授权。", tone: "warning" },
    NeedsMachineAuth: { title: "等待批准", description: "请在 Tailscale 管理后台批准此设备。", tone: "warning" },
    ServiceStopped: { title: "服务未启动", description: "Tailscale 的后台服务当前未运行。", tone: "neutral" },
    ServiceError: { title: "后台未响应", description: "无法读取后台状态，请稍后刷新或查看模块日志。", tone: "warning" },
    Starting: { title: "正在连接", description: "Tailscale 正在建立连接，请稍候。", tone: "neutral" },
    NoState: { title: "尚未就绪", description: "等待后台服务提供连接状态。", tone: "neutral" },
  };
  const $ = (id) => document.getElementById(id);
  const ui = {
    main: $("main"),
    notice: $("notice"),
    preview: $("preview-notice"),
    card: $("state-card"),
    title: $("state-title"),
    description: $("state-description"),
    version: $("version"),
    service: $("service-status"),
    freshness: $("freshness"),
    addresses: $("addresses"),
    connect: $("connect"),
    connectLabel: $("connect-label"),
    disconnect: $("disconnect"),
    settingsHint: $("settings-hint"),
    save: $("save-settings"),
    exitLan: $("exit-lan"),
    exitLanDescription: $("exit-lan-description"),
    loginCard: $("login-card"),
    loginURL: $("login-url"),
    command: $("command-input"),
    output: $("command-output"),
    exit: $("command-exit"),
    run: $("run-command"),
  };
  const state = {
    available: trustedEnvironment(),
    busy: false,
    busyAction: "",
    status: null,
    settings: null,
    draft: null,
    dirtyFields: new Set(),
    noticeSource: "",
  };
  let callbackSequence = 0;

  function trustedEnvironment() {
    try {
      return window.self === window.top &&
        window.location.origin === "https://mui.kernelsu.org" &&
        (window.location.pathname === "/index.html" || window.location.pathname === "/") &&
        typeof window.ksu !== "undefined" && typeof window.ksu.exec === "function";
    } catch (_) {
      return false;
    }
  }

  function message(text, kind = "info", source = "operation") {
    ui.notice.textContent = text;
    ui.notice.dataset.kind = kind;
    ui.notice.hidden = !text;
    ui.notice.setAttribute("role", kind === "error" ? "alert" : "status");
    state.noticeSource = source;
  }

  function errorText(error) {
    return error instanceof Error ? error.message : "发生未知错误，请刷新状态后重试。";
  }

  function pendingSettings() {
    const patch = {};
    if (!state.draft) return patch;
    for (const key of SETTING_KEYS) {
      if (!state.dirtyFields.has(key)) continue;
      if (key === "exitNodeAllowLanAccess" && (!state.status || state.status.exitNodeConfigured !== true)) continue;
      patch[key] = state.draft[key];
    }
    return patch;
  }

  function updateControls() {
    const disabled = !state.available || state.busy;
    document.querySelectorAll("[data-requires-bridge]").forEach((control) => {
      control.disabled = disabled;
    });
    const current = state.status && state.status.state;
    ui.connect.disabled = disabled || current === "Running" || current === "Starting";
    ui.disconnect.disabled = disabled || !state.status || current === "Stopped" || current === "ServiceStopped";
    document.querySelectorAll("[data-setting]").forEach((control) => {
      control.disabled = disabled || !state.settings;
    });
    const exitNodeConfigured = state.status && state.status.exitNodeConfigured === true;
    ui.exitLan.disabled = disabled || !state.settings || !exitNodeConfigured;
    ui.exitLanDescription.textContent = !state.status ? "选择退出节点后可配置局域网访问。" :
      exitNodeConfigured ? "使用退出节点时，仍可访问当前局域网。" :
        "请先在命令页选择退出节点，再启用局域网访问。";
    ui.save.disabled = disabled || !state.settings || Object.keys(pendingSettings()).length === 0;
    ui.save.textContent = state.busyAction === "settings" ? "正在保存…" : "保存设置";
    ui.run.textContent = state.busyAction === "command" ? "执行中…" : "运行命令";
    ui.connectLabel.textContent = state.busyAction === "up" ? "正在连接…" : "连接 Tailscale";
    ui.connect.classList.toggle("is-connecting", state.busyAction === "up");
    ui.main.setAttribute("aria-busy", state.busy ? "true" : "false");
    ui.settingsHint.textContent = !state.settings ? "读取当前设置后可修改。" :
      state.dirtyFields.size > 0 ? "有未保存的设置" : "已与设备设置同步";
  }

  function encodeRequest(payload) {
    const bytes = new TextEncoder().encode(JSON.stringify(payload));
    let binary = "";
    for (const byte of bytes) binary += String.fromCharCode(byte);
    const encoded = btoa(binary);
    if (!/^[A-Za-z0-9+/]+={0,2}$/.test(encoded)) throw new Error("无法编码本地请求。");
    return encoded;
  }

  function rpc(payload) {
    if (!state.available || !trustedEnvironment()) {
      return Promise.reject(new Error("当前为只读预览，请从 KernelSU 模块页面打开设置。"));
    }
    // Only the constant executable, its constant 'rpc' verb and base64 are
    // passed to the root shell. User text is data parsed by the native bridge.
    const command = RPC_COMMAND + encodeRequest(payload);
    const callbackName = "shellscaled_rpc_" + Date.now() + "_" + (++callbackSequence);
    return new Promise((resolve, reject) => {
      const cleanup = () => {
        clearTimeout(timer);
        delete window[callbackName];
      };
      const timer = setTimeout(() => {
        cleanup();
        reject(new Error("设备响应超时。操作可能已执行，请先刷新状态；请求不会自动重试。"));
      }, RPC_TIMEOUT_MS);
      window[callbackName] = (errno, stdout, stderr) => {
        cleanup();
        try {
          if (typeof stdout !== "string" || stdout.length > 1500000) {
            throw new Error("设备返回了无法识别的响应。");
          }
          let result;
          try {
            result = JSON.parse(stdout.trim());
          } catch (_) {
            throw new Error(Number(errno) !== 0 ? "本地控制程序未能运行，请检查模块安装。" : "设备响应格式无效，请检查模块版本。");
          }
          if (!result || typeof result !== "object" || Array.isArray(result) || typeof result.ok !== "boolean") {
            throw new Error("设备响应格式无效，请检查模块版本。");
          }
          if (Number(errno) !== 0 && result.ok) {
            throw new Error("本地控制程序异常退出，请刷新状态后重试。");
          }
          resolve(result);
        } catch (error) {
          reject(error);
        }
      };
      try {
        window.ksu.exec(command, "{}", callbackName);
      } catch (_) {
        cleanup();
        reject(new Error("无法调用 KernelSU 本地接口，请重新打开模块设置。"));
      }
    });
  }

  function requireSuccess(result) {
    if (!result.ok) throw new Error(typeof result.error === "string" ? result.error.slice(0, 2000) : "设备未能完成操作，请刷新状态后检查。");
    return result;
  }

  function validSettings(value) {
    return value && typeof value === "object" && SETTING_KEYS.every((key) => typeof value[key] === "boolean");
  }

  function syncDraft() {
    if (!state.draft) return;
    document.querySelectorAll("[data-setting]").forEach((control) => {
      control.checked = state.draft[control.dataset.setting];
    });
  }

  function safeLoginURL(value) {
    if (typeof value !== "string" || value.length > 2048) return "";
    try {
      const url = new URL(value);
      return url.protocol === "https:" && !url.username && !url.password ? value : "";
    } catch (_) {
      return "";
    }
  }

  function renderStatus(result, resetDraft = false) {
    if (typeof result.state !== "string" || typeof result.daemonRunning !== "boolean") {
      throw new Error("设备没有返回完整的状态，请刷新后重试。");
    }
    state.status = result;
    const view = STATES[result.state] || { title: "等待连接", description: "后台服务正在准备，请稍后刷新状态。", tone: "neutral" };
    ui.title.textContent = view.title;
    ui.description.textContent = view.description;
    ui.card.dataset.tone = view.tone;
    ui.version.textContent = typeof result.version === "string" ? "v" + result.version.replace(/^v/, "").slice(0, 30) : "—";
    ui.service.textContent = "后台服务 · " + (result.daemonRunning ? "运行中" : "已停止");
    ui.freshness.textContent = "更新于 " + new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false }).format(new Date());
    ui.addresses.replaceChildren();
    const ips = Array.isArray(result.ips) ? result.ips.filter((ip) => typeof ip === "string" && ip.length <= 80).slice(0, 4) : [];
    if (ips.length) {
      for (const ip of ips) {
        const address = document.createElement("span");
        address.className = "address";
        address.textContent = ip;
        ui.addresses.appendChild(address);
      }
    } else {
      const empty = document.createElement("p");
      empty.className = "empty-copy";
      empty.textContent = "当前没有 Tailscale IP。";
      ui.addresses.appendChild(empty);
    }
    const loginURL = safeLoginURL(result.loginURL);
    ui.loginURL.value = loginURL;
    ui.loginCard.hidden = !loginURL;
    if (result.daemonRunning && validSettings(result.settings)) {
      state.settings = Object.fromEntries(SETTING_KEYS.map((key) => [key, result.settings[key]]));
      if (!state.draft || resetDraft) {
        state.draft = { ...state.settings };
        state.dirtyFields.clear();
      } else {
        // Preserve only fields explicitly edited in this page. An external
        // command or another client may have changed every other preference.
        for (const key of SETTING_KEYS) {
          if (!state.dirtyFields.has(key)) state.draft[key] = state.settings[key];
          else if (state.draft[key] === state.settings[key]) state.dirtyFields.delete(key);
        }
      }
      syncDraft();
    } else {
      state.settings = null;
    }
    updateControls();
  }

  async function refresh(manual = false) {
    if (state.busy || !state.available) return;
    state.busy = true;
    updateControls();
    try {
      renderStatus(requireSuccess(await rpc({ action: "status" })));
      if (manual) message("状态已更新。", "info", "status");
      else if (state.noticeSource === "status") message("", "info", "status");
    } catch (error) {
      ui.freshness.textContent = "状态可能已过期";
      message(errorText(error), "error", "status");
    } finally {
      state.busy = false;
      updateControls();
    }
  }

  async function change(action, extra = {}) {
    if (state.busy || !state.available) return;
    state.busy = true;
    state.busyAction = action;
    message("");
    updateControls();
    try {
      const result = requireSuccess(await rpc({ action, ...extra }));
      const status = typeof result.state === "string" ? result : requireSuccess(await rpc({ action: "status" }));
      renderStatus(status, action === "settings");
      if (action === "settings") message("设置已保存。", "info");
      else if (action === "down") message("Tailscale 已断开。", "info");
      else if (status.state === "NeedsLogin") message("需要完成账号授权，请按页面提示登录。", "warning");
      else message(status.state === "Running" ? "Tailscale 已连接。" : "连接请求已提交，请稍候刷新状态。", "info");
    } catch (error) {
      message(errorText(error), "error");
      ui.freshness.textContent = "请刷新确认设备状态";
    } finally {
      state.busy = false;
      state.busyAction = "";
      updateControls();
    }
  }

  function clippedOutput(value) {
    if (typeof value !== "string") return "";
    if (value.length <= MAX_OUTPUT_LENGTH) return value;
    return value.slice(0, MAX_OUTPUT_LENGTH) + "\n\n[页面仅显示前 " + MAX_OUTPUT_LENGTH + " 个字符]";
  }

  async function runCommand(event) {
    event.preventDefault();
    if (state.busy || !state.available) return;
    const command = ui.command.value.trim();
    if (!command || command.length > MAX_COMMAND_LENGTH || !/^(tailscale|tailscaled)(?:\s|$)/.test(command)) {
      message("请输入以 tailscale 或 tailscaled 开头的命令。", "error");
      ui.command.focus();
      return;
    }
    // This is a usability check, not the permission boundary. The native
    // executable independently tokenizes and validates every command/flag.
    state.busy = true;
    state.busyAction = "command";
    ui.output.textContent = "正在执行…";
    ui.exit.hidden = true;
    message("");
    updateControls();
    try {
      const result = await rpc({ action: "command", command });
      const stdout = clippedOutput(result.stdout);
      const stderr = clippedOutput(result.stderr);
      const chunks = [];
      if (stdout) chunks.push(stdout);
      if (stderr) chunks.push((stdout ? "\n标准错误：\n" : "") + stderr);
      if (typeof result.error === "string" && result.error) chunks.push("\n" + result.error.slice(0, 2000));
      if (result.truncated) chunks.push("\n输出达到限额，已截断。");
      ui.output.textContent = chunks.join("\n").trim() || (result.ok ? "命令已完成，没有输出。" : "命令未完成。");
      const exitCode = Number.isInteger(result.exitCode) ? result.exitCode : null;
      ui.exit.textContent = exitCode === null ? (result.ok ? "完成" : "被拒绝") : "退出码 " + exitCode;
      ui.exit.dataset.tone = result.ok && (exitCode === null || exitCode === 0) ? "success" : "error";
      ui.exit.hidden = false;
      if (!result.ok) message("命令未完成，请查看执行结果。", "error");
      try {
        renderStatus(requireSuccess(await rpc({ action: "status" })));
      } catch (_) {
        ui.freshness.textContent = "请刷新确认设备状态";
      }
    } catch (error) {
      ui.output.textContent = errorText(error);
      message("未获得命令结果，请刷新状态后检查。", "error");
    } finally {
      state.busy = false;
      state.busyAction = "";
      updateControls();
    }
  }

  function selectTab(name, focus = false) {
    for (const tab of document.querySelectorAll("[data-tab]")) {
      const selected = tab.dataset.tab === name;
      tab.classList.toggle("active", selected);
      tab.setAttribute("aria-selected", selected ? "true" : "false");
      tab.tabIndex = selected ? 0 : -1;
      $("panel-" + tab.dataset.tab).hidden = !selected;
      if (selected && focus) tab.focus();
    }
  }

  document.querySelectorAll("[data-tab]").forEach((tab) => {
    tab.addEventListener("click", () => selectTab(tab.dataset.tab));
    tab.addEventListener("keydown", (event) => {
      if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
      event.preventDefault();
      const names = ["overview", "settings", "command"];
      let next = names.indexOf(tab.dataset.tab);
      if (event.key === "ArrowLeft") next = (next + 2) % 3;
      if (event.key === "ArrowRight") next = (next + 1) % 3;
      if (event.key === "Home") next = 0;
      if (event.key === "End") next = 2;
      selectTab(names[next], true);
    });
  });
  $("refresh").addEventListener("click", () => refresh(true));
  ui.connect.addEventListener("click", () => change("up"));
  ui.disconnect.addEventListener("click", () => change("down"));
  ui.save.addEventListener("click", () => {
    const settings = pendingSettings();
    if (Object.keys(settings).length === 0) return;
    change("settings", { settings });
  });
  document.querySelectorAll("[data-setting]").forEach((control) => {
    control.addEventListener("change", () => {
      if (!state.draft || !state.settings) return;
      const key = control.dataset.setting;
      state.draft[key] = control.checked;
      if (state.draft[key] === state.settings[key]) state.dirtyFields.delete(key);
      else state.dirtyFields.add(key);
      updateControls();
    });
  });
  document.querySelectorAll("[data-example]").forEach((button) => {
    button.addEventListener("click", () => {
      ui.command.value = button.dataset.example;
      ui.command.focus();
    });
  });
  $("command-form").addEventListener("submit", runCommand);
  $("clear-command").addEventListener("click", () => {
    if (state.busyAction === "command") return;
    ui.command.value = "";
    ui.output.textContent = "等待执行命令。";
    ui.exit.hidden = true;
  });
  $("copy-login").addEventListener("click", async () => {
    const value = safeLoginURL(ui.loginURL.value);
    if (!value) return;
    try {
      await navigator.clipboard.writeText(value);
      message("授权链接已复制，请在外部浏览器中打开。", "info");
    } catch (_) {
      ui.loginURL.focus();
      ui.loginURL.select();
      message("请长按已选中的授权链接，手动复制。", "warning");
    }
  });

  // No navigation is required by this UI. In particular, never open an auth
  // link or command output inside a WebView that has the root bridge attached.
  document.addEventListener("click", (event) => {
    if (event.target instanceof Element && event.target.closest("a")) event.preventDefault();
  }, true);
  document.addEventListener("submit", (event) => event.preventDefault());
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") refresh(false);
  });
  window.addEventListener("pagehide", () => {
    ui.loginURL.value = "";
    ui.command.value = "";
    ui.output.textContent = "等待执行命令。";
  });

  ui.preview.hidden = state.available;
  updateControls();
  if (state.available) {
    ui.description.textContent = "正在读取本机 Tailscale 状态。";
    refresh(false);
    setInterval(() => {
      if (document.visibilityState === "visible") refresh(false);
    }, 15000);
  }
})();
