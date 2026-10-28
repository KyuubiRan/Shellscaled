/* Documentation-only synthetic state, prepared inside Android WebView without running tests. */
"use strict";
(async () => {
  const $ = (id) => document.getElementById(id);
  const config = window.shellscaledScreenshot;
  const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  async function until(predicate) {
    const end = Date.now() + 6000;
    while (!predicate()) {
      if (Date.now() > end) throw new Error("timeout");
      await wait(25);
    }
  }
  const idle = () => until(() => $("main") && $("main").getAttribute("aria-busy") === "false");
  try {
    await until(() => window.ShellscaledI18n && $("preview-notice") && $("preview-notice").hidden);
    await idle();
    const requestsBefore = JSON.parse(fixture.requestsJSON()).length;
    $("language").value = config.locale;
    $("language").dispatchEvent(new Event("change", { bubbles: true }));
    await until(() => JSON.parse(fixture.requestsJSON()).length > requestsBefore);
    await idle();
    $("tab-" + (config.tab === "login" ? "overview" : config.tab)).click();
    await idle();
    if (config.tab === "login") await until(() => !$("login-card").hidden && !$("login-tailscale").disabled &&
      $("copy-login").disabled && !$("login-url").value && $("state-title").textContent === ShellscaledI18n.t("state.NeedsLogin.title"));
    if (config.tab === "overview") await until(() => $("open-about") && !$("open-about").disabled &&
      $("about-label").textContent === ShellscaledI18n.t("about.title") && $("about-message").hidden);
    if (config.tab === "devices") await until(() => document.querySelector("#device-list .device-card"));
    if (config.tab === "settings") await until(() => !$("save-settings") &&
      $("panel-settings").querySelector(".card").contains($("language")) &&
      [...document.querySelectorAll("[data-setting]")].every((control) => !control.disabled));
    if (config.tab === "command") {
      $("command-input").value = "tailscale version";
      $("command-form").requestSubmit();
      await idle();
    }
    if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
    window.scrollTo(0, 0);
    if (config.tab === "login") $("login-card").scrollIntoView({ block: "nearest" });
    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    await wait(800);
    fixture.screenshotReady();
  } catch (_) {
    fixture.screenshotError();
  } finally {
    delete window.shellscaledScreenshot;
  }
})();
