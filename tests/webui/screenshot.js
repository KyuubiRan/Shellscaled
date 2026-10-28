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
    $("tab-" + config.tab).click();
    if (config.tab === "command") {
      $("command-input").value = "tailscale version";
      $("command-form").requestSubmit();
      await idle();
    }
    if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
    window.scrollTo(0, 0);
    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    fixture.screenshotReady();
  } catch (_) {
    fixture.screenshotError();
  } finally {
    delete window.shellscaledScreenshot;
  }
})();
