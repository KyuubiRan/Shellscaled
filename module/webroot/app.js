/* Implements KernelSU's documented exec(command, options, callback) API.
 * https://github.com/tiann/KernelSU/tree/main/js
 * No KernelSU SDK code is vendored.
 */
"use strict";

(() => {
  const I18n = window.ShellscaledI18n;
  const t = I18n.t;
  const RPC_COMMAND = "/data/adb/modules/shellscaled/bin/shellscaled-web rpc ";
  const RPC_TIMEOUT_MS = 60000;
  const MAX_COMMAND_LENGTH = 2048;
  const MAX_OUTPUT_LENGTH = 100000;
  const SETTING_KEYS = ["acceptRoutes", "shieldsUp", "exitNodeAllowLanAccess"];
  const STATE_TONES = {
    Running: "success", Stopped: "neutral", NeedsLogin: "warning",
    NeedsMachineAuth: "warning", ServiceStopped: "neutral", ServiceError: "warning",
    Starting: "neutral", NoState: "neutral",
  };
  const $ = (id) => document.getElementById(id);
  const ui = {
    main: $("main"), notice: $("notice"), preview: $("preview-notice"),
    card: $("state-card"), title: $("state-title"), description: $("state-description"),
    version: $("version"), service: $("service-status"), freshness: $("freshness"),
    addresses: $("addresses"), connection: $("toggle-connection"), connectionLabel: $("connection-label"),
    settingsHint: $("settings-hint"), save: $("save-settings"), exitLan: $("exit-lan"),
    exitLanDescription: $("exit-lan-description"), language: $("language"),
    loginCard: $("login-card"), loginURL: $("login-url"), command: $("command-input"),
    output: $("command-output"), exit: $("command-exit"), run: $("run-command"),
  };
  const state = {
    available: trustedEnvironment(), busy: false, busyAction: "", refreshAfterBusy: false,
    status: null, settings: null, draft: null, dirtyFields: new Set(),
    lastUpdated: null, freshness: "waiting", notice: null, output: { kind: "idle" },
  };
  let callbackSequence = 0;

  class UIError extends Error {
    constructor(key, values = {}) {
      super(t(key, values));
      this.key = key;
      this.values = values;
    }
  }

  class NativeError extends Error {}

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

  function errorView(error) {
    if (error instanceof UIError) return { key: error.key, values: error.values };
    if (error instanceof NativeError) return { raw: error.message, locale: I18n.locale() };
    return { key: "error.unknown" };
  }

  function viewText(view) {
    return view.raw !== undefined ? (view.locale === I18n.locale() ? view.raw : t("error.operation")) : t(view.key, view.values);
  }

  function renderNotice() {
    const notice = state.notice;
    ui.notice.textContent = notice ? viewText(notice) : "";
    ui.notice.dataset.kind = notice ? notice.kind : "info";
    ui.notice.hidden = !notice;
    ui.notice.setAttribute("role", notice && notice.kind === "error" ? "alert" : "status");
  }

  function message(key = "", kind = "info", source = "operation", values = {}) {
    state.notice = key ? { key, values, kind, source } : null;
    renderNotice();
  }

  function showError(error, source = "operation") {
    state.notice = { ...errorView(error), kind: "error", source };
    renderNotice();
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
    const connected = current === "Running";
    const connecting = state.busyAction === "up" || current === "Starting";
    ui.connection.disabled = disabled || !state.status;
    ui.connection.classList.toggle("primary", !connected);
    ui.connection.classList.toggle("secondary", connected);
    ui.connection.classList.toggle("is-working", connecting || state.busyAction === "down");
    ui.connectionLabel.textContent = t(state.busyAction === "down" ? "connection.disconnecting" :
      state.busyAction === "up" ? "connection.connecting" : current === "Starting" ? "connection.cancel" :
        connected ? "connection.disconnect" : "connection.connect");
    document.querySelectorAll("[data-setting]").forEach((control) => {
      control.disabled = disabled || !state.settings;
    });
    const exitNodeConfigured = state.status && state.status.exitNodeConfigured === true;
    ui.exitLan.disabled = disabled || !state.settings || !exitNodeConfigured;
    ui.exitLanDescription.textContent = t(!state.status ? "settings.exitLanWaiting" :
      exitNodeConfigured ? "settings.exitLanDescription" : "settings.exitLanUnavailable");
    ui.save.disabled = disabled || !state.settings || Object.keys(pendingSettings()).length === 0;
    ui.save.textContent = t(state.busyAction === "settings" ? "settings.saving" : "settings.save");
    ui.run.textContent = t(state.busyAction === "command" ? "command.running" : "command.run");
    ui.language.disabled = state.busy;
    ui.language.value = I18n.preference();
    ui.main.setAttribute("aria-busy", state.busy ? "true" : "false");
    ui.settingsHint.textContent = t(!state.settings ? "settings.unread" :
      state.dirtyFields.size > 0 ? "settings.unsaved" : "settings.saved");
    if (!state.status) ui.description.textContent = state.busy ? t("state.reading") : "";
  }

  function encodeRequest(payload) {
    const bytes = new TextEncoder().encode(JSON.stringify(payload));
    let binary = "";
    for (const byte of bytes) binary += String.fromCharCode(byte);
    const encoded = btoa(binary);
    if (!/^[A-Za-z0-9+/]+={0,2}$/.test(encoded)) throw new UIError("error.encode");
    return encoded;
  }

  function rpc(payload) {
    if (!state.available || !trustedEnvironment()) return Promise.reject(new UIError("error.preview"));
    // User text stays inside JSON data; only base64 enters this fixed command.
    // Keep 'system' as a preference so the native module card also follows Android.
    const command = RPC_COMMAND + encodeRequest({ ...payload, locale: I18n.preference() });
    const callbackName = "shellscaled_rpc_" + Date.now() + "_" + (++callbackSequence);
    return new Promise((resolve, reject) => {
      const cleanup = () => {
        clearTimeout(timer);
        delete window[callbackName];
      };
      const timer = setTimeout(() => {
        cleanup();
        // A timed-out mutation may already have run. Never automatically resend it.
        reject(new UIError("error.timeout"));
      }, RPC_TIMEOUT_MS);
      window[callbackName] = (errno, stdout, stderr) => {
        cleanup();
        try {
          if (typeof stdout !== "string" || stdout.length > 1500000) throw new UIError("error.response");
          let result;
          try {
            result = JSON.parse(stdout.trim());
          } catch (_) {
            throw new UIError(Number(errno) !== 0 ? "error.unavailable" : "error.response");
          }
          if (!result || typeof result !== "object" || Array.isArray(result) || typeof result.ok !== "boolean") {
            throw new UIError("error.response");
          }
          if (Number(errno) !== 0 && result.ok) throw new UIError("error.unexpectedExit");
          resolve(result);
        } catch (error) {
          reject(error);
        }
      };
      try {
        window.ksu.exec(command, "{}", callbackName);
      } catch (_) {
        cleanup();
        reject(new UIError("error.bridge"));
      }
    });
  }

  function requireSuccess(result) {
    if (!result.ok) {
      if (typeof result.error === "string" && result.error) throw new NativeError(result.error.slice(0, 2000));
      throw new UIError("error.operation");
    }
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

  function renderFreshness() {
    const values = state.lastUpdated ? {
      time: new Intl.DateTimeFormat(I18n.locale(), { hour: "2-digit", minute: "2-digit" }).format(state.lastUpdated),
    } : {};
    ui.freshness.textContent = t("freshness." + state.freshness, values);
  }

  function renderPresentation() {
    const result = state.status;
    if (result) {
      const name = Object.prototype.hasOwnProperty.call(STATE_TONES, result.state) ? result.state : "NoState";
      ui.title.textContent = t("state." + name + ".title");
      ui.description.textContent = t("state." + name + ".description");
      ui.card.dataset.tone = STATE_TONES[name];
      ui.version.textContent = typeof result.version === "string" ? "v" + result.version.replace(/^v/, "").slice(0, 30) : "—";
      ui.service.textContent = t(result.daemonRunning ? "service.running" : "service.stopped");
    } else {
      ui.title.textContent = t("state.unread");
      ui.description.textContent = state.busy ? t("state.reading") : "";
      ui.service.textContent = t("service.unread");
    }
    renderFreshness();
    ui.addresses.replaceChildren();
    const ips = result && Array.isArray(result.ips) ?
      result.ips.filter((ip) => typeof ip === "string" && ip.length <= 80).slice(0, 4) : [];
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
      empty.textContent = t(result ? "overview.addressEmpty" : "overview.addressWaiting");
      ui.addresses.appendChild(empty);
    }
    const loginURL = safeLoginURL(result && result.loginURL);
    ui.loginURL.value = loginURL;
    ui.loginCard.hidden = !loginURL;
  }

  function renderStatus(result, resetDraft = false) {
    if (typeof result.state !== "string" || typeof result.daemonRunning !== "boolean") throw new UIError("error.status");
    state.status = result;
    state.lastUpdated = new Date();
    state.freshness = "updated";
    if (validSettings(result.settings)) {
      state.settings = Object.fromEntries(SETTING_KEYS.map((key) => [key, result.settings[key]]));
      if (!state.draft || resetDraft) {
        state.draft = { ...state.settings };
        state.dirtyFields.clear();
      } else {
        // Synchronize untouched fields without overwriting the user's edits.
        for (const key of SETTING_KEYS) {
          if (!state.dirtyFields.has(key)) state.draft[key] = state.settings[key];
          else if (state.draft[key] === state.settings[key]) state.dirtyFields.delete(key);
        }
      }
      syncDraft();
    }
    renderPresentation();
    updateControls();
  }

  function markFreshness(kind) {
    state.freshness = kind;
    renderFreshness();
  }

  function finishBusy() {
    state.busy = false;
    state.busyAction = "";
    updateControls();
    if (state.refreshAfterBusy) {
      state.refreshAfterBusy = false;
      refresh(false);
    }
  }

  async function refresh(manual = false) {
    if (state.busy || !state.available) return;
    state.busy = true;
    state.busyAction = "status";
    updateControls();
    try {
      renderStatus(requireSuccess(await rpc({ action: "status" })));
      if (manual) message("notice.updated", "info", "status");
      else if (state.notice && state.notice.source === "status") message("");
    } catch (error) {
      markFreshness("stale");
      showError(error, "status");
    } finally {
      finishBusy();
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
      if (action === "settings") message("notice.saved");
    } catch (error) {
      showError(error);
      markFreshness("verify");
    } finally {
      finishBusy();
    }
  }

  function clippedOutput(value) {
    if (typeof value !== "string") return "";
    if (value.length <= MAX_OUTPUT_LENGTH) return value;
    const count = new Intl.NumberFormat(I18n.locale()).format(MAX_OUTPUT_LENGTH);
    return value.slice(0, MAX_OUTPUT_LENGTH) + "\n\n" + t("command.clipped", { count });
  }

  function renderOutput() {
    const output = state.output;
    ui.exit.hidden = output.kind !== "result";
    if (output.kind === "idle") {
      ui.output.textContent = t("command.waiting");
    } else if (output.kind === "running") {
      ui.output.textContent = t("command.running");
    } else if (output.kind === "error") {
      ui.output.textContent = viewText(output.error);
    } else {
      const result = output.result;
      const stdout = clippedOutput(result.stdout);
      const stderr = clippedOutput(result.stderr);
      const chunks = [];
      if (stdout) chunks.push(stdout);
      if (stderr) chunks.push((stdout ? "\n" + t("command.errorOutput") + "\n" : "") + stderr);
      if (typeof result.error === "string" && result.error) {
        chunks.push("\n" + (output.locale === I18n.locale() ? result.error.slice(0, 2000) : t("error.operation")));
      }
      if (result.truncated) chunks.push("\n" + t("command.truncated"));
      ui.output.textContent = chunks.join("\n").trim() || t(result.ok ? "command.empty" : "command.incomplete");
      const exitCode = Number.isInteger(result.exitCode) ? result.exitCode : null;
      ui.exit.textContent = exitCode === null ? t(result.ok ? "command.complete" : "command.rejected") : t("command.exitCode", { code: exitCode });
      ui.exit.dataset.tone = result.ok && (exitCode === null || exitCode === 0) ? "success" : "error";
    }
  }

  async function runCommand(event) {
    event.preventDefault();
    if (state.busy || !state.available) return;
    const command = ui.command.value.trim();
    if (!command || command.length > MAX_COMMAND_LENGTH || !/^(tailscale|tailscaled)(?:\s|$)/.test(command)) {
      message("error.command", "error");
      ui.command.focus();
      return;
    }
    // Native validation is the permission boundary; this is only an input hint.
    state.busy = true;
    state.busyAction = "command";
    state.output = { kind: "running" };
    renderOutput();
    message("");
    updateControls();
    try {
      const result = await rpc({ action: "command", command });
      state.output = { kind: "result", result, locale: I18n.locale() };
      renderOutput();
      if (!result.ok) message("notice.commandFailed", "error");
      try {
        renderStatus(requireSuccess(await rpc({ action: "status" })));
      } catch (_) {
        markFreshness("verify");
      }
    } catch (error) {
      state.output = { kind: "error", error: errorView(error) };
      renderOutput();
      message("notice.commandUnavailable", "error");
    } finally {
      finishBusy();
    }
  }

  function applyLanguage() {
    I18n.applyDocument();
    // A previous native error belongs to its original language; request a fresh
    // status instead of trying to translate arbitrary device text in JavaScript.
    if (state.notice && state.notice.raw !== undefined) state.notice = null;
    renderPresentation();
    renderNotice();
    renderOutput();
    syncDraft();
    updateControls();
    if (state.busy) state.refreshAfterBusy = true;
    else refresh(false);
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
  ui.connection.addEventListener("click", () => {
    if (state.status) change(["Running", "Starting"].includes(state.status.state) ? "down" : "up");
  });
  ui.save.addEventListener("click", () => {
    const settings = pendingSettings();
    if (Object.keys(settings).length) change("settings", { settings });
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
  ui.language.addEventListener("change", () => {
    if (state.busy) {
      ui.language.value = I18n.preference();
      return;
    }
    I18n.setPreference(ui.language.value);
    applyLanguage();
  });
  window.addEventListener("languagechange", () => {
    if (I18n.preference() === "system") applyLanguage();
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
    state.output = { kind: "idle" };
    renderOutput();
  });
  $("copy-login").addEventListener("click", async () => {
    const value = safeLoginURL(ui.loginURL.value);
    if (!value) return;
    try {
      await navigator.clipboard.writeText(value);
      message("notice.linkCopied");
    } catch (_) {
      ui.loginURL.focus();
      ui.loginURL.select();
      message("notice.copyManually", "warning");
    }
  });

  // Keep auth links and arbitrary output out of navigation in this root WebView.
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
    state.output = { kind: "idle" };
    renderOutput();
  });

  ui.preview.hidden = state.available;
  I18n.applyDocument();
  renderPresentation();
  renderOutput();
  updateControls();
  if (state.available) {
    refresh(false);
    setInterval(() => {
      if (document.visibilityState === "visible") refresh(false);
    }, 15000);
  }
})();
