/* Executed only inside the fixture's Android WebView, against the real packaged assets. */
"use strict";
(async () => {
  const $ = (id) => document.getElementById(id);
  const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  const requests = () => JSON.parse(fixture.requestsJSON());
  const backend = () => JSON.parse(fixture.statusJSON());
  const assert = (value) => { if (!value) throw new Error("assertion"); };
  const rawKey = /^(?:app|preview|tab|overview|state|service|freshness|connection|login|settings|language|command|notice|error)\.[A-Za-z0-9.]+$/;
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

  try {
    await until(() => window.ShellscaledI18n && $("main") && $("preview-notice").hidden);
    await idle();
    localStorage.clear(); // This standalone fixture has its own app/origin storage.
    const translations = JSON.parse(fixture.translationsJSON());
    const locales = ["zh-CN", "zh-TW", "en"];
    const keys = Object.keys(translations.en).sort();
    const dynamicIDs = new Set(["state-title", "connection-label", "service-status", "freshness",
      "exit-lan-description", "settings-hint", "save-settings", "run-command", "command-output"]);

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
          assert(ShellscaledI18n.t(key, { time: "12:34", code: 0, count: 100000 }) ===
            value.replace(/\{time\}/g, "12:34").replace(/\{code\}/g, "0").replace(/\{count\}/g, "100000"));
        }
        for (const element of document.querySelectorAll("[data-i18n]")) {
          const key = element.dataset.i18n;
          assert(Object.hasOwn(translations[locale], key));
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

    await test("language-keeps-edits-and-save-is-masked", async () => {
      fixture.setState("Running");
      fixture.setAcceptRoutes(false);
      await refresh();
      click("tab-settings");
      const before = backend().settings.shieldsUp;
      click("shields-up");
      assert(!$("save-settings").disabled);
      for (const locale of locales) {
        await language(locale);
        assert($("shields-up").checked === !before && !$("save-settings").disabled);
      }
      fixture.setAcceptRoutes(true); // An unrelated concurrent external preference edit.
      click("tab-overview");
      await refresh();
      click("tab-settings");
      assert($("accept-routes").checked && $("shields-up").checked === !before);
      click("save-settings");
      await idle();
      const save = requests().filter((request) => request.action === "settings").at(-1);
      assert(save && Object.keys(save.settings).join(",") === "shieldsUp" && save.settings.shieldsUp === !before);
      assert(backend().settings.acceptRoutes && $("save-settings").disabled);
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

    await test("english-layout-no-overflow", async () => {
      await language("en");
      for (const tab of ["overview", "settings", "command"]) {
        click("tab-" + tab);
        await wait(40);
        assert(document.documentElement.scrollWidth <= window.innerWidth + 1);
        assert(document.body.scrollWidth <= window.innerWidth + 1);
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
