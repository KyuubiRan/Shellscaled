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
  const BINARY_UPDATE_TIMEOUT_MS = 200000;
  const MAX_COMMAND_LENGTH = 2048;
  const MAX_OUTPUT_LENGTH = 100000;
  const MAX_DEVICES = 500;
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
    loginCard: $("login-card"), loginURL: $("login-url"), login: $("login-tailscale"),
    loginLabel: $("login-label"), loginMessage: $("login-message"), copyLogin: $("copy-login"), command: $("command-input"),
    output: $("command-output"), exit: $("command-exit"), run: $("run-command"),
    console: $("open-console"), consoleLabel: $("console-label"), consoleMessage: $("console-message"),
    about: $("open-about"), aboutDescription: $("about-description"), aboutMessage: $("about-message"),
    deviceSearch: $("device-search"), deviceList: $("device-list"), deviceMessage: $("devices-message"),
    deviceSummary: $("devices-summary"), deviceUpdated: $("devices-updated"),
    binaryCurrent: $("binary-current-version"), binaryLatestRow: $("binary-latest-row"),
    binaryLatest: $("binary-latest-version"), binaryCheck: $("binary-check"), binaryUpdate: $("binary-update"),
    binaryCheckLabel: $("binary-check-label"), binaryUpdateLabel: $("binary-update-label"),
    binaryMessage: $("binary-message"),
    binaryChannel: $("binary-channel"),
  };
  const state = {
    available: trustedEnvironment(), busy: false, busyAction: "", refreshAfterBusy: false,
    status: null, settings: null, draft: null, dirtyFields: new Set(),
    lastUpdated: null, freshness: "waiting", notice: null, output: { kind: "idle" },
    activeTab: "overview", pageActive: true, devices: null, devicesError: null,
    deviceRefreshPending: false, deviceRequestVersion: 0, deviceRenderSignature: null,
    addressSignature: null, consoleError: null,
    aboutError: null, aboutRequestVersion: 0,
    loginError: null, loginOpened: false, loginRequestVersion: 0,
    binary: null, binaryFeedback: "", binaryError: null, binaryRequestVersion: 0,
    updateChannel: null, binaryChannelPending: null,
  };
  let callbackSequence = 0;
  const copyTimers = new Map();
  const copyOperations = new Map();
  let copyEpoch = 0;
  let copySequence = 0;

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

  function updateCopyFeedback(control) {
    const value = control.dataset.copy || "";
    const kind = control.dataset.copyState;
    const key = kind === "copied" ? "copy.copied" : kind === "failed" ? "copy.failed" :
      control.dataset.copyKind === "login" ? "login.copy" : "copy.action";
    const feedback = control.querySelector(".copy-feedback");
    if (feedback) feedback.textContent = t(key);
    control.setAttribute("aria-label", t(kind === "copied" ? "copy.copiedLabel" :
      kind === "failed" ? "copy.failedLabel" : "copy.label", { value }));
  }

  function releaseCopyOperation(control, operation) {
    if (copyOperations.get(control) !== operation) return false;
    copyOperations.delete(control);
    if (copyTimers.has(control)) {
      clearTimeout(copyTimers.get(control));
      copyTimers.delete(control);
    }
    delete control.dataset.copyPending;
    delete control.dataset.copyState;
    control.disabled = false;
    if (control.isConnected) updateCopyFeedback(control);
    return true;
  }

  function invalidateCopyControl(control) {
    copyOperations.delete(control);
    if (copyTimers.has(control)) {
      clearTimeout(copyTimers.get(control));
      copyTimers.delete(control);
    }
    delete control.dataset.copyPending;
    delete control.dataset.copyState;
    control.disabled = false;
  }

  function clearCopyControls(container = document) {
    const controls = new Set(container.querySelectorAll(".copy-control"));
    if (container === document) {
      for (const control of copyOperations.keys()) controls.add(control);
      for (const control of copyTimers.keys()) controls.add(control);
    }
    for (const control of controls) invalidateCopyControl(control);
  }

  function copyIsCurrent(control, operation) {
    const current = copyOperations.get(control);
    return current === operation && current.token === operation.token && operation.epoch === copyEpoch &&
      state.pageActive && control.isConnected && control.dataset.copy === operation.value;
  }

  function createCopyControl(value, kind, className, fieldLabel = "") {
    const control = document.createElement("button");
    control.type = "button";
    control.className = "copy-control " + className;
    control.dataset.copy = value;
    control.dataset.copyKind = kind;
    if (fieldLabel) {
      const label = document.createElement("span");
      label.className = "copy-field-label";
      label.textContent = fieldLabel;
      control.appendChild(label);
    }
    const text = document.createElement("span");
    text.className = "copy-value";
    text.textContent = value;
    const feedback = document.createElement("span");
    feedback.className = "copy-feedback";
    feedback.setAttribute("aria-live", "polite");
    control.append(text, feedback);
    updateCopyFeedback(control);
    return control;
  }

  function fallbackCopy(value, isCurrent) {
    if (!isCurrent()) return false;
    const previousFocus = document.activeElement;
    const buffer = document.createElement("textarea");
    buffer.className = "clipboard-buffer";
    buffer.value = value;
    buffer.readOnly = true;
    buffer.tabIndex = -1;
    buffer.setAttribute("aria-hidden", "true");
    document.body.appendChild(buffer);
    try {
      buffer.focus({ preventScroll: true });
      buffer.select();
      buffer.setSelectionRange(0, value.length);
      if (!isCurrent()) return false;
      return document.execCommand("copy") === true;
    } catch (_) {
      return false;
    } finally {
      buffer.remove();
      if (isCurrent() && previousFocus && previousFocus.isConnected && typeof previousFocus.focus === "function") {
        try { previousFocus.focus({ preventScroll: true }); } catch (_) { /* Keep focus local. */ }
      }
    }
  }

  async function copyText(value, isCurrent) {
    if (!isCurrent()) return false;
    try {
      if (navigator.clipboard && typeof navigator.clipboard.writeText === "function") {
        await navigator.clipboard.writeText(value);
        return isCurrent();
      }
    } catch (_) {
      // Some WebViews deny the async API while still supporting DOM selection.
    }
    if (!isCurrent()) return false;
    return fallbackCopy(value, isCurrent);
  }

  async function copyFromControl(control) {
    if (!state.pageActive || control.disabled || !control.dataset.copy || control.dataset.copyPending) return;
    if (copyTimers.has(control)) {
      clearTimeout(copyTimers.get(control));
      copyTimers.delete(control);
    }
    const operation = { value: control.dataset.copy, epoch: copyEpoch, token: ++copySequence };
    copyOperations.set(control, operation);
    delete control.dataset.copyState;
    updateCopyFeedback(control);
    control.dataset.copyPending = "true";
    control.disabled = true;
    const copied = await copyText(operation.value, () => copyIsCurrent(control, operation));
    if (!copyIsCurrent(control, operation)) {
      releaseCopyOperation(control, operation);
      return;
    }
    delete control.dataset.copyPending;
    control.disabled = false;
    control.dataset.copyState = copied ? "copied" : "failed";
    updateCopyFeedback(control);
    copyTimers.set(control, setTimeout(() => {
      if (!copyIsCurrent(control, operation)) {
        releaseCopyOperation(control, operation);
        return;
      }
      releaseCopyOperation(control, operation);
    }, copied ? 1800 : 2600));
  }

  function renderConsoleMessage() {
    ui.consoleMessage.textContent = state.consoleError ? viewText(state.consoleError) : "";
    ui.consoleMessage.dataset.kind = "error";
    ui.consoleMessage.hidden = !state.consoleError;
  }

  async function openConsole() {
    if (state.busy || !state.available || !state.pageActive) return;
    state.busy = true;
    state.busyAction = "console";
    state.consoleError = null;
    renderConsoleMessage();
    updateControls();
    try {
      requireSuccess(await rpc({ action: "console" }));
    } catch (error) {
      if (state.pageActive) {
        state.consoleError = errorView(error);
        renderConsoleMessage();
      }
    } finally {
      finishBusy();
    }
  }

  function renderAbout() {
    const opening = state.busyAction === "about";
    ui.aboutDescription.textContent = t(opening ? "about.opening" : "about.description");
    ui.about.classList.toggle("is-working", opening);
    ui.about.setAttribute("aria-busy", opening ? "true" : "false");
    ui.aboutMessage.textContent = state.aboutError ? t("about.failed") + " " + viewText(state.aboutError) : "";
    ui.aboutMessage.hidden = !state.aboutError;
    ui.aboutMessage.dataset.kind = "error";
  }

  async function openAbout() {
    if (state.busy || !state.available || !state.pageActive) return;
    state.busy = true;
    state.busyAction = "about";
    state.aboutError = null;
    const version = ++state.aboutRequestVersion;
    updateControls();
    try {
      requireSuccess(await rpc({ action: "about" }));
    } catch (error) {
      if (state.pageActive && version === state.aboutRequestVersion) state.aboutError = errorView(error);
    } finally {
      finishBusy();
    }
  }

  async function startLogin() {
    if (state.busy || !state.available || !state.pageActive || !state.status || state.status.state !== "NeedsLogin") return;
    state.busy = true;
    state.busyAction = "login";
    state.loginError = null;
    state.loginOpened = false;
    const version = ++state.loginRequestVersion;
    updateControls();
    try {
      const result = requireSuccess(await rpc({ action: "login" }));
      if (!state.pageActive || version !== state.loginRequestVersion) return;
      state.loginOpened = true;
      if (typeof result.state === "string") renderStatus(result);
      try {
        const status = requireSuccess(await rpc({ action: "status" }));
        if (state.pageActive && version === state.loginRequestVersion) renderStatus(status);
      } catch (_) {
        if (state.pageActive && version === state.loginRequestVersion) markFreshness("verify");
      }
    } catch (error) {
      if (state.pageActive && version === state.loginRequestVersion) state.loginError = errorView(error);
    } finally {
      finishBusy();
    }
  }

  function boundedString(value, maximum = 1024) {
    return typeof value === "string" ? value.slice(0, maximum) : "";
  }

  function normalizeDevices(result) {
    if (!Array.isArray(result.devices)) throw new UIError("error.response");
    const items = result.devices.slice(0, MAX_DEVICES).filter((device) =>
      device && typeof device === "object" && !Array.isArray(device)).map((device, index) => ({
      id: boundedString(typeof device.id === "number" ? String(device.id) : device.id, 256) || "device-" + index,
      name: boundedString(device.name),
      dnsName: boundedString(device.dnsName),
      ips: Array.isArray(device.ips) ? [...new Set(device.ips.filter((ip) => typeof ip === "string" && ip.length <= 80))].slice(0, 8) : [],
      online: device.online === true,
      self: device.self === true,
      os: boundedString(device.os, 200),
      lastSeen: typeof device.lastSeen === "number" ? device.lastSeen : boundedString(device.lastSeen, 100),
    }));
    return { items, truncated: result.truncated === true || result.devices.length > MAX_DEVICES, updated: new Date() };
  }

  function deviceLastSeen(value) {
    if (!value) return "";
    const timestamp = typeof value === "number" && value < 1000000000000 ? value * 1000 : value;
    const date = new Date(timestamp);
    if (!Number.isFinite(date.getTime()) || date.getTime() <= 0) return "";
    return new Intl.DateTimeFormat(I18n.locale(), { dateStyle: "medium", timeStyle: "short" }).format(date);
  }

  function createDeviceCard(device) {
    const card = document.createElement("article");
    card.className = "card device-card";
    card.dataset.deviceId = device.id;
    card.setAttribute("role", "listitem");
    const header = document.createElement("div");
    header.className = "device-card-header";
    if (device.name) header.appendChild(createCopyControl(device.name, "name", "device-name"));
    else {
      const title = document.createElement("h3");
      title.textContent = device.dnsName || t("devices.unnamed");
      header.appendChild(title);
    }
    const badges = document.createElement("div");
    badges.className = "device-badges";
    const status = document.createElement("span");
    status.className = "badge device-status";
    status.dataset.online = String(device.online);
    status.textContent = t(device.online ? "devices.online" : "devices.offline");
    badges.appendChild(status);
    if (device.self) {
      const self = document.createElement("span");
      self.className = "badge device-self";
      self.textContent = t("devices.self");
      badges.appendChild(self);
    }
    if (device.os) {
      const os = document.createElement("span");
      os.className = "device-os";
      os.textContent = device.os;
      badges.appendChild(os);
    }
    header.appendChild(badges);
    card.appendChild(header);
    const fields = document.createElement("div");
    fields.className = "device-fields";
    if (device.dnsName) fields.appendChild(createCopyControl(device.dnsName, "dns", "device-field", t("devices.dns")));
    for (const ip of device.ips) {
      const kind = ip.includes(":") ? "ipv6" : "ipv4";
      fields.appendChild(createCopyControl(ip, kind, "device-field", t("devices." + kind)));
    }
    if (!device.ips.length && !device.dnsName) {
      const empty = document.createElement("p");
      empty.className = "empty-copy";
      empty.textContent = t("devices.noAddresses");
      fields.appendChild(empty);
    }
    card.appendChild(fields);
    const seen = !device.online && deviceLastSeen(device.lastSeen);
    if (seen) {
      const lastSeen = document.createElement("p");
      lastSeen.className = "device-last-seen";
      lastSeen.textContent = t("devices.lastSeen", { time: seen });
      card.appendChild(lastSeen);
    }
    return card;
  }

  function renderDevices() {
    const devices = state.devices;
    const query = ui.deviceSearch.value.trim().toLocaleLowerCase(I18n.locale());
    const loading = state.busyAction === "devices" || (state.deviceRefreshPending && state.activeTab === "devices");
    const items = devices ? devices.items.filter((device) => !query ||
      [device.name, device.dnsName, ...device.ips].some((value) => value.toLocaleLowerCase(I18n.locale()).includes(query))) : [];
    items.sort((left, right) => Number(right.self) - Number(left.self) || Number(right.online) - Number(left.online) ||
      (left.name || left.dnsName).localeCompare(right.name || right.dnsName, I18n.locale()));
    const number = (value) => new Intl.NumberFormat(I18n.locale()).format(value);
    ui.deviceSummary.textContent = devices ? t(query ? "devices.filteredSummary" : "devices.summary", {
      count: number(items.length), total: number(devices.items.length),
      online: number(devices.items.filter((device) => device.online).length),
    }) : "";
    ui.deviceUpdated.textContent = devices ? t("freshness.updated", {
      time: new Intl.DateTimeFormat(I18n.locale(), { hour: "2-digit", minute: "2-digit" }).format(devices.updated),
    }) : "";
    const messages = [];
    let kind = "info";
    if (loading) messages.push(t("devices.loading"));
    else if (state.devicesError) {
      messages.push(t("devices.loadFailed"), viewText(state.devicesError));
      if (devices) messages.push(t("devices.previousData"));
      kind = "error";
    } else if (!devices) messages.push(t("devices.notLoaded"));
    else if (!devices.items.length) messages.push(t("devices.empty"));
    else if (!items.length) messages.push(t("devices.noResults"));
    if (devices && devices.truncated) {
      messages.push(t("devices.truncated"));
      if (kind !== "error") kind = "warning";
    }
    ui.deviceMessage.textContent = messages.join(" ");
    ui.deviceMessage.hidden = messages.length === 0;
    ui.deviceMessage.dataset.kind = kind;
    ui.deviceMessage.setAttribute("role", kind === "error" ? "alert" : "status");
    const signature = I18n.locale() + "\n" + query + "\n" + JSON.stringify(items);
    if (signature !== state.deviceRenderSignature) {
      clearCopyControls(ui.deviceList);
      ui.deviceList.replaceChildren();
      for (const device of items) ui.deviceList.appendChild(createDeviceCard(device));
      state.deviceRenderSignature = signature;
    }
  }

  async function loadDevices() {
    if (!state.available || !state.pageActive) return;
    if (state.busyAction === "devices") return;
    if (state.busy) {
      state.deviceRefreshPending = true;
      renderDevices();
      return;
    }
    state.deviceRefreshPending = false;
    state.busy = true;
    state.busyAction = "devices";
    state.devicesError = null;
    const version = ++state.deviceRequestVersion;
    renderDevices();
    updateControls();
    try {
      const result = requireSuccess(await rpc({ action: "devices" }));
      if (version !== state.deviceRequestVersion || !state.pageActive) return;
      state.devices = normalizeDevices(result);
    } catch (error) {
      if (version === state.deviceRequestVersion && state.pageActive) state.devicesError = errorView(error);
    } finally {
      if (state.pageActive) renderDevices();
      finishBusy();
    }
  }

  function validUpdateChannel(channel) {
    return channel === "stable" || channel === "preview";
  }

  function syncUpdateChannel(channel) {
    if (!validUpdateChannel(channel)) return;
    if (state.updateChannel !== channel) {
      state.updateChannel = channel;
      state.binary = null;
      state.binaryFeedback = "";
      state.binaryError = null;
    }
  }

  function normalizeBinary(result) {
    const binary = result.binary;
    if (!binary || typeof binary !== "object" || Array.isArray(binary) ||
      ["updateAvailable", "checked", "supported"].some((key) => typeof binary[key] !== "boolean")) {
      throw new UIError("error.response");
    }
    if (binary.channel !== undefined && !validUpdateChannel(binary.channel)) throw new UIError("error.response");
    return {
      currentVersion: boundedString(binary.currentVersion, 80),
      latestVersion: boundedString(binary.latestVersion, 80),
      updateAvailable: binary.updateAvailable,
      checked: binary.checked,
      supported: binary.supported,
      channel: binary.channel || state.updateChannel,
    };
  }

  function binaryUpdateAvailable() {
    return state.binary && state.binary.checked && state.binary.supported && state.binary.updateAvailable &&
      (!state.binary.channel || state.binary.channel === state.updateChannel);
  }

  function renderBinary() {
    const binary = state.binary;
    const current = (binary && binary.currentVersion) || (state.status && state.status.version);
    const latest = binary && binary.latestVersion;
    ui.binaryCurrent.textContent = typeof current === "string" && current ? "v" + current.replace(/^v/, "") : "—";
    ui.binaryLatest.textContent = latest ? "v" + latest.replace(/^v/, "") : "—";
    ui.binaryLatestRow.hidden = !latest;
    ui.binaryChannel.value = state.binaryChannelPending || state.updateChannel || "";
    ui.binaryChannel.disabled = !state.available || state.busy || !state.updateChannel;
    const checking = state.busyAction === "binary-check";
    const updating = state.busyAction === "binary-update";
    const savingChannel = state.busyAction === "binary-channel";
    ui.binaryCheck.disabled = !state.available || state.busy;
    ui.binaryUpdate.disabled = !state.available || state.busy || !binaryUpdateAvailable();
    ui.binaryCheckLabel.textContent = t(checking ? "binary.checking" : "binary.check");
    ui.binaryUpdateLabel.textContent = t(updating ? "binary.updating" : "binary.update");
    ui.binaryUpdate.classList.toggle("is-working", updating);
    let text = "";
    let kind = "info";
    if (savingChannel) text = t("binary.channelSaving");
    else if (checking) text = t("binary.checking");
    else if (updating) text = t("binary.busy");
    else if (state.binaryError) {
      text = t(state.binaryFeedback === "channelFailed" ? "binary.channelFailed" :
        state.binaryFeedback === "updateFailed" ? "binary.updateFailed" : "binary.checkFailed") + " " + viewText(state.binaryError);
      kind = "error";
    } else if (state.binaryFeedback === "updated") text = t("binary.completed");
    else if (state.binaryFeedback === "channelSaved") text = t("binary.channelSaved");
    else if (binary && binary.checked) {
      if (!binary.supported) {
        text = t("binary.unsupported");
        kind = "warning";
      } else text = t(binary.updateAvailable ? "binary.available" : "binary.currentStatus");
    } else if (state.binaryFeedback === "checked") text = t("binary.notChecked");
    ui.binaryMessage.textContent = text;
    ui.binaryMessage.hidden = !text;
    ui.binaryMessage.dataset.kind = kind;
    ui.binaryMessage.setAttribute("role", kind === "error" ? "alert" : "status");
  }

  async function setBinaryChannel(channel) {
    if (state.busy || !state.available || !state.pageActive || !validUpdateChannel(channel) || !state.updateChannel) {
      ui.binaryChannel.value = state.updateChannel || "";
      return;
    }
    if (channel === state.updateChannel) return;
    state.busy = true;
    state.busyAction = "binary-channel";
    state.binaryChannelPending = channel;
    state.binary = null;
    state.binaryFeedback = "";
    state.binaryError = null;
    const version = ++state.binaryRequestVersion;
    updateControls();
    try {
      const result = requireSuccess(await rpc({ action: "binary-channel", channel }));
      if (!state.pageActive || version !== state.binaryRequestVersion) return;
      if (result.updateChannel !== channel) throw new UIError("error.response");
      syncUpdateChannel(result.updateChannel);
      state.binaryFeedback = "channelSaved";
    } catch (error) {
      if (state.pageActive && version === state.binaryRequestVersion) {
        state.binaryError = errorView(error);
        state.binaryFeedback = "channelFailed";
      }
    } finally {
      if (version === state.binaryRequestVersion) state.binaryChannelPending = null;
      finishBusy();
    }
  }

  async function runBinaryAction(action) {
    if (state.busy || !state.available || !state.pageActive) return;
    if (action === "binary-update" && !binaryUpdateAvailable()) return;
    state.busy = true;
    state.busyAction = action;
    state.binaryError = null;
    state.binaryFeedback = "";
    const version = ++state.binaryRequestVersion;
    updateControls();
    try {
      const result = requireSuccess(await rpc({ action }));
      if (!state.pageActive || version !== state.binaryRequestVersion) return;
      const binary = normalizeBinary(result);
      syncUpdateChannel(binary.channel);
      state.binary = binary;
      state.binaryFeedback = action === "binary-update" ? "updated" : "checked";
      if (action === "binary-update") {
        try {
          const status = requireSuccess(await rpc({ action: "status" }));
          if (state.pageActive && version === state.binaryRequestVersion) renderStatus(status);
        } catch (_) {
          if (state.pageActive) markFreshness("verify");
        }
      }
    } catch (error) {
      if (state.pageActive && version === state.binaryRequestVersion) {
        state.binaryError = errorView(error);
        state.binaryFeedback = action === "binary-update" ? "updateFailed" : "checkFailed";
      }
    } finally {
      finishBusy();
    }
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
    ui.consoleLabel.textContent = t(state.busyAction === "console" ? "console.opening" : "console.open");
    renderAbout();
    renderLogin();
    ui.language.disabled = state.busy;
    ui.language.value = I18n.preference();
    ui.main.setAttribute("aria-busy", state.busy ? "true" : "false");
    ui.settingsHint.textContent = t(!state.settings ? "settings.unread" :
      state.dirtyFields.size > 0 ? "settings.unsaved" : "settings.saved");
    if (!state.status) ui.description.textContent = state.busy ? t("state.reading") : "";
    renderBinary();
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
      }, payload.action === "binary-update" ? BINARY_UPDATE_TIMEOUT_MS : RPC_TIMEOUT_MS);
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

  function renderLogin() {
    const needsLogin = state.status && state.status.state === "NeedsLogin";
    const opening = state.busyAction === "login";
    const loginURL = needsLogin ? safeLoginURL(state.status.loginURL) : "";
    ui.loginCard.hidden = !needsLogin;
    ui.loginURL.value = loginURL;
    if ((ui.copyLogin.dataset.copy || "") !== loginURL) invalidateCopyControl(ui.copyLogin);
    if (loginURL) ui.copyLogin.dataset.copy = loginURL;
    else delete ui.copyLogin.dataset.copy;
    ui.copyLogin.disabled = !needsLogin || !loginURL || !state.available || state.busy || !state.pageActive || Boolean(ui.copyLogin.dataset.copyPending);
    updateCopyFeedback(ui.copyLogin);
    ui.login.disabled = !needsLogin || !state.available || state.busy || !state.pageActive;
    ui.login.classList.toggle("is-working", opening);
    ui.login.setAttribute("aria-busy", opening ? "true" : "false");
    ui.loginLabel.textContent = t(opening ? "login.opening" : "login.action");
    ui.loginMessage.textContent = state.loginError ? t("login.failed") + " " + viewText(state.loginError) :
      state.loginOpened ? t("login.opened") : "";
    ui.loginMessage.hidden = !ui.loginMessage.textContent;
    ui.loginMessage.dataset.kind = state.loginError ? "error" : "info";
    ui.loginMessage.setAttribute("role", state.loginError ? "alert" : "status");
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
    const ips = result && Array.isArray(result.ips) ?
      result.ips.filter((ip) => typeof ip === "string" && ip.length <= 80).slice(0, 4) : [];
    const signature = Boolean(result) + "\n" + ips.join("\n");
    if (signature !== state.addressSignature) {
      clearCopyControls(ui.addresses);
      ui.addresses.replaceChildren();
      if (ips.length) {
        for (const ip of ips) ui.addresses.appendChild(createCopyControl(ip, "ip", "address"));
      } else {
        const empty = document.createElement("p");
        empty.className = "empty-copy";
        ui.addresses.appendChild(empty);
      }
      state.addressSignature = signature;
    }
    const empty = ui.addresses.querySelector(".empty-copy");
    if (empty) {
      empty.textContent = t(result ? "overview.addressEmpty" : "overview.addressWaiting");
    }
    ui.addresses.querySelectorAll(".copy-control").forEach(updateCopyFeedback);
    renderLogin();
  }

  function renderStatus(result, resetDraft = false) {
    if (!state.pageActive) return;
    if (typeof result.state !== "string" || typeof result.daemonRunning !== "boolean") throw new UIError("error.status");
    state.status = result;
    if (result.state !== "NeedsLogin") {
      state.loginError = null;
      state.loginOpened = false;
    }
    syncUpdateChannel(result.updateChannel);
    if (state.binary && typeof result.version === "string" && result.version) {
      state.binary.currentVersion = result.version.slice(0, 80);
    }
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
    const completedAction = state.busyAction;
    state.busy = false;
    state.busyAction = "";
    updateControls();
    if (!state.pageActive) return;
    if (completedAction === "devices") renderDevices();
    if (state.refreshAfterBusy) {
      state.refreshAfterBusy = false;
      refresh(false);
    } else if (state.deviceRefreshPending && state.activeTab === "devices") {
      loadDevices();
    }
  }

  async function refresh(manual = false) {
    if (state.busy || !state.available || !state.pageActive) return;
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
    if (state.busy || !state.available || !state.pageActive) return;
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
    if (state.busy || !state.available || !state.pageActive) return;
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
      if (!state.pageActive) return;
      state.output = { kind: "result", result, locale: I18n.locale() };
      renderOutput();
      if (!result.ok) message("notice.commandFailed", "error");
      try {
        renderStatus(requireSuccess(await rpc({ action: "status" })));
      } catch (_) {
        markFreshness("verify");
      }
    } catch (error) {
      if (!state.pageActive) return;
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
    renderDevices();
    renderConsoleMessage();
    document.querySelectorAll(".copy-control[data-copy]").forEach(updateCopyFeedback);
    syncDraft();
    updateControls();
    if (state.busy) state.refreshAfterBusy = true;
    else refresh(false);
  }

  function selectTab(name, focus = false) {
    state.activeTab = name;
    for (const tab of document.querySelectorAll("[data-tab]")) {
      const selected = tab.dataset.tab === name;
      tab.classList.toggle("active", selected);
      tab.setAttribute("aria-selected", selected ? "true" : "false");
      tab.tabIndex = selected ? 0 : -1;
      $("panel-" + tab.dataset.tab).hidden = !selected;
      if (selected && focus) tab.focus();
    }
    if (name === "devices" && !state.devices && !state.devicesError) loadDevices();
  }

  document.querySelectorAll("[data-tab]").forEach((tab) => {
    tab.addEventListener("click", () => selectTab(tab.dataset.tab));
    tab.addEventListener("keydown", (event) => {
      if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
      event.preventDefault();
      const names = ["overview", "devices", "settings", "command"];
      let next = names.indexOf(tab.dataset.tab);
      if (event.key === "ArrowLeft") next = (next + names.length - 1) % names.length;
      if (event.key === "ArrowRight") next = (next + 1) % names.length;
      if (event.key === "Home") next = 0;
      if (event.key === "End") next = names.length - 1;
      selectTab(names[next], true);
    });
  });
  $("refresh").addEventListener("click", () => refresh(true));
  $("refresh-devices").addEventListener("click", () => loadDevices());
  ui.deviceSearch.addEventListener("input", renderDevices);
  ui.console.addEventListener("click", openConsole);
  ui.about.addEventListener("click", openAbout);
  ui.login.addEventListener("click", startLogin);
  ui.binaryCheck.addEventListener("click", () => runBinaryAction("binary-check"));
  ui.binaryUpdate.addEventListener("click", () => runBinaryAction("binary-update"));
  ui.binaryChannel.addEventListener("change", () => setBinaryChannel(ui.binaryChannel.value));
  document.addEventListener("click", (event) => {
    if (!(event.target instanceof Element)) return;
    const control = event.target.closest("button.copy-control[data-copy]");
    if (control) {
      event.preventDefault();
      copyFromControl(control);
    }
  });
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
  // Keep auth links and arbitrary output out of navigation in this root WebView.
  document.addEventListener("click", (event) => {
    if (event.target instanceof Element && event.target.closest("a")) event.preventDefault();
  }, true);
  document.addEventListener("submit", (event) => event.preventDefault());
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") refresh(false);
  });
  window.addEventListener("pagehide", () => {
    copyEpoch++;
    state.pageActive = false;
    state.deviceRequestVersion++;
    state.deviceRefreshPending = false;
    state.refreshAfterBusy = false;
    state.devices = null;
    state.devicesError = null;
    state.deviceRenderSignature = null;
    state.consoleError = null;
    state.aboutRequestVersion++;
    state.aboutError = null;
    state.loginRequestVersion++;
    state.loginError = null;
    state.loginOpened = false;
    state.binaryRequestVersion++;
    state.binary = null;
    state.binaryFeedback = "";
    state.binaryError = null;
    state.updateChannel = null;
    state.binaryChannelPending = null;
    state.status = null;
    state.lastUpdated = null;
    state.freshness = "waiting";
    state.addressSignature = null;
    state.notice = null;
    clearCopyControls();
    document.querySelectorAll(".copy-control").forEach((control) => {
      delete control.dataset.copy;
      delete control.dataset.copyState;
    });
    ui.loginURL.value = "";
    ui.deviceSearch.value = "";
    ui.command.value = "";
    state.output = { kind: "idle" };
    renderPresentation();
    renderDevices();
    renderConsoleMessage();
    renderAbout();
    renderNotice();
    renderOutput();
    renderBinary();
  });
  window.addEventListener("pageshow", () => {
    if (state.pageActive) return;
    state.pageActive = true;
    if (state.activeTab === "devices") state.deviceRefreshPending = true;
    if (state.busy) state.refreshAfterBusy = true;
    else refresh(false);
  });

  ui.preview.hidden = state.available;
  I18n.applyDocument();
  renderPresentation();
  renderOutput();
  renderDevices();
  renderConsoleMessage();
  updateControls();
  if (state.available) {
    refresh(false);
    setInterval(() => {
      if (document.visibilityState === "visible") refresh(false);
    }, 15000);
  }
})();
