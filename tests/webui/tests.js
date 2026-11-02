/* Executed only inside the fixture's Android WebView, against the real packaged assets. */
"use strict";
(async () => {
  const $ = (id) => document.getElementById(id);
  const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  const requests = () => JSON.parse(fixture.requestsJSON());
  const backend = () => JSON.parse(fixture.statusJSON());
  const assert = (value, label = "assertion") => { if (!value) throw new Error(label); };
  const rawKey = /^(?:app|preview|tab|overview|devices|copy|console|about|binary|state|service|freshness|connection|login|settings|language|command|notice|error|network)\.[A-Za-z0-9.]+$/;
  async function until(predicate, label = "timeout", end = Date.now() + 6000) {
    while (!predicate()) {
      if (Date.now() > end) throw new Error(label);
      await wait(25);
    }
  }
  async function idle(label = "main-idle") {
    const end = Date.now() + 6000;
    const ready = () => $("main") && $("main").getAttribute("aria-busy") === "false";
    await until(ready, label + "-before-paint", end);
    await wait(30);
    // A periodic refresh may start during the paint wait. Recheck before a
    // caller dispatches a change that the production busy guard would reject.
    await until(ready, label + "-after-paint", end);
  }
  async function test(name, body) {
    try { await body(); fixture.report(name, true); }
    catch (error) {
      const lines = [...(error.stack || "").matchAll(/:(\d+):\d+/g)].map((match) => match[1]);
      const line = lines.find((value) => value !== "8");
      fixture.report(name + (line ? " line=" + line : "") + (error.message === "assertion" ? "" : " " + error.message), false);
    }
  }
  function click(id) {
    assert($(id) && !$(id).disabled, "click-ready-" + id);
    $(id).click();
  }
  async function refresh() {
    await idle();
    const before = requests().length;
    click("refresh");
    await until(() => requests().length > before);
    await idle();
  }
  async function language(value, label = "language") {
    await idle(label + "-ready");
    const before = requests().length;
    $("language").value = value;
    $("language").dispatchEvent(new Event("change", { bubbles: true }));
    await until(() => requests().length > before, label + "-rpc-start");
    await idle(label + "-complete");
    assert(requests().at(-1).locale === value);
  }
  function storageIsLanguageOnly() {
    return localStorage.length === 1 && localStorage.key(0) === "shellscaled.language" &&
      localStorage.getItem("shellscaled.language") === $("language").value;
  }
  const cards = () => [...document.querySelectorAll("#device-list .device-card")];
  const deviceRequests = () => requests().filter((request) => request.action === "devices");
  const settingsRequests = () => requests().filter((request) => request.action === "settings");
  const statusRequests = () => requests().filter((request) => request.action === "status");
  async function applySetting(id, value) {
    await idle();
    const control = $(id);
    if (control.checked === value) {
      assert(backend().settings[control.dataset.setting] === value);
      return;
    }
    const before = settingsRequests().length;
    click(id);
    await until(() => settingsRequests().length > before);
    await idle();
    const request = settingsRequests().at(-1);
    assert(settingsRequests().length === before + 1 && Object.keys(request.settings).join(",") === control.dataset.setting);
    assert(request.settings[control.dataset.setting] === value && backend().settings[control.dataset.setting] === value && control.checked === value);
  }
  const binaryRequests = () => requests().filter((request) => request.action.startsWith("binary-"));
  const networkRequests = (action) => requests().filter((request) => request.action === action);
  function editRoutes(value) {
    $("advertise-routes").value = value;
    $("advertise-routes").dispatchEvent(new Event("input", { bubbles: true }));
  }
  async function networkSetup(target = "", advertised = false, routes = "") {
    await idle();
    fixture.setNetworkRole(target, advertised, routes);
    await refresh();
    click("tab-settings");
    // A cached list keeps the select usable while entering Settings refreshes
    // exit-nodes independently of the status/busy queue. Start each held-read
    // scenario only after that initial refresh has finished.
    await until(() => !$("exit-node-select").disabled && !$("network-refresh").disabled &&
      $("network-refresh").getAttribute("aria-busy") === "false", "network-setup-ready");
  }
  async function networkTest(name, body) {
    await test(name, async () => {
      const original = backend();
      try { await body(); }
      finally {
        window.dispatchEvent(new Event("pagehide"));
        fixture.setStatusMode("normal");
        fixture.setIPv6Mode("normal");
        fixture.setExitNodesOnline(true);
        fixture.setNetworkRole(original.exitNodeTarget, original.advertiseExitNode, original.advertiseRoutes.join(","));
        fixture.setIPv6Status(original.exitNodeIPv6Takeover, original.exitNodeIPv6);
        fixture.releaseNetwork();
        fixture.releaseStatus();
        fixture.releaseExitNodes(0);
        fixture.releaseExitNodes(0);
        await idle();
        window.dispatchEvent(new Event("pageshow"));
        await idle();
        click("tab-overview");
      }
    });
  }
  const binaryLabel = () => $("binary-action-label").textContent;
  async function checkBinary(mode) {
    await idle();
    // Changing channel invalidates the previous offer through the real UI.
    // A ready Update button must never be used as a second Check button.
    if (binaryLabel() === ShellscaledI18n.t("binary.update")) {
      const selected = $("binary-channel");
      const original = selected.value;
      for (const channel of [original === "stable" ? "preview" : "stable", original]) {
        selected.value = channel;
        selected.dispatchEvent(new Event("change", { bubbles: true }));
        await idle();
      }
    }
    fixture.setBinaryMode(mode);
    const before = binaryRequests().length;
    click("binary-action");
    await until(() => binaryRequests().length > before);
    await idle();
    assert(binaryRequests().length === before + 1 && binaryRequests().at(-1).action === "binary-check");
    assert(Object.keys(binaryRequests().at(-1)).sort().join(",") === "action,locale");
  }
  function binaryMessage() {
    const message = $("binary-message");
    return message && $("binary-update-card").contains(message) && !message.hidden &&
      message.textContent.trim() && !rawKey.test(message.textContent.trim());
  }
  async function openDevices(reload = false) {
    await idle();
    click("tab-devices");
    await idle();
    await until(() => cards().length > 0);
    if (reload) {
      const before = deviceRequests().length;
      click("refresh-devices");
      await until(() => deviceRequests().length > before);
      await idle();
    }
  }
  function search(value) {
    $("device-search").value = value;
    $("device-search").dispatchEvent(new Event("input", { bubbles: true }));
  }
  const notice = () => JSON.stringify([$("notice").textContent, $("notice").hidden]);
  function mockClipboard(writeText, execCommand) {
    const original = Object.getOwnPropertyDescriptor(navigator, "clipboard");
    const originalExec = document.execCommand;
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: writeText ? { writeText } : undefined });
    // Always intercept the fallback too: the ordinary fixture never touches Android's clipboard.
    document.execCommand = execCommand || (() => false);
    return () => {
      if (original) Object.defineProperty(navigator, "clipboard", original);
      else delete navigator.clipboard;
      document.execCommand = originalExec;
    };
  }
  async function copy(button, state) {
    assert(button && !button.disabled && button.dataset.copy);
    const before = notice();
    button.click();
    await until(() => !button.dataset.copyPending && !button.disabled && button.dataset.copyState === state, "copy-feedback-" + state);
    const feedback = button.querySelector(".copy-feedback");
    assert(feedback && feedback.getAttribute("aria-live") === "polite");
    assert(feedback.textContent === ShellscaledI18n.t(state === "copied" ? "copy.copied" : "copy.failed"));
    assert(notice() === before);
  }

  try {
    await until(() => window.ShellscaledI18n && $("main") && $("preview-notice").hidden);
    await idle();
    localStorage.clear(); // This standalone fixture has its own app/origin storage.
    const translations = JSON.parse(fixture.translationsJSON());
    const locales = ["zh-CN", "zh-TW", "en"];
    const keys = Object.keys(translations.en).sort();
    const dynamicIDs = new Set(["state-title", "connection-label", "service-status", "freshness",
      "exit-lan-description", "settings-hint", "run-command", "command-output",
      "devices-message", "console-label", "about-description", "login-label", "binary-action-label", "binary-message", "network-message", "exit-node-summary", "exit-node-ipv6", "advertise-exit-description"]);

    await test("bridge-is-fixture", () => {
      assert(location.origin === "https://mui.kernelsu.org" && window.top === window);
      assert($("preview-notice").hidden && requests().length > 0);
      assert([...$("language").options].map((option) => option.value).join(",") === "system,zh-CN,zh-TW,en");
    });

    await test("bridge-spawn-paints-and-keeps-animating", async () => {
      const native = window.ksu;
      let painted = false;
      let called = false;
      const bridge = {
        exec() { throw new Error("synchronous bridge must not run"); },
        spawn(...args) {
          assert(painted && $("refresh").classList.contains("is-working"));
          called = true;
          native.spawn(...args);
        },
      };
      try {
        window.ksu = bridge;
        assert(window.ksu === bridge);
        fixture.holdNextStatus();
        click("refresh");
        requestAnimationFrame(() => { painted = true; });
        await until(() => called);
        const icon = $("refresh").querySelector("svg");
        const transform = getComputedStyle(icon).transform;
        let frames = 0;
        requestAnimationFrame(function tick() {
          frames++;
          if (frames < 4) requestAnimationFrame(tick);
        });
        await until(() => frames === 4);
        assert(getComputedStyle(icon).transform !== transform && $("main").getAttribute("aria-busy") === "true");
      } finally {
        window.ksu = native;
        fixture.releaseStatus();
        await idle();
      }
      assert(JSON.parse(fixture.transportsJSON()).every((mode) => mode === "spawn"));
    });

    await test("bridge-exec-only-manager-fallback", async () => {
      const native = window.ksu;
      try {
        window.ksu = { exec: (...args) => native.exec(...args) };
        await refresh();
        assert(JSON.parse(fixture.transportsJSON()).at(-1) === "exec");
      } finally { window.ksu = native; }
    });

    await test("idle-helper-rechecks-refresh-during-paint-wait", async () => {
      await idle();
      const before = statusRequests().length;
      let resolved = false;
      fixture.holdNextStatus();
      const waiting = idle("idle-helper-held-refresh").then(() => { resolved = true; });
      try {
        // This starts a refresh after idle's first check, before its 30ms
        // paint wait finishes, as the periodic poll can do in other cases.
        document.dispatchEvent(new Event("visibilitychange"));
        await until(() => statusRequests().length > before, "idle-helper-refresh-start");
        await wait(50);
        assert(!resolved && $("main").getAttribute("aria-busy") === "true", "idle-helper-waits-for-refresh");
      } finally {
        fixture.releaseStatus();
        await waiting;
      }
      assert(resolved && $("main").getAttribute("aria-busy") === "false", "idle-helper-refresh-complete");
    });

    await test("settings-load-exit-nodes-automatically", async () => {
      const count = () => requests().filter((request) => request.action === "exit-nodes").length;
      const before = count();
      click("tab-settings");
      await until(() => count() > before);
      await until(() => $("network-refresh").getAttribute("aria-busy") === "false");
      assert(!$("exit-node-select").disabled);
      assert($("network-message").textContent !== ShellscaledI18n.t("network.loading"));
      const refreshBox = $("network-refresh").getBoundingClientRect();
      assert(refreshBox.width >= 44 && refreshBox.height >= 44);
      assert($("network-refresh").getAttribute("aria-label").trim());
      const dns = $("magicdns-status");
      // MagicDNS is off by default; with an exit node in use, say DNS bypasses it.
      assert(backend().state === "Running" && !backend().magicDNS && backend().magicDNSEnabled === false && backend().exitNodeConfigured === true);
      assert(!dns.hidden && dns.dataset.kind === "warning" && dns.getAttribute("aria-live") === "polite");
      assert(dns.textContent === ShellscaledI18n.t("network.dnsOffExit"));
      click("tab-overview");
    });

    await test("magicdns-opt-in-defaults-off", async () => {
      await idle();
      click("tab-settings");
      await until(() => !$("magicdns-enabled").disabled);
      const toggle = $("magicdns-enabled");
      assert(backend().magicDNSEnabled === false && !toggle.checked);
      assert(toggle.getAttribute("role") === "switch" && toggle.closest("#network-role-card"));
      const dnsRequests = () => requests().filter((request) => request.action === "magicdns");
      for (const enabled of [true, false]) {
        const before = dnsRequests().length;
        click("magicdns-enabled");
        await until(() => dnsRequests().length === before + 1);
        const request = dnsRequests().at(-1);
        assert(request.magicDNSEnabled === enabled && Object.keys(request).every((key) => ["action", "locale", "magicDNSEnabled"].includes(key)));
        await idle();
        assert(toggle.checked === enabled && backend().magicDNSEnabled === enabled);
        const status = $("magicdns-status");
        if (enabled) assert(!status.hidden && status.textContent === ShellscaledI18n.t("network.dnsInactive"));
        else if (backend().exitNodeConfigured === true) assert(!status.hidden && status.textContent === ShellscaledI18n.t("network.dnsOffExit"));
        else assert(status.hidden);
      }
      click("tab-overview");
    });

    await networkTest("exit-node-selection-masked-prefs-and-clear", async () => {
      await networkSetup("", false, "10.0.0.0/8");
      const settings = JSON.stringify(backend().settings);
      const select = $("exit-node-select");
      const before = networkRequests("exit-node").length;
      assert([...select.options].some((option) => option.value === "fixture-offline" && option.disabled));
      select.value = "fixture-offline";
      select.dispatchEvent(new Event("change", { bubbles: true }));
      assert($("exit-node-apply").disabled);
      select.value = "fixture-workstation";
      select.dispatchEvent(new Event("change", { bubbles: true }));
      fixture.holdNextNetwork();
      click("exit-node-apply");
      await until(() => networkRequests("exit-node").length === before + 1);
      assert(select.disabled && getComputedStyle(select).opacity === "1");
      $("exit-node-apply").click();
      await wait(60);
      assert(networkRequests("exit-node").length === before + 1);
      select.value = "fixture-offline";
      select.dispatchEvent(new Event("change", { bubbles: true }));
      fixture.releaseNetwork();
      await idle();
      assert(backend().exitNodeTarget === "fixture-workstation" && select.value === "fixture-offline");
      click("exit-node-clear");
      await until(() => networkRequests("exit-node").length === before + 2);
      await idle();
      const request = networkRequests("exit-node").at(-1);
      assert(request.exitNode === "" && Object.keys(request).sort().join() === "action,exitNode,locale");
      assert(!backend().exitNodeConfigured && JSON.stringify(backend().settings) === settings);
      assert(backend().advertiseRoutes.join() === "10.0.0.0/8");
    });

    await networkTest("advertise-exit-preserves-external-subnet-edit", async () => {
      await networkSetup("", false, "10.0.0.0/8");
      const settings = JSON.stringify(backend().settings);
      fixture.setNetworkRole("", false, "192.0.2.0/24");
      const before = networkRequests("advertise").length;
      click("advertise-exit-node");
      click("advertise-save");
      await until(() => networkRequests("advertise").length === before + 1);
      await idle();
      const request = networkRequests("advertise").at(-1);
      assert(request.advertiseExitNode === true && !Object.hasOwn(request, "advertiseRoutes"));
      assert(backend().advertiseExitNode && backend().advertiseRoutes.join() === "192.0.2.0/24");
      assert(JSON.stringify(backend().settings) === settings);
    });

    await networkTest("advertise-routes-preserve-external-exit-edit", async () => {
      await networkSetup("", false, "10.0.0.0/8");
      fixture.setNetworkRole("", true, "10.0.0.0/8");
      editRoutes("192.0.2.0/24");
      const before = networkRequests("advertise").length;
      click("advertise-save");
      await until(() => networkRequests("advertise").length === before + 1);
      await idle();
      const request = networkRequests("advertise").at(-1);
      assert(request.advertiseRoutes === "192.0.2.0/24" && !Object.hasOwn(request, "advertiseExitNode"));
      assert(backend().advertiseExitNode && $("advertise-exit-node").checked);
      assert(backend().advertiseRoutes.join() === "192.0.2.0/24");
    });

    await networkTest("advertise-captures-submitted-draft-and-preserves-later-edits", async () => {
      await networkSetup();
      editRoutes("192.0.2.0/24");
      const before = networkRequests("advertise").length;
      fixture.holdNextNetwork();
      click("advertise-save");
      await until(() => networkRequests("advertise").length === before + 1);
      editRoutes("203.0.113.0/24");
      $("advertise-exit-node").checked = true;
      $("advertise-exit-node").dispatchEvent(new Event("change", { bubbles: true }));
      fixture.releaseNetwork();
      await idle();
      const first = networkRequests("advertise").at(-1);
      assert(first.advertiseRoutes === "192.0.2.0/24" && !Object.hasOwn(first, "advertiseExitNode"));
      assert($("advertise-routes").value === "203.0.113.0/24" && $("advertise-exit-node").checked);
      assert(!$("advertise-save").disabled && !backend().advertiseExitNode);
      click("advertise-save");
      await until(() => networkRequests("advertise").length === before + 2);
      await idle();
      const second = networkRequests("advertise").at(-1);
      assert(second.advertiseRoutes === "203.0.113.0/24" && second.advertiseExitNode === true);
      assert(backend().advertiseExitNode && backend().advertiseRoutes.join() === "203.0.113.0/24");
    });

    await networkTest("advertise-invalid-routes-and-failure-reconciliation", async () => {
      await networkSetup();
      const before = networkRequests("advertise").length;
      editRoutes("0.0.0.0/0");
      assert($("advertise-routes").getAttribute("aria-invalid") === "true" && $("advertise-save").disabled);
      $("advertise-save").click();
      await wait(50);
      assert(networkRequests("advertise").length === before);
      editRoutes("192.0.2.0/24");
      fixture.nextNetworkResponseFails();
      fixture.setStatusMode("error");
      click("advertise-save");
      await until(() => networkRequests("advertise").length === before + 1);
      await idle();
      assert(backend().advertiseRoutes.join() === "192.0.2.0/24");
      assert($("advertise-routes").disabled && $("advertise-save").disabled && $("exit-node-select").disabled);
      assert(!$("advertise-message").hidden && $("advertise-message").dataset.kind === "error");
      fixture.setStatusMode("normal");
      await refresh();
      assert(!$("advertise-routes").disabled && $("advertise-routes").value === "192.0.2.0/24");
    });

    await networkTest("exit-node-ipv6-pending-success-and-duplicate-blocking", async () => {
      await networkSetup("fixture-workstation");
      const toggle = $("exit-node-ipv6-takeover");
      assert(toggle.checked && $("exit-node-ipv6").hidden);
      const before = networkRequests("exit-node-ipv6").length;
      fixture.holdNextNetwork();
      click("exit-node-ipv6-takeover");
      await until(() => networkRequests("exit-node-ipv6").length === before + 1);
      assert(!toggle.checked && toggle.disabled);
      toggle.click();
      await wait(60);
      assert(networkRequests("exit-node-ipv6").length === before + 1);
      const request = networkRequests("exit-node-ipv6").at(-1);
      assert(request.exitNodeIPv6 === false && Object.keys(request).sort().join() === "action,exitNodeIPv6,locale");
      fixture.releaseNetwork();
      await idle();
      assert(!toggle.checked && !backend().exitNodeIPv6Takeover && !backend().exitNodeIPv6);
      assert($("exit-node-ipv6").textContent === ShellscaledI18n.t("network.ipv6Physical"));
      click("exit-node-ipv6-takeover");
      await until(() => networkRequests("exit-node-ipv6").length === before + 2);
      await idle();
      assert(toggle.checked && $("exit-node-ipv6").hidden);
    });

    await networkTest("exit-node-ipv6-unsupported-daemon-reconciles-saved-preference", async () => {
      await networkSetup("fixture-workstation");
      fixture.setIPv6Mode("unsupported");
      const before = networkRequests("exit-node-ipv6").length;
      const statusBefore = statusRequests().length;
      click("exit-node-ipv6-takeover");
      await until(() => networkRequests("exit-node-ipv6").length === before + 1 && statusRequests().length > statusBefore);
      await idle();
      assert(!$("exit-node-ipv6-takeover").checked && !backend().exitNodeIPv6Takeover && backend().exitNodeIPv6);
      assert($("exit-node-ipv6").textContent === ShellscaledI18n.t("network.ipv6Unsupported"));
      assert(!$("network-message").hidden && $("network-message").dataset.kind === "error");
    });

    await networkTest("exit-node-list-ignores-out-of-order-page-lifecycle-completions", async () => {
      await networkSetup();
      fixture.setExitNodesOnline(false);
      fixture.holdNextExitNodes();
      const before = networkRequests("exit-nodes").length;
      click("network-refresh");
      await until(() => networkRequests("exit-nodes").length === before + 1);
      window.dispatchEvent(new Event("pagehide"));
      fixture.setExitNodesOnline(true);
      fixture.holdNextExitNodes();
      window.dispatchEvent(new Event("pageshow"));
      await until(() => networkRequests("exit-nodes").length === before + 2);
      fixture.releaseExitNodes(1);
      await until(() => !$("exit-node-select").disabled);
      const option = () => [...$("exit-node-select").options].find((value) => value.value === "fixture-workstation");
      assert(option() && !option().disabled, "fresh-exit-list-online");
      fixture.releaseExitNodes(0);
      await wait(100);
      assert(option() && !option().disabled && $("network-refresh").getAttribute("aria-busy") === "false", "stale-exit-list-ignored");
    });

    await networkTest("network-write-page-lifecycle-requires-fresh-status", async () => {
      await networkSetup("fixture-workstation");
      fixture.holdNextNetwork();
      const before = networkRequests("exit-node-ipv6").length;
      click("exit-node-ipv6-takeover");
      await until(() => networkRequests("exit-node-ipv6").length === before + 1);
      window.dispatchEvent(new Event("pagehide"));
      assert($("advertise-routes").value === "" && $("exit-node-select").disabled);
      fixture.setNetworkRole("", false, "203.0.113.0/24");
      window.dispatchEvent(new Event("pageshow"));
      fixture.releaseNetwork();
      await idle();
      assert(!backend().exitNodeConfigured && $("exit-node-summary").textContent === ShellscaledI18n.t("network.summaryNone"));
      assert(!$("exit-node-ipv6-takeover").checked && $("advertise-routes").value === "203.0.113.0/24");
    });

    await networkTest("network-fallback-status-cannot-render-after-pagehide", async () => {
      await networkSetup("fixture-workstation", false, "192.0.2.0/24");
      fixture.nextNetworkNeedsStatus();
      fixture.holdNextStatus();
      const before = statusRequests().length;
      click("exit-node-ipv6-takeover");
      await until(() => statusRequests().length > before);
      window.dispatchEvent(new Event("pagehide"));
      fixture.setNetworkRole("", false, "203.0.113.0/24");
      fixture.releaseStatus();
      await idle();
      assert($("advertise-routes").value === "" && $("exit-node-select").disabled);
      assert($("exit-node-summary").textContent === ShellscaledI18n.t("network.waiting"));
      window.dispatchEvent(new Event("pageshow"));
      await idle();
      assert($("advertise-routes").value === "203.0.113.0/24");
      assert($("exit-node-summary").textContent === ShellscaledI18n.t("network.summaryNone"));
    });

    for (const locale of locales) {
      await test("translations." + locale, async () => {
        await language(locale);
        assert(document.documentElement.lang === locale);
        assert(Object.keys(translations[locale]).sort().join("|") === keys.join("|"));
        for (const key of keys) {
          const value = translations[locale][key];
          assert(typeof value === "string" && value.trim() && value !== key);
          const parameters = { time: "12:34", code: 0, count: 100000 };
          for (const match of value.matchAll(/\{([A-Za-z]+)\}/g)) if (!(match[1] in parameters)) parameters[match[1]] = "example";
          assert(ShellscaledI18n.t(key, parameters) === value.replace(/\{([A-Za-z]+)\}/g, (_, key) => String(parameters[key])));
        }
        for (const element of document.querySelectorAll("[data-i18n]")) {
          const key = element.dataset.i18n;
          assert(Object.hasOwn(translations[locale], key));
          if (dynamicIDs.has(element.id) && element.hidden && !element.textContent.trim()) continue;
          assert(element.textContent.trim() && !rawKey.test(element.textContent) && !/\{[A-Za-z]+\}/.test(element.textContent));
          if (!dynamicIDs.has(element.id)) assert(element.textContent === ShellscaledI18n.t(key));
        }
        for (const [attribute, target] of [["data-i18n-aria", "aria-label"], ["data-i18n-placeholder", "placeholder"]]) {
          for (const element of document.querySelectorAll("[" + attribute + "]")) {
            const key = element.getAttribute(attribute);
            assert(Object.hasOwn(translations[locale], key));
            assert(element.getAttribute(target) === ShellscaledI18n.t(key) && element.getAttribute(target).trim());
          }
        }
        assert(storageIsLanguageOnly());
      });
    }

    await test("system-language-and-rpc", async () => {
      const original = Object.getOwnPropertyDescriptor(navigator, "languages");
      try {
        for (const [tag, expected] of [["zh-Hant-TW", "zh-TW"], ["zh-Hans-CN", "zh-CN"], ["en-US", "en"]]) {
          Object.defineProperty(navigator, "languages", { configurable: true, get: () => [tag] });
          await language("system");
          assert(ShellscaledI18n.preference() === "system" && document.documentElement.lang === expected);
          assert(storageIsLanguageOnly());
        }
      } finally {
        if (original) Object.defineProperty(navigator, "languages", original);
        else delete navigator.languages;
        window.dispatchEvent(new Event("languagechange"));
        await idle();
      }
    });

    await test("single-connection-toggle", async () => {
      assert(document.querySelectorAll("#state-card button").length === 1 && !$("connect") && !$("disconnect"));
      fixture.setState("Running");
      await refresh();
      click("toggle-connection");
      await idle();
      assert(requests().at(-1).action === "down" && backend().state === "Stopped");
      assert($("connection-label").textContent === ShellscaledI18n.t("connection.connect"));
      click("toggle-connection");
      await idle();
      assert(requests().at(-1).action === "up" && backend().state === "Running");
      assert($("connection-label").textContent === ShellscaledI18n.t("connection.disconnect"));
    });

    await test("cancel-starting-connection", async () => {
      fixture.setState("Stopped");
      await refresh();
      fixture.nextUpStarts();
      click("toggle-connection");
      await idle();
      assert(backend().state === "Starting" && !$("toggle-connection").disabled);
      assert($("connection-label").textContent.trim() && !rawKey.test($("connection-label").textContent));
      click("toggle-connection");
      await idle();
      assert(requests().at(-1).action === "down" && backend().state === "Stopped");
    });

    await test("settings-apply-immediately-with-single-field-masks", async () => {
      fixture.setState("Running");
      fixture.setAcceptRoutes(false);
      await language($("language").value);
      click("tab-settings");
      const original = backend().settings;
      const controls = [...document.querySelectorAll("[data-setting]")];
      const before = settingsRequests().length;
      const globalNotice = notice();
      const card = $("panel-settings").querySelector(".settings-card");
      const rows = [...card.children];
      const cardTop = card.getBoundingClientRect().top;
      try {
        fixture.holdNextSettings();
        click("shields-up");
        await until(() => settingsRequests().length === before + 1);
        assert($("main").getAttribute("aria-busy") === "true" && controls.every((control) => control.disabled));
        assert(rows.every((row, index) => row === card.children[index]));
        assert(card.getBoundingClientRect().top === cardTop);
        for (const id of ["language", "binary-channel", "binary-action"]) assert(getComputedStyle($(id)).opacity === "1");
        for (const control of controls) assert(getComputedStyle(control.nextElementSibling).opacity === "1");
        assert($("shields-up").closest(".setting-row").getAttribute("aria-busy") === "true");
        assert($("shields-up").checked === !original.shieldsUp && $("settings-hint").textContent === ShellscaledI18n.t("settings.applying"));
        for (const control of controls) {
          control.click();
          control.checked = !control.checked;
          control.dispatchEvent(new Event("change", { bubbles: true }));
          assert(control.checked === (control.dataset.setting === "shieldsUp" ? !original.shieldsUp : original[control.dataset.setting]));
        }
        document.dispatchEvent(new Event("visibilitychange"));
        fixture.setAcceptRoutes(true); // A concurrent unrelated native preference edit.
        await wait(50);
        assert(settingsRequests().length === before + 1 && $("shields-up").checked === !original.shieldsUp);
        fixture.releaseSettings();
        await idle();
        const request = settingsRequests().at(-1);
        assert(Object.keys(request.settings).join(",") === "shieldsUp" && request.settings.shieldsUp === !original.shieldsUp);
        assert(backend().settings.acceptRoutes && backend().settings.shieldsUp === !original.shieldsUp);
        for (const locale of locales) {
          await language(locale);
          assert($("accept-routes").checked && $("shields-up").checked === !original.shieldsUp);
        }
        await openDevices(true);
        assert(settingsRequests().length === before + 1 && notice() === globalNotice);
        click("tab-settings");
        await applySetting("accept-routes", original.acceptRoutes);
        await applySetting("exit-lan", !original.exitNodeAllowLanAccess);
        await applySetting("exit-lan", original.exitNodeAllowLanAccess);
        await applySetting("shields-up", original.shieldsUp);
        assert(!$("save-settings") && $("settings-hint").textContent === ShellscaledI18n.t("settings.automatic"));
        assert(JSON.stringify(backend().settings) === JSON.stringify(original));
      } finally {
        fixture.releaseSettings();
        await idle();
        await language($("language").value);
        for (const control of controls) await applySetting(control.id, original[control.dataset.setting]);
      }
    });

    await test("settings-failure-rolls-back-and-serializes-refresh", async () => {
      await idle();
      click("tab-settings");
      const original = backend().settings;
      const controls = [...document.querySelectorAll("[data-setting]")];
      const globalNotice = notice();
      try {
        const before = settingsRequests().length;
        const statuses = statusRequests().length;
        fixture.nextSettingsFails();
        fixture.holdNextSettings();
        click("shields-up");
        await until(() => settingsRequests().length === before + 1);
        document.dispatchEvent(new Event("visibilitychange"));
        await wait(50);
        assert($("shields-up").checked === !original.shieldsUp && controls.every((control) => control.disabled));
        fixture.releaseSettings();
        await idle();
        assert(statusRequests().length > statuses && $("shields-up").checked === original.shieldsUp);
        assert(JSON.stringify(backend().settings) === JSON.stringify(original));
        assert($("settings-hint").textContent.includes(ShellscaledI18n.t("settings.failed")) && notice() === globalNotice);
        await applySetting("shields-up", !original.shieldsUp);
        assert($("settings-hint").textContent === ShellscaledI18n.t("settings.automatic"));

        fixture.holdNextStatus();
        const statusCount = statusRequests().length;
        const settingsCount = settingsRequests().length;
        const refreshPending = language($("language").value);
        await until(() => statusRequests().length > statusCount);
        fixture.setAcceptRoutes(!original.acceptRoutes);
        for (const control of controls) {
          assert(control.disabled);
          control.checked = !control.checked;
          control.dispatchEvent(new Event("change", { bubbles: true }));
          assert(control.checked === (control.dataset.setting === "shieldsUp" ? !original.shieldsUp : original[control.dataset.setting]));
        }
        assert(settingsRequests().length === settingsCount);
        fixture.releaseStatus();
        await refreshPending;
        await applySetting("shields-up", original.shieldsUp);
        assert(backend().settings.acceptRoutes === !original.acceptRoutes && $("accept-routes").checked === !original.acceptRoutes);
      } finally {
        fixture.releaseSettings();
        fixture.releaseStatus();
        await idle();
        await language($("language").value);
        for (const control of controls) await applySetting(control.id, original[control.dataset.setting]);
      }
    });

    await test("settings-failed-reconciliation-blocks-edits-until-confirmed", async () => {
      await idle();
      click("tab-settings");
      const original = backend().settings.shieldsUp;
      const controls = [...document.querySelectorAll("[data-setting]")];
      const globalNotice = notice();
      const before = settingsRequests().length;
      const statuses = statusRequests().length;
      try {
        fixture.setStatusMode("error");
        fixture.nextSettingsResponseFails();
        click("shields-up");
        await until(() => settingsRequests().length > before && statusRequests().length > statuses);
        await idle();
        assert(backend().settings.shieldsUp === !original && $("shields-up").checked === original);
        assert(controls.every((control) => control.disabled));
        assert($("settings-hint").textContent.includes(ShellscaledI18n.t("settings.unconfirmed")) && notice() === globalNotice);
        // Programmatic change events must not bypass disabled controls either.
        $("shields-up").checked = !original;
        $("shields-up").dispatchEvent(new Event("change", { bubbles: true }));
        await wait(50);
        assert(settingsRequests().length === before + 1 && $("shields-up").checked === original);
        for (const locale of locales) {
          await language(locale);
          assert(controls.every((control) => control.disabled));
          assert($("settings-hint").textContent.includes(ShellscaledI18n.t("settings.unconfirmed")));
        }
        fixture.setStatusMode("normal");
        await refresh();
        assert(controls.every((control) => !control.disabled) && $("shields-up").checked === !original);
      } finally {
        fixture.setStatusMode("normal");
        await refresh();
        await applySetting("shields-up", original);
      }
    });

    await test("settings-ignore-unavailable-service-defaults", async () => {
      await idle();
      click("tab-settings");
      const original = backend().settings.shieldsUp;
      const controls = [...document.querySelectorAll("[data-setting]")];
      try {
        await applySetting("shields-up", true);
        const before = settingsRequests().length;
        for (const mode of ["stopped", "service-error", "invalid-settings"]) {
          fixture.setStatusMode(mode);
          await refresh();
          assert(controls.every((control) => control.disabled));
          assert($("shields-up").checked && backend().settings.shieldsUp);
          assert($("settings-hint").textContent.includes(ShellscaledI18n.t("settings.unconfirmed")));
        }
        assert(settingsRequests().length === before);
        fixture.setStatusMode("normal");
        await refresh();
        assert(controls.every((control) => !control.disabled) && $("shields-up").checked);
      } finally {
        fixture.setStatusMode("normal");
        await refresh();
        await applySetting("shields-up", original);
      }
    });

    await test("settings-page-return-waits-for-fresh-preferences", async () => {
      await idle();
      click("tab-settings");
      const original = backend().settings.shieldsUp;
      const controls = [...document.querySelectorAll("[data-setting]")];
      const before = settingsRequests().length;
      const statuses = statusRequests().length;
      try {
        fixture.holdNextSettings();
        click("shields-up");
        await until(() => settingsRequests().length > before);
        window.dispatchEvent(new Event("pagehide"));
        fixture.setStatusMode("error");
        window.dispatchEvent(new Event("pageshow"));
        assert(controls.every((control) => control.disabled));
        fixture.releaseSettings();
        await until(() => statusRequests().length > statuses);
        await idle();
        assert(controls.every((control) => control.disabled));
        assert(settingsRequests().length === before + 1 && backend().settings.shieldsUp === !original);
        fixture.setStatusMode("normal");
        await refresh();
        assert(controls.every((control) => !control.disabled) && $("shields-up").checked === !original);
      } finally {
        fixture.releaseSettings();
        fixture.setStatusMode("normal");
        window.dispatchEvent(new Event("pageshow"));
        await refresh();
        await applySetting("shields-up", original);
      }
    });

    await test("raw-command-examples-and-language-first-settings", async () => {
      const expected = ["tailscale status", "tailscale version", "tailscale netcheck", "tailscaled --version"].sort();
      for (const locale of locales) {
        await language(locale);
        click("tab-settings");
        const firstCard = $("panel-settings").querySelector(".card");
        assert(firstCard.classList.contains("language-card") && firstCard.contains($("language")));
        assert(!$("save-settings") && $("settings-hint").textContent === ShellscaledI18n.t("settings.automatic"));
        click("tab-command");
        const examples = [...document.querySelectorAll("button[data-example]")];
        assert(examples.map((button) => button.dataset.example).sort().join("|") === expected.join("|"));
        for (const button of examples) {
          assert(button.textContent.trim() === button.dataset.example && !button.hasAttribute("data-i18n"));
          button.click();
          assert($("command-input").value === button.dataset.example);
        }
      }
    });

    await test("device-list-search-and-untrusted-text", async () => {
      await openDevices();
      const examples = JSON.parse(fixture.devicesJSON());
      assert(cards().length === examples.length && examples.some((device) => device.self) && examples.some((device) => !device.online));
      for (const example of examples) {
        const card = cards().find((element) => element.dataset.deviceId === example.id);
        assert(card && card.textContent.trim());
        if (example.name) assert(card.textContent.includes(example.name));
        if (example.dnsName) assert(card.textContent.includes(example.dnsName));
        for (const ip of example.ips) assert(card.textContent.includes(ip));
        const status = card.querySelector(".device-status");
        assert(status && status.dataset.online === String(example.online));
        assert(status.textContent === ShellscaledI18n.t(example.online ? "devices.online" : "devices.offline"));
        assert(Boolean(card.querySelector(".device-self")) === example.self);
        if (!example.ips.length) assert(!card.querySelector('[data-copy-kind="ipv4"],[data-copy-kind="ipv6"]'));
      }
      assert(!$("fixture-device-injected") && !$("fixture-dns-injected") && !$("fixture-os-injected"));
      assert(!$("device-list").querySelector("img,script,iframe,[onerror],[onload]"));
      const before = deviceRequests().length;
      for (const [query, id] of [["WORKSTATION.DOCS.EXAMPLE", "fixture-workstation"],
        ["::20", "fixture-workstation"], ["TRAVEL-LAPTOP", "fixture-offline"],
        ["INITIALIZING", "fixture-no-ip"], ["<img", "fixture-untrusted"]]) {
        search(query);
        assert(cards().length === 1 && cards()[0].dataset.deviceId === id);
      }
      search("[no-match]");
      assert(cards().length === 0 && !$("devices-message").hidden && $("devices-message").textContent.trim());
      search("");
      assert(cards().length === examples.length && deviceRequests().length === before);
    });

    await test("copy-keeps-focus-brightness-selection-and-layout", async () => {
      const pending = [];
      const restore = mockClipboard(() => new Promise((resolve, reject) => pending.push({ resolve, reject })), () => false);
      try {
        for (const locale of locales) {
          await language(locale, "copy-layout-" + locale + "-language");
          click("tab-overview");
          const button = $("addresses").lastElementChild;
          assert(button && button.isConnected && !button.disabled && button.dataset.copy && !button.dataset.copyPending,
            "copy-layout-" + locale + "-control-ready");
          const feedback = button.querySelector(".copy-feedback");
          const value = button.querySelector(".copy-value");
          button.scrollIntoView({ block: "center" });
          button.focus({ preventScroll: true });
          const range = document.createRange();
          range.selectNodeContents(value);
          window.getSelection().removeAllRanges();
          window.getSelection().addRange(range);
          const selected = window.getSelection().toString();
          const scroll = window.scrollY;
          const geometry = () => [button.getBoundingClientRect().height, value.getBoundingClientRect().width,
            feedback.getBoundingClientRect().width];
          const original = JSON.stringify(geometry());
          const before = pending.length;
          button.click();
          await until(() => pending.length === before + 1, "copy-layout-" + locale + "-write-start");
          assert(!button.disabled && button.getAttribute("aria-disabled") === "true" && button.getAttribute("aria-busy") === "true");
          assert(getComputedStyle(button).opacity === "1" && document.activeElement === button);
          button.click();
          assert(pending.length === before + 1 && JSON.stringify(geometry()) === original);
          pending.at(-1).resolve();
          await until(() => button.dataset.copyState === "copied", "copy-layout-" + locale + "-success-feedback");
          assert(JSON.stringify(geometry()) === original && document.activeElement === button && window.scrollY === scroll);
          button.click();
          await until(() => pending.length === before + 2, "copy-layout-" + locale + "-retry-start");
          pending.at(-1).reject(new Error("synthetic denial"));
          await until(() => button.dataset.copyState === "failed", "copy-layout-" + locale + "-failure-feedback");
          assert(JSON.stringify(geometry()) === original && document.activeElement === button && window.scrollY === scroll);
          assert(window.getSelection().toString() === selected);
        }
      } finally {
        for (const operation of pending) operation.resolve();
        restore();
        window.getSelection().removeAllRanges();
        window.scrollTo(0, 0);
      }
    });

    await test("copy-every-field-through-clipboard-api", async () => {
      const writes = [];
      const restore = mockClipboard(async (value) => { writes.push(value); });
      try {
        click("tab-overview");
        const local = [...$("addresses").querySelectorAll("button.copy-control[data-copy]")];
        assert(local.length === backend().ips.length);
        for (const button of local) {
          const before = writes.length;
          await copy(button, "copied");
          assert(writes.length === before + 1 && writes.at(-1) === button.dataset.copy && backend().ips.includes(writes.at(-1)));
        }
        await openDevices();
        const examples = JSON.parse(fixture.devicesJSON());
        const kinds = new Set();
        for (const card of cards()) {
          const example = examples.find((device) => device.id === card.dataset.deviceId);
          for (const button of card.querySelectorAll("button.copy-control[data-copy]")) {
            const kind = button.dataset.copyKind;
            kinds.add(kind);
            const expected = kind === "name" ? example.name : kind === "dns" ? example.dnsName : null;
            if (expected !== null) assert(button.dataset.copy === expected);
            else assert((kind === "ipv4" || kind === "ipv6") && example.ips.includes(button.dataset.copy));
            const before = writes.length;
            await copy(button, "copied");
            assert(writes.length === before + 1 && writes.at(-1) === button.dataset.copy);
          }
        }
        assert(["name", "dns", "ipv4", "ipv6"].every((kind) => kinds.has(kind)));
      } finally { restore(); }
    });

    await test("copy-dom-fallback-and-local-failure", async () => {
      const writes = [];
      let allow = true;
      const restore = mockClipboard(undefined, (command) => {
        assert(command === "copy");
        writes.push(document.activeElement && typeof document.activeElement.value === "string" ?
          document.activeElement.value : window.getSelection().toString());
        return allow;
      });
      try {
        for (const locale of locales) {
          await language(locale);
          click("tab-overview");
          const button = $("addresses").querySelector("button.copy-control[data-copy]");
          await copy(button, "copied");
          assert(writes.at(-1) === button.dataset.copy);
        }
        allow = false;
        // A failed retry must replace any earlier success feedback on the same control.
        await refresh();
        await copy($("addresses").querySelector("button.copy-control[data-copy]"), "failed");
      } finally { restore(); }
    });

    await test("copy-lifecycle-rejects-stale-completions", async () => {
      const original = backend();
      const pending = [];
      let fallbacks = 0;
      const restore = mockClipboard((value) => new Promise((resolve, reject) => {
        const operation = { value, settled: false };
        operation.resolve = () => { operation.settled = true; resolve(); };
        operation.reject = () => { operation.settled = true; reject(new Error("fixture-copy-rejected")); };
        pending.push(operation);
      }), () => { fallbacks++; return true; });
      const hide = () => window.dispatchEvent(new PageTransitionEvent("pagehide", { persisted: true }));
      const resume = async (value) => {
        fixture.setLoginURL(value);
        window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: true }));
        await idle();
        await until(() => $("copy-login").dataset.copy === value && !$("copy-login").disabled);
      };
      const begin = async (value) => {
        const before = pending.length;
        click("copy-login");
        await until(() => pending.length === before + 1);
        assert(pending.at(-1).value === value && $("copy-login").dataset.copyPending && !$("copy-login").disabled);
        assert($("copy-login").getAttribute("aria-busy") === "true");
        return pending.at(-1);
      };
      try {
        click("tab-overview");
        fixture.setState("NeedsLogin");
        fixture.setLoginURL("https://example.test/login/old");
        await refresh();
        const control = $("copy-login");
        const old = await begin("https://example.test/login/old");
        hide();
        await resume("https://example.test/login/new");
        old.resolve();
        await wait(50);
        assert($("copy-login") === control && control.dataset.copy === "https://example.test/login/new");
        assert(!control.dataset.copyState && !control.dataset.copyPending && !control.disabled);
        const current = await begin("https://example.test/login/new");
        current.resolve();
        await until(() => control.dataset.copyState === "copied" && !control.dataset.copyPending && !control.disabled);

        hide();
        await resume("https://example.test/login/hidden-reject");
        const hidden = await begin("https://example.test/login/hidden-reject");
        hide();
        hidden.reject();
        await wait(50);
        assert(fallbacks === 0 && !document.querySelector(".clipboard-buffer"));
        await resume("https://example.test/login/late-reject");
        const late = await begin("https://example.test/login/late-reject");
        hide();
        await resume("https://example.test/login/latest");
        const latest = await begin("https://example.test/login/latest");
        late.reject();
        await wait(50);
        assert(fallbacks === 0 && control.dataset.copy === "https://example.test/login/latest");
        assert(control.dataset.copyPending && !control.disabled && !control.dataset.copyState);
        latest.resolve();
        await until(() => control.dataset.copyState === "copied" && !control.dataset.copyPending && !control.disabled);
        assert(fallbacks === 0);
      } finally {
        try {
          hide();
          fixture.setLoginURL(original.loginURL || "");
          fixture.setState(original.state);
          for (const operation of pending) if (!operation.settled) operation.resolve();
          await wait(25);
          window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: true }));
          await idle();
        } finally { restore(); }
      }
    });

    await test("login-without-url-busy-errors-and-confirmed-settings", async () => {
      await idle();
      const original = backend();
      const calls = () => requests().filter((request) => request.action === "login");
      const checkbox = document.querySelector('[data-setting="shieldsUp"]');
      const confirmed = original.settings.shieldsUp;
      const viewport = document.querySelector('meta[name="viewport"]');
      const originalViewport = viewport.content;
      const originalOpen = window.open;
      const url = location.href;
      const navigations = fixture.navigationAttempts();
      let opens = 0;
      window.open = () => { opens++; return null; };
      try {
        fixture.setState("NeedsLogin");
        fixture.setLoginURL("");
        fixture.setLoginMode("waiting");
        await language($("language").value);
        click("tab-settings");
        click("tab-overview");
        viewport.content = "width=320";
        await until(() => Math.abs(window.innerWidth - 320) <= 2);
        for (const locale of locales) {
          await language(locale);
          assert(!$("login-card").hidden && !$("login-tailscale").disabled && $("copy-login").disabled);
          assert(!$("login-url").value && $("login-label").textContent === ShellscaledI18n.t("login.action"));
          assert(document.documentElement.scrollWidth <= innerWidth + 1 && document.body.scrollWidth <= innerWidth + 1);
        }
        viewport.content = originalViewport;
        const before = calls().length;
        const globalNotice = notice();
        for (const tab of ["devices", "settings", "command", "overview"]) {
          click("tab-" + tab);
          await idle();
        }
        assert(calls().length === before);
        const firstRequest = requests().length;
        const settingsCalls = requests().filter((request) => request.action === "settings").length;
        fixture.holdNextLogin();
        click("login-tailscale");
        await until(() => calls().length === before + 1);
        assert($("main").getAttribute("aria-busy") === "true" && $("login-tailscale").disabled && $("copy-login").disabled);
        assert($("login-tailscale").getAttribute("aria-busy") === "true" && $("login-label").textContent === ShellscaledI18n.t("login.opening"));
        $("login-tailscale").click();
        click("tab-settings");
        await wait(50);
        assert(calls().length === before + 1 && notice() === globalNotice);
        fixture.releaseLogin();
        await idle();
        assert(requests().slice(firstRequest).some((request) => request.action === "status"));
        assert(!$("login-card").hidden && !$("login-tailscale").disabled && !$("copy-login").disabled);
        assert($("login-url").value === "https://example.test/login/generated");
        assert(!$("login-message").hidden && $("login-message").dataset.kind === "info");
        assert($("login-message").textContent === ShellscaledI18n.t("login.opened"));
        assert(checkbox.checked === confirmed && !checkbox.disabled);
        click("tab-overview");
        fixture.setLoginMode("error");
        click("login-tailscale");
        await until(() => calls().length === before + 2);
        await idle();
        assert(!$("login-tailscale").disabled && !$("copy-login").disabled && $("login-message").dataset.kind === "error");
        assert($("login-message").closest("#login-card") && $("login-message").textContent.includes(ShellscaledI18n.t("login.failed")));
        click("tab-command");
        click("tab-overview");
        assert(calls().length === before + 2 && notice() === globalNotice);
        assert(calls().every((request) => Object.keys(request).sort().join(",") === "action,locale"));
        assert(requests().filter((request) => request.action === "settings").length === settingsCalls);
        assert(checkbox.checked === confirmed && JSON.stringify(backend().settings) === JSON.stringify(original.settings));
        assert(location.href === url && fixture.navigationAttempts() === navigations && opens === 0 && storageIsLanguageOnly());
      } finally {
        fixture.releaseLogin();
        await idle();
        viewport.content = originalViewport;
        window.open = originalOpen;
        fixture.setState(original.state);
        fixture.setLoginURL(original.loginURL || "");
        fixture.setLoginMode("waiting");
        await language($("language").value);
      }
    });

    await test("login-existing-link-running-and-machine-approval", async () => {
      await idle();
      const original = backend();
      const calls = () => requests().filter((request) => request.action === "login");
      const writes = [];
      const restore = mockClipboard((value) => { writes.push(value); return Promise.resolve(); });
      try {
        fixture.setState("NeedsLogin");
        fixture.setLoginURL("https://example.test/login/existing");
        fixture.setLoginMode("running");
        await language($("language").value);
        click("tab-overview");
        assert(!$("login-card").hidden && !$("copy-login").disabled && !$("login-tailscale").disabled);
        const globalNotice = notice();
        await copy($("copy-login"), "copied");
        assert(writes.length === 1 && writes[0] === "https://example.test/login/existing");
        const before = calls().length;
        click("login-tailscale");
        await until(() => calls().length === before + 1);
        await idle();
        assert(backend().state === "Running" && $("login-card").hidden && $("login-tailscale").disabled && $("copy-login").disabled);
        assert(!$("login-url").value && !$("copy-login").dataset.copy);
        fixture.setLoginURL("https://example.test/login/stale");
        await language($("language").value);
        assert($("login-card").hidden && !$("login-url").value);
        $("login-tailscale").click();
        assert(calls().length === before + 1);
        fixture.setState("NeedsMachineAuth");
        await language($("language").value);
        assert($("login-card").hidden && $("login-tailscale").disabled && $("copy-login").disabled);
        assert($("state-title").textContent === ShellscaledI18n.t("state.NeedsMachineAuth.title"));
        assert($("state-description").textContent === ShellscaledI18n.t("state.NeedsMachineAuth.description"));
        $("login-tailscale").click();
        click("tab-settings");
        click("tab-overview");
        await idle();
        assert(calls().length === before + 1 && notice() === globalNotice);
        assert(JSON.stringify(backend().settings) === JSON.stringify(original.settings) && storageIsLanguageOnly());
      } finally {
        restore();
        fixture.setState(original.state);
        fixture.setLoginURL(original.loginURL || "");
        fixture.setLoginMode("waiting");
        await language($("language").value);
      }
    });

    await test("console-fixed-rpc-and-no-webview-navigation", async () => {
      await idle();
      click("tab-overview");
      const url = location.href;
      const before = fixture.navigationAttempts();
      const globalNotice = notice();
      const originalOpen = window.open;
      let opens = 0;
      window.open = () => { opens++; return null; };
      try {
        click("open-console");
        await idle();
        fixture.nextConsoleFails();
        click("open-console");
        await idle();
        const calls = requests().filter((request) => request.action === "console");
        assert(calls.length === 2 && calls.every((request) => Object.keys(request).sort().join(",") === "action,locale"));
        assert(location.href === url && fixture.navigationAttempts() === before && opens === 0);
        assert(!$("console-message").hidden && $("console-message").textContent.trim() && notice() === globalNotice);
      } finally { window.open = originalOpen; }
    });

    await test("about-fixed-rpc-local-feedback-and-keyboard", async () => {
      await idle();
      const button = $("open-about");
      const calls = () => requests().filter((request) => request.action === "about");
      const globalNotice = notice();
      const url = location.href;
      const navigations = fixture.navigationAttempts();
      const originalOpen = window.open;
      let opens = 0;
      window.open = () => { opens++; return null; };
      try {
        assert(calls().length === 0);
        for (const tab of ["devices", "settings", "command", "overview"]) {
          click("tab-" + tab);
          await idle();
        }
        assert(calls().length === 0);
        assert(button.tagName === "BUTTON" && button.type === "button" && button.tabIndex === 0);
        assert(button.getAttribute("aria-labelledby") === "about-label" && $("about-label").textContent.trim());
        assert(button.getAttribute("aria-describedby").split(/\s+/).includes("about-description"));
        assert(!button.closest("a") && !button.querySelector("a,[href],[onclick]"));
        fixture.holdNextAbout();
        click("open-about");
        await until(() => calls().length === 1);
        assert($("main").getAttribute("aria-busy") === "true" && button.disabled && button.getAttribute("aria-busy") === "true");
        assert($("about-description").textContent === ShellscaledI18n.t("about.opening") && $("about-message").hidden);
        button.click();
        click("tab-settings");
        await wait(50);
        assert(calls().length === 1 && notice() === globalNotice);
        fixture.releaseAbout();
        await idle();
        assert(!button.disabled && button.getAttribute("aria-busy") === "false" && $("about-message").hidden);
        click("tab-overview");
        assert(calls().length === 1);
        fixture.nextAboutFails();
        button.focus();
        assert(document.activeElement === button);
        fixture.pressEnter();
        await until(() => calls().length === 2);
        await idle();
        assert(!button.disabled && button.getAttribute("aria-busy") === "false");
        assert(!$("about-message").hidden && $("about-message").dataset.kind === "error");
        assert($("about-message").closest(".about-action") && $("about-message").textContent.includes(ShellscaledI18n.t("about.failed")));
        assert($("about-description").textContent === ShellscaledI18n.t("about.description"));
        click("tab-command");
        await idle();
        assert(notice() === globalNotice && calls().length === 2);
        click("tab-overview");
        assert(calls().every((request) => Object.keys(request).sort().join(",") === "action,locale"));
        assert(location.href === url && fixture.navigationAttempts() === navigations && opens === 0 && storageIsLanguageOnly());
      } finally {
        fixture.releaseAbout();
        await idle();
        window.open = originalOpen;
      }
    });

    await test("binary-check-is-explicit-and-busy", async () => {
      await idle();
      click("tab-settings");
      assert(binaryRequests().length === 0 && !$("binary-action").disabled);
      assert($("binary-update-card").querySelectorAll(".binary-actions button").length === 1, "single-binary-card-action");
      assert(!$("binary-check") && !$("binary-update") && binaryLabel() === ShellscaledI18n.t("binary.check"));
      await language("en");
      click("tab-overview");
      click("tab-settings");
      await idle();
      assert(binaryRequests().length === 0);
      const globalNotice = notice();
      fixture.holdNextBinary();
      try {
        click("binary-action");
        await until(() => binaryRequests().length === 1);
        assert($("main").getAttribute("aria-busy") === "true" && binaryMessage());
        assert($("binary-action").disabled && $("binary-action").getAttribute("aria-busy") === "true");
        assert(binaryLabel() === ShellscaledI18n.t("binary.checking"));
        const spinner = $("binary-action").querySelector(".button-spinner");
        assert(getComputedStyle(spinner).display !== "none" && getComputedStyle(spinner).animationName === "spin");
        assert(spinner.getBoundingClientRect().right < $("binary-action-label").getBoundingClientRect().left);
        assert($("binary-action").querySelector(".button-ripple"));
        $("binary-action").click();
        await wait(75);
        assert(binaryRequests().length === 1 && notice() === globalNotice);
      } finally { fixture.releaseBinary(); await idle(); }
      assert(!$("binary-action").disabled && binaryMessage());
      assert(binaryLabel() === ShellscaledI18n.t("binary.update") && $("binary-action").getAttribute("aria-busy") === "false");
      assert($("binary-current-version").textContent.includes("1.104.2"));
      assert(!$("binary-latest-row").hidden && $("binary-latest-version").textContent.includes("1.104.4"));
      assert(Object.keys(binaryRequests()[0]).sort().join(",") === "action,locale");
      assert(notice() === globalNotice);
    });

    await test("binary-first-click-during-status-runs-once", async () => {
      await checkBinary("current");
      fixture.setBinaryMode("newer");
      fixture.holdNextStatus();
      fixture.holdNextBinary();
      const beforeStatus = statusRequests().length;
      const beforeBinary = binaryRequests().length;
      try {
        document.dispatchEvent(new Event("visibilitychange"));
        await until(() => statusRequests().length === beforeStatus + 1);
        assert(!$("binary-action").disabled);
        click("binary-action");
        assert($("binary-action").disabled && binaryLabel() === ShellscaledI18n.t("binary.checking"));
        assert($("binary-action").getAttribute("aria-busy") === "true");
        $("binary-action").click();
        await wait(75);
        assert(binaryRequests().length === beforeBinary);
        fixture.releaseStatus();
        await until(() => binaryRequests().length === beforeBinary + 1);
        assert(binaryRequests().at(-1).action === "binary-check");
        const spinner = $("binary-action").querySelector(".button-spinner");
        const beforeRotation = getComputedStyle(spinner).transform;
        await wait(100);
        assert(getComputedStyle(spinner).transform !== beforeRotation);
      } finally {
        fixture.releaseStatus();
        fixture.releaseBinary();
        await idle();
      }
      assert(!$("binary-action").disabled && binaryLabel() === ShellscaledI18n.t("binary.update"));
    });

    await test("binary-channel-saves-without-checking-and-preserves-settings", async () => {
      await idle();
      const original = backend();
      const selected = $("binary-channel");
      const other = original.updateChannel === "stable" ? "preview" : "stable";
      const channelRequests = () => requests().filter((request) => request.action === "binary-channel");
      const networkActions = () => requests().filter((request) => ["binary-check", "binary-update"].includes(request.action)).length;
      const settingsCalls = requests().filter((request) => request.action === "settings").length;
      const globalNotice = notice();
      const firstChannelRequest = channelRequests().length;
      const checkbox = document.querySelector('[data-setting="shieldsUp"]');
      const confirmed = original.settings.shieldsUp;
      async function choose(value) {
        assert(!selected.disabled);
        const before = channelRequests().length;
        selected.value = value;
        selected.dispatchEvent(new Event("change", { bubbles: true }));
        await until(() => channelRequests().length > before);
        assert(channelRequests().length === before + 1);
      }
      assert([...selected.options].map((option) => option.value).filter(Boolean).sort().join(",") === "preview,stable");
      assert(selected.value === original.updateChannel && !$("binary-action").disabled);
      try {
        const before = networkActions();
        fixture.holdNextBinary();
        await choose(other);
        assert($("main").getAttribute("aria-busy") === "true" && selected.disabled && binaryMessage());
        assert($("binary-action").disabled);
        fixture.releaseBinary();
        await idle();
        assert(backend().updateChannel === other && selected.value === other);
        assert(!$("binary-action").disabled && binaryLabel() === ShellscaledI18n.t("binary.check") && $("binary-latest-row").hidden && binaryMessage());
        assert(networkActions() === before && notice() === globalNotice);
        await checkBinary("revision");
        assert($("binary-current-version").textContent === $("binary-latest-version").textContent, "revision-version-display");
        assert(!$("binary-action").disabled);
        const revisionChecked = networkActions();
        await language("zh-TW");
        assert(selected.value === other && checkbox.checked === confirmed && !checkbox.disabled);
        assert(networkActions() === revisionChecked && !$("binary-action").disabled && storageIsLanguageOnly(), "channel-after-language");

        await checkBinary("newer");
        assert(!$("binary-action").disabled);
        const checked = networkActions();
        fixture.nextChannelFails();
        await choose(original.updateChannel);
        await idle();
        assert(selected.value === other && backend().updateChannel === other, "failed-channel-restoration");
        assert(!$("binary-action").disabled && binaryLabel() === ShellscaledI18n.t("binary.check") && $("binary-latest-row").hidden);
        assert(binaryMessage() && $("binary-message").dataset.kind === "error" && notice() === globalNotice);
        assert(networkActions() === checked && checkbox.checked === confirmed && !checkbox.disabled);
        assert(JSON.stringify(backend().settings) === JSON.stringify(original.settings));
        assert(JSON.stringify(backend().ips) === JSON.stringify(original.ips) && backend().state === original.state);
        assert(requests().filter((request) => request.action === "settings").length === settingsCalls);
        assert(channelRequests().slice(firstChannelRequest).every((request) =>
          Object.keys(request).sort().join(",") === "action,channel,locale" && ["stable", "preview"].includes(request.channel)));
        assert(storageIsLanguageOnly());
      } finally {
        fixture.releaseBinary();
        await idle();
        if (backend().updateChannel !== original.updateChannel) {
          await choose(original.updateChannel);
          await idle();
        }
      }
    });

    await test("binary-current-unsupported-and-check-error", async () => {
      const globalNotice = notice();
      for (const mode of ["current", "unsupported", "error"]) {
        await checkBinary(mode);
        assert(!$("binary-action").disabled && binaryLabel() === ShellscaledI18n.t("binary.check") && binaryMessage());
        if (mode === "unsupported") assert($("binary-latest-row").hidden);
        const before = binaryRequests().length;
        click("binary-action");
        await until(() => binaryRequests().length > before);
        await idle();
        assert(binaryRequests().length === before + 1 && binaryRequests().at(-1).action === "binary-check" && notice() === globalNotice);
      }
    });

    await test("binary-update-error-is-local", async () => {
      await checkBinary("update-error");
      const original = backend();
      const globalNotice = notice();
      const settingsCalls = requests().filter((request) => request.action === "settings").length;
      const before = binaryRequests().length;
      click("binary-action");
      await until(() => binaryRequests().length > before);
      await idle();
      assert(binaryRequests().length === before + 1 && binaryRequests().at(-1).action === "binary-update");
      assert(Object.keys(binaryRequests().at(-1)).sort().join(",") === "action,locale");
      assert(binaryMessage() && notice() === globalNotice && !$("binary-action").disabled);
      assert(binaryLabel() === ShellscaledI18n.t("binary.update"));
      assert(JSON.stringify(backend()) === JSON.stringify(original));
      assert(requests().filter((request) => request.action === "settings").length === settingsCalls);
    });

    await test("binary-update-keeps-settings-identity-and-navigation", async () => {
      await checkBinary("newer");
      const original = backend();
      const checkbox = document.querySelector('[data-setting="shieldsUp"]');
      const confirmed = original.settings.shieldsUp;
      const settingsCalls = requests().filter((request) => request.action === "settings").length;
      const globalNotice = notice();
      const url = location.href;
      const navigations = fixture.navigationAttempts();
      const originalOpen = window.open;
      let opens = 0;
      window.open = () => { opens++; return null; };
      fixture.holdNextBinary();
      try {
        const before = binaryRequests().length;
        click("binary-action");
        await until(() => binaryRequests().length > before);
        assert($("main").getAttribute("aria-busy") === "true" && binaryMessage());
        assert($("binary-action").disabled && binaryLabel() === ShellscaledI18n.t("binary.updating"));
        assert(getComputedStyle($("binary-action").querySelector(".button-spinner")).display !== "none");
        $("binary-action").click();
        await wait(50);
        assert(binaryRequests().length === before + 1);
        fixture.releaseBinary();
        await idle();
        assert(!$("binary-action").disabled && binaryLabel() === ShellscaledI18n.t("binary.check") && binaryMessage() && notice() === globalNotice);
        assert($("binary-current-version").textContent.includes("1.104.4"));
        assert(checkbox.checked === confirmed && !checkbox.disabled);
        assert(JSON.stringify(backend().settings) === JSON.stringify(original.settings));
        assert(JSON.stringify(backend().ips) === JSON.stringify(original.ips) && backend().state === original.state);
        assert(requests().filter((request) => request.action === "settings").length === settingsCalls);
        assert(Object.keys(binaryRequests().at(-1)).sort().join(",") === "action,locale");
        assert(location.href === url && fixture.navigationAttempts() === navigations && opens === 0);
        assert(storageIsLanguageOnly());
      } finally {
        fixture.releaseBinary();
        await idle();
        window.open = originalOpen;
      }
    });

    await test("output-is-text-and-storage-is-private", async () => {
      click("tab-command");
      $("command-input").value = "tailscale status";
      $("command-form").requestSubmit();
      await idle();
      assert(!$("fixture-injected") && $("command-output").textContent.includes("<img"));
      assert(storageIsLanguageOnly());
      const before = requests().length;
      $("command-input").value = "id";
      $("command-form").requestSubmit();
      await wait(75);
      assert(requests().length === before);
      click("clear-command");
    });

    await test("three-language-four-tab-narrow-layout", async () => {
      const viewport = document.querySelector('meta[name="viewport"]');
      const original = viewport.content;
      try {
        for (const width of [320, 360]) {
          viewport.content = "width=" + width;
          await until(() => Math.abs(window.innerWidth - width) <= 2);
          for (const locale of locales) {
            await language(locale);
            for (const tab of ["overview", "devices", "settings", "command"]) {
              click("tab-" + tab);
              await idle();
              await wait(40);
              assert(document.documentElement.scrollWidth <= window.innerWidth + 1);
              assert(document.body.scrollWidth <= window.innerWidth + 1);
            }
          }
        }
      } finally {
        viewport.content = original;
        await wait(100);
      }
    });

    await test("removed-compatibility-and-developer-copy", () => {
      assert(!document.querySelector(".compatibility-card,.info-note,.privacy-note,.section-intro"));
      assert(!/VpnService|netfilter|metamodule|base64|\bRPC\b|compatibility|网络兼容|網路相容|开发说明|開發說明/i.test(document.body.textContent));
    });
    click("tab-overview");
  } catch (_) {
    fixture.report("suite-initialization", false);
  } finally {
    fixture.finish();
  }
})();
