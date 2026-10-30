/* Executed only inside the fixture's Android WebView, against the real packaged assets. */
"use strict";
(async () => {
  const $ = (id) => document.getElementById(id);
  const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  const requests = () => JSON.parse(fixture.requestsJSON());
  const backend = () => JSON.parse(fixture.statusJSON());
  const assert = (value) => { if (!value) throw new Error("assertion"); };
  const rawKey = /^(?:app|preview|tab|overview|devices|copy|console|about|binary|state|service|freshness|connection|login|settings|language|command|notice|error|taildrop)\.[A-Za-z0-9.]+$/;
  async function until(predicate) {
    const end = Date.now() + 6000;
    while (!predicate()) {
      if (Date.now() > end) throw new Error("timeout");
      await wait(25);
    }
  }
  async function idle() {
    await until(() => $("main") && $("main").getAttribute("aria-busy") === "false");
    await wait(30);
  }
  async function test(name, body) {
    try { await body(); fixture.report(name, true); }
    catch (_) { fixture.report(name, false); }
  }
  function click(id) {
    assert($(id) && !$(id).disabled);
    $(id).click();
  }
  async function refresh() {
    await idle();
    const before = requests().length;
    click("refresh");
    await until(() => requests().length > before);
    await idle();
  }
  async function language(value) {
    await idle();
    const before = requests().length;
    $("language").value = value;
    $("language").dispatchEvent(new Event("change", { bubbles: true }));
    await until(() => requests().length > before);
    await idle();
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
  async function checkBinary(mode) {
    fixture.setBinaryMode(mode);
    const before = binaryRequests().length;
    click("binary-check");
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
    await until(() => !button.dataset.copyPending && !button.disabled && button.dataset.copyState === state);
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
      "devices-message", "console-label", "about-description", "login-label", "binary-check-label", "binary-update-label", "binary-message",
      "taildrop-message", "taildrop-save-label", "taildrop-receive-label"]);

    await test("bridge-is-fixture", () => {
      assert(location.origin === "https://mui.kernelsu.org" && window.top === window);
      assert($("preview-notice").hidden && requests().length > 0);
      assert([...$("language").options].map((option) => option.value).join(",") === "system,zh-CN,zh-TW,en");
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
      try {
        fixture.holdNextSettings();
        click("shields-up");
        await until(() => settingsRequests().length === before + 1);
        assert($("main").getAttribute("aria-busy") === "true" && controls.every((control) => control.disabled));
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
        assert(pending.at(-1).value === value && $("copy-login").dataset.copyPending && $("copy-login").disabled);
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
        assert(control.dataset.copyPending && control.disabled && !control.dataset.copyState);
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
      assert(binaryRequests().length === 0 && $("binary-update").disabled);
      await language("en");
      click("tab-overview");
      click("tab-settings");
      await idle();
      assert(binaryRequests().length === 0);
      const globalNotice = notice();
      fixture.holdNextBinary();
      try {
        click("binary-check");
        await until(() => binaryRequests().length === 1);
        assert($("main").getAttribute("aria-busy") === "true" && binaryMessage());
        assert($("binary-check").disabled && $("binary-update").disabled);
        $("binary-check").click();
        $("binary-update").click();
        await wait(75);
        assert(binaryRequests().length === 1 && notice() === globalNotice);
      } finally { fixture.releaseBinary(); await idle(); }
      assert(!$("binary-update").disabled && binaryMessage());
      assert($("binary-current-version").textContent.includes("1.104.2"));
      assert(!$("binary-latest-row").hidden && $("binary-latest-version").textContent.includes("1.104.4"));
      assert(Object.keys(binaryRequests()[0]).sort().join(",") === "action,locale");
      assert(notice() === globalNotice);
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
      assert(selected.value === original.updateChannel && !$("binary-update").disabled);
      try {
        const before = networkActions();
        fixture.holdNextBinary();
        await choose(other);
        assert($("main").getAttribute("aria-busy") === "true" && selected.disabled && binaryMessage());
        assert($("binary-check").disabled && $("binary-update").disabled);
        fixture.releaseBinary();
        await idle();
        assert(backend().updateChannel === other && selected.value === other);
        assert($("binary-update").disabled && $("binary-latest-row").hidden && binaryMessage());
        assert(networkActions() === before && notice() === globalNotice);
        await checkBinary("revision");
        assert($("binary-current-version").textContent === $("binary-latest-version").textContent);
        assert(!$("binary-update").disabled);
        const revisionChecked = networkActions();
        await language("zh-TW");
        assert(selected.value === other && checkbox.checked === confirmed && !checkbox.disabled);
        assert(networkActions() === revisionChecked && !$("binary-update").disabled && storageIsLanguageOnly());

        await checkBinary("newer");
        assert(!$("binary-update").disabled);
        const checked = networkActions();
        fixture.nextChannelFails();
        await choose(original.updateChannel);
        await idle();
        assert(selected.value === other && backend().updateChannel === other);
        assert($("binary-update").disabled && $("binary-latest-row").hidden);
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
        assert($("binary-update").disabled && !$("binary-check").disabled && binaryMessage());
        if (mode === "unsupported") assert($("binary-latest-row").hidden);
        const before = binaryRequests().length;
        $("binary-update").click();
        await wait(50);
        assert(binaryRequests().length === before && notice() === globalNotice);
      }
    });

    await test("binary-update-error-is-local", async () => {
      await checkBinary("update-error");
      const original = backend();
      const globalNotice = notice();
      const settingsCalls = requests().filter((request) => request.action === "settings").length;
      const before = binaryRequests().length;
      click("binary-update");
      await until(() => binaryRequests().length > before);
      await idle();
      assert(binaryRequests().length === before + 1 && binaryRequests().at(-1).action === "binary-update");
      assert(Object.keys(binaryRequests().at(-1)).sort().join(",") === "action,locale");
      assert(binaryMessage() && notice() === globalNotice && !$("binary-check").disabled);
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
        click("binary-update");
        await until(() => binaryRequests().length > before);
        assert($("main").getAttribute("aria-busy") === "true" && binaryMessage());
        assert($("binary-update").disabled && $("binary-check").disabled);
        $("binary-update").click();
        await wait(50);
        assert(binaryRequests().length === before + 1);
        fixture.releaseBinary();
        await idle();
        assert($("binary-update").disabled && binaryMessage() && notice() === globalNotice);
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
