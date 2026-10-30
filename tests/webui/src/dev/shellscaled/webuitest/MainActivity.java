package dev.shellscaled.webuitest;

import android.app.Activity;
import android.graphics.Bitmap;
import android.net.Uri;
import android.os.Bundle;
import android.os.SystemClock;
import android.util.Base64;
import android.util.Log;
import android.view.KeyEvent;
import android.webkit.ConsoleMessage;
import android.webkit.JavascriptInterface;
import android.webkit.WebChromeClient;
import android.webkit.WebResourceRequest;
import android.webkit.WebResourceResponse;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import java.io.ByteArrayInputStream;
import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.nio.charset.StandardCharsets;
import java.util.Arrays;
import java.util.Collections;
import java.util.HashSet;
import java.util.Set;
import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

/** No root, network permission, process execution, or access to module/device state. */
public final class MainActivity extends Activity {
    private static final String TAG = "ShellscaledUiTest";
    private static final String ORIGIN = "https://mui.kernelsu.org";
    private static final String RPC = "/data/adb/modules/shellscaled/bin/shellscaled-web rpc ";
    private static final Set<String> ASSETS = new HashSet<>(Arrays.asList(
            "index.html", "app.js", "i18n.js", "styles.css"));
    private final Object guard = new Object();
    private final JSONArray requests = new JSONArray();
    private String state = "Running";
    private String loginURL = "";
    private String loginMode = "waiting";
    private boolean holdNextLogin;
    private Runnable loginReply;
    private boolean acceptRoutes;
    private boolean shieldsUp;
    private boolean allowLAN;
    private boolean nextSettingsFails;
    private boolean nextSettingsResponseFails;
    private boolean holdNextSettings;
    private Runnable settingsReply;
    private boolean holdNextStatus;
    private String statusMode = "normal";
    private Runnable statusReply;
    private boolean nextUpStarts;
    private boolean nextConsoleFails;
    private boolean nextAboutFails;
    private boolean holdNextAbout;
    private Runnable aboutReply;
    private String updateChannel = "stable";
    private boolean nextChannelFails;
    private String binaryMode = "newer";
    private String binaryVersion = "1.104.2";
    private boolean binaryChecked;
    private boolean holdNextBinary;
    private Runnable binaryReply;
    private boolean testsStarted;
    private boolean finished;
    private int passed;
    private int failed;
    private int navigationAttempts;
    private String screenshotTab;
    private String screenshotLocale;
    private WebView web;

    @Override public void onCreate(Bundle saved) {
        super.onCreate(saved);
        screenshotTab = getIntent().getStringExtra("screenshot");
        screenshotLocale = getIntent().getStringExtra("locale");
        if (screenshotLocale == null) screenshotLocale = "zh-CN";
        if (screenshotTab != null) {
            if (!Arrays.asList("overview", "devices", "settings", "command", "login").contains(screenshotTab) ||
                    !Arrays.asList("zh-CN", "zh-TW", "en").contains(screenshotLocale)) {
                Log.i(TAG, "SCREENSHOT ERROR invalid-options");
                finish();
                return;
            }
            acceptRoutes = true;
            if ("login".equals(screenshotTab)) state = "NeedsLogin";
            Log.i(TAG, "SCREENSHOT START " + screenshotTab + " " + screenshotLocale);
        } else {
            Log.i(TAG, "START run=" + SystemClock.elapsedRealtime());
        }
        web = new WebView(this);
        web.getSettings().setJavaScriptEnabled(true);
        web.getSettings().setDomStorageEnabled(true);
        web.getSettings().setUseWideViewPort(true);
        web.getSettings().setAllowFileAccess(false);
        web.getSettings().setAllowContentAccess(false);
        web.getSettings().setJavaScriptCanOpenWindowsAutomatically(false);
        web.getSettings().setSupportMultipleWindows(false);
        web.addJavascriptInterface(new FakeKsu(), "ksu");
        web.addJavascriptInterface(new TestControl(), "fixture");
        web.setWebChromeClient(new WebChromeClient() {
            @Override public boolean onConsoleMessage(ConsoleMessage message) {
                if (message.messageLevel() == ConsoleMessage.MessageLevel.ERROR) record("console-error", false);
                return true;
            }
        });
        web.setWebViewClient(new WebViewClient() {
            @Override public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
                synchronized (guard) { navigationAttempts++; }
                return true; // The fixture never opens links, even on the synthetic asset origin.
            }
            @Override public WebResourceResponse shouldInterceptRequest(WebView view, WebResourceRequest request) {
                Uri uri = request.getUrl();
                String path = uri.getPath();
                String name = "/".equals(path) ? "index.html" :
                        path != null && path.startsWith("/") ? path.substring(1) : "";
                if (!"GET".equals(request.getMethod()) || !"https".equals(uri.getScheme()) ||
                        !"mui.kernelsu.org".equals(uri.getHost()) || uri.getPort() != -1 ||
                        uri.getEncodedQuery() != null || !ASSETS.contains(name)) return blocked();
                try {
                    String mime = name.endsWith(".html") ? "text/html" :
                            name.endsWith(".css") ? "text/css" : "application/javascript";
                    return new WebResourceResponse(mime, "UTF-8", 200, "OK",
                            Collections.singletonMap("Cache-Control", "no-store"), getAssets().open("webroot/" + name));
                } catch (IOException error) {
                    return blocked();
                }
            }
            @Override public void onPageStarted(WebView view, String url, Bitmap icon) {
                if (!url.equals(ORIGIN + "/index.html")) record("unexpected-navigation", false);
            }
            @Override public void onPageFinished(WebView view, String url) {
                if (testsStarted || !url.equals(ORIGIN + "/index.html")) return;
                testsStarted = true;
                try {
                    if (screenshotTab == null) {
                        view.evaluateJavascript(readAsset("fixture/tests.js"), null);
                    } else {
                        String config = "window.shellscaledScreenshot = {tab:" + JSONObject.quote(screenshotTab) +
                                ",locale:" + JSONObject.quote(screenshotLocale) + "};\n";
                        view.evaluateJavascript(config + readAsset("fixture/screenshot.js"), null);
                    }
                } catch (IOException error) {
                    record("test-asset", false);
                    complete();
                }
            }
        });
        setContentView(web);
        web.loadUrl(ORIGIN + "/index.html");
    }

    private static WebResourceResponse blocked() {
        return new WebResourceResponse("text/plain", "UTF-8", 403, "Blocked",
                Collections.emptyMap(), new ByteArrayInputStream(new byte[0]));
    }

    private String readAsset(String path) throws IOException {
        try (InputStream input = getAssets().open(path); ByteArrayOutputStream output = new ByteArrayOutputStream()) {
            byte[] buffer = new byte[8192];
            for (int n; (n = input.read(buffer)) != -1;) output.write(buffer, 0, n);
            return new String(output.toByteArray(), StandardCharsets.UTF_8);
        }
    }

    private JSONObject status() throws JSONException {
        JSONObject settings = new JSONObject().put("acceptRoutes", acceptRoutes).put("shieldsUp", shieldsUp)
                .put("exitNodeAllowLanAccess", allowLAN);
        JSONArray addresses = new JSONArray();
        if (!"login".equals(screenshotTab)) addresses.put("100.64.0.10").put("fd7a:115c:a1e0::10");
        JSONObject result = new JSONObject().put("ok", true).put("state", state)
                .put("version", binaryVersion)
                .put("daemonRunning", true).put("exitNodeConfigured", true).put("exitCode", 0)
                .put("ips", addresses)
                .put("settings", settings).put("updateChannel", updateChannel);
        if (!loginURL.isEmpty()) result.put("loginURL", loginURL);
        return result;
    }

    private JSONObject binaryStatus() throws JSONException {
        boolean supported = !Arrays.asList("unsupported", "error").contains(binaryMode);
        String latest = !supported ? "" : Arrays.asList("current", "revision").contains(binaryMode) ? binaryVersion : "1.104.4";
        JSONObject result = new JSONObject().put("currentVersion", binaryVersion).put("latestVersion", latest)
                .put("checked", binaryChecked).put("supported", supported)
                .put("updateAvailable", binaryChecked && supported && ("revision".equals(binaryMode) || !binaryVersion.equals(latest)))
                .put("channel", updateChannel);
        if ("unsupported".equals(binaryMode)) result.put("reason", "no-compatible-release");
        return result;
    }

    private static JSONObject device(String id, String name, String dns, String[] ips,
            boolean online, String os, boolean self) throws JSONException {
        JSONArray addresses = new JSONArray();
        for (String ip : ips) addresses.put(ip);
        return new JSONObject().put("id", id).put("name", name).put("dnsName", dns)
                .put("ips", addresses).put("online", online).put("os", os).put("self", self);
    }

    private JSONArray devices() throws JSONException {
        JSONArray values = new JSONArray()
                .put(device("fixture-self", "demo-phone", "demo-phone.docs.example",
                        new String[]{"100.64.0.10", "fd7a:115c:a1e0::10"}, true, "android", true))
                .put(device("fixture-workstation", "workstation", "workstation.docs.example",
                        new String[]{"100.64.0.20", "fd7a:115c:a1e0::20"}, true, "linux", false))
                .put(device("fixture-offline", "travel-laptop", "travel-laptop.docs.example",
                        new String[]{"100.64.0.30"}, false, "windows", false)
                        .put("lastSeen", "2026-01-01T12:00:00Z"));
        if (screenshotTab != null) return values;
        return values
                .put(device("fixture-untrusted", "<img id=\"fixture-device-injected\" src=x onerror=\"alert(1)\">",
                        "<svg id=\"fixture-dns-injected\" onload=\"alert(1)\">.docs.example",
                        new String[]{"100.64.0.40"}, true, "<script id=fixture-os-injected>example</script>", false))
                .put(device("fixture-long", "documentation-workstation-with-a-long-name-that-must-wrap-without-changing-the-copied-value",
                        "documentation-workstation-with-a-long-name-that-must-wrap.without-changing-the-copied-value.docs.example",
                        new String[]{"100.64.0.50", "fd7a:115c:a1e0::50"}, true, "darwin", false))
                .put(device("fixture-no-ip", "initializing-device", "", new String[]{}, false, "freebsd", false))
                .put(device("fixture-unnamed", "", "", new String[]{"100.64.0.70"}, false, "", false));
    }

    private void record(String name, boolean success) {
        synchronized (guard) {
            if (finished) return;
            if (success) passed++; else failed++;
            // Log only case identifiers and counts; never browser contents or request payloads.
            String safe = name.replaceAll("[^a-zA-Z0-9_.-]", "_");
            Log.i(TAG, (success ? "PASS " : "FAIL ") + safe);
        }
    }

    private void complete() {
        synchronized (guard) {
            if (finished) return;
            finished = true;
            Log.i(TAG, "DONE pass=" + passed + " fail=" + failed);
        }
    }

    public final class FakeKsu {
        @JavascriptInterface public void exec(String command, String options, String callback) {
            if (!callback.matches("shellscaled_rpc_[0-9]+_[0-9]+")) {
                record("bridge-callback", false);
                return;
            }
            JSONObject result;
            try {
                if (!command.startsWith(RPC) || !"{}".equals(options)) throw new JSONException("envelope");
                String encoded = command.substring(RPC.length());
                if (encoded.length() > 8192 || !encoded.matches("[A-Za-z0-9+/]+={0,2}")) throw new JSONException("encoding");
                JSONObject request = new JSONObject(new String(Base64.decode(encoded, Base64.DEFAULT), StandardCharsets.UTF_8));
                if (!Arrays.asList("system", "zh-CN", "zh-TW", "en").contains(request.optString("locale"))) {
                    throw new JSONException("locale");
                }
                synchronized (guard) {
                    String action = request.getString("action");
                    if (Arrays.asList("devices", "exit-nodes", "console", "about", "login", "binary-check", "binary-update").contains(action) && request.length() != 2) {
                        throw new JSONException("extra-fields");
                    }
                    if ("binary-channel".equals(action) && (request.length() != 3 ||
                            !Arrays.asList("stable", "preview").contains(request.getString("channel")))) {
                        throw new JSONException("channel");
                    }
                    requests.put(request);
                    switch (action) {
                        case "status":
                            JSONObject snapshot = status();
                            if ("error".equals(statusMode)) {
                                snapshot = new JSONObject().put("ok", false)
                                        .put("error", "Fixture status unavailable").put("exitCode", 1);
                            } else if ("stopped".equals(statusMode) || "service-error".equals(statusMode)) {
                                snapshot.put("state", "stopped".equals(statusMode) ? "ServiceStopped" : "ServiceError")
                                        .put("daemonRunning", false).put("settings", new JSONObject()
                                                .put("acceptRoutes", false).put("shieldsUp", false)
                                                .put("exitNodeAllowLanAccess", false));
                            } else if ("invalid-settings".equals(statusMode)) {
                                snapshot.put("settings", new JSONObject());
                            }
                            if (holdNextStatus) {
                                holdNextStatus = false;
                                JSONObject heldSnapshot = snapshot;
                                statusReply = () -> reply(callback, heldSnapshot);
                            } else reply(callback, snapshot);
                            return;
                        case "login":
                            boolean loginOK = !"error".equals(loginMode);
                            if (loginOK && "NeedsLogin".equals(state)) {
                                if ("running".equals(loginMode)) { state = "Running"; loginURL = ""; }
                                else if (loginURL.isEmpty()) loginURL = "https://example.test/login/generated";
                            }
                            JSONObject login = loginOK ? status() : new JSONObject().put("ok", false)
                                    .put("error", "Fixture sign-in unavailable").put("exitCode", 1);
                            if (holdNextLogin) {
                                holdNextLogin = false;
                                loginReply = () -> reply(callback, login);
                            } else reply(callback, login);
                            return;
                        case "about":
                            // Native code owns the fixed project URL. This fixture never opens it.
                            JSONObject about = new JSONObject().put("ok", !nextAboutFails);
                            if (nextAboutFails) about.put("error", "Fixture project home unavailable").put("exitCode", 1);
                            nextAboutFails = false;
                            if (holdNextAbout) {
                                holdNextAbout = false;
                                aboutReply = () -> reply(callback, about);
                            } else reply(callback, about);
                            return;
                        case "binary-channel":
                            boolean channelOK = !nextChannelFails;
                            nextChannelFails = false;
                            binaryChecked = false;
                            if (channelOK) updateChannel = request.getString("channel");
                            JSONObject channel = new JSONObject().put("ok", channelOK).put("updateChannel", updateChannel);
                            if (!channelOK) channel.put("error", "Fixture channel save unavailable").put("exitCode", 1);
                            replyOrHold(callback, channel);
                            return;
                        case "binary-check":
                        case "binary-update":
                            boolean updating = "binary-update".equals(action);
                            boolean binaryOK = !"error".equals(binaryMode) &&
                                    (!updating || binaryChecked && !"unsupported".equals(binaryMode) && !"update-error".equals(binaryMode));
                            if (!updating) binaryChecked = binaryOK;
                            if (updating && binaryOK) {
                                if (!"revision".equals(binaryMode)) binaryVersion = "1.104.4";
                                binaryMode = "current";
                            }
                            JSONObject binary = new JSONObject().put("ok", binaryOK).put("binary", binaryStatus());
                            if (!binaryOK) binary.put("error", "Fixture binary action unavailable").put("exitCode", 1);
                            replyOrHold(callback, binary);
                            return;
                        case "devices":
                        case "exit-nodes":
                            reply(callback, new JSONObject().put("ok", true).put("devices", devices()));
                            return;
                        case "console":
                            // Native code owns the fixed external console URL. The fixture opens nothing.
                            JSONObject console = new JSONObject().put("ok", !nextConsoleFails);
                            if (nextConsoleFails) console.put("error", "Fixture console unavailable").put("exitCode", 1);
                            nextConsoleFails = false;
                            reply(callback, console);
                            return;
                        case "down": state = "Stopped"; break;
                        case "up": state = nextUpStarts ? "Starting" : "Running"; nextUpStarts = false; break;
                        case "settings":
                            JSONObject patch = request.getJSONObject("settings");
                            if (request.length() != 3 || patch.length() != 1) throw new JSONException("settings-mask");
                            String settingKey = patch.keys().next();
                            if (!Arrays.asList("acceptRoutes", "shieldsUp", "exitNodeAllowLanAccess").contains(settingKey)) throw new JSONException("settings-key");
                            boolean settingsOK = !nextSettingsFails;
                            nextSettingsFails = false;
                            if (settingsOK) {
                                if (patch.has("acceptRoutes")) acceptRoutes = patch.getBoolean("acceptRoutes");
                                if (patch.has("shieldsUp")) shieldsUp = patch.getBoolean("shieldsUp");
                                if (patch.has("exitNodeAllowLanAccess")) allowLAN = patch.getBoolean("exitNodeAllowLanAccess");
                            }
                            // A lost/failed response can occur after the daemon applied the edit.
                            if (nextSettingsResponseFails) settingsOK = false;
                            nextSettingsResponseFails = false;
                            JSONObject settingsResult = settingsOK ? status() : new JSONObject().put("ok", false)
                                    .put("error", "Fixture settings unavailable").put("exitCode", 1);
                            if (holdNextSettings) {
                                holdNextSettings = false;
                                settingsReply = () -> reply(callback, settingsResult);
                            } else reply(callback, settingsResult);
                            return;
                        case "command":
                            result = new JSONObject().put("ok", true).put("exitCode", 0)
                                    .put("stdout", screenshotTab == null ?
                                            "fixture <img id=\"fixture-injected\" src=\"invalid\" onerror=\"alert(1)\">" :
                                            "tailscale version".equals(request.optString("command")) ? "1.104.2\n" : "");
                            reply(callback, result);
                            return;
                        default: throw new JSONException("action");
                    }
                    result = status();
                }
            } catch (JSONException | IllegalArgumentException error) {
                record("bridge-envelope", false);
                result = new JSONObject();
                try { result.put("ok", false).put("error", "Fixture rejected request").put("exitCode", 1); }
                catch (JSONException impossible) { throw new IllegalStateException(impossible); }
            }
            reply(callback, result);
        }

        private void replyOrHold(String callback, JSONObject result) {
            if (holdNextBinary) {
                holdNextBinary = false;
                binaryReply = () -> reply(callback, result);
            } else reply(callback, result);
        }

        private void reply(String callback, JSONObject result) {
            String script = "window[" + JSONObject.quote(callback) + "](" + (result.optBoolean("ok") ? "0" : "1") +
                    "," + JSONObject.quote(result.toString()) + ",\"\")";
            web.postDelayed(() -> web.evaluateJavascript(script, null), 30);
        }
    }

    public final class TestControl {
        @JavascriptInterface public String requestsJSON() {
            synchronized (guard) { return requests.toString(); }
        }
        @JavascriptInterface public String statusJSON() {
            synchronized (guard) {
                try { return status().toString(); }
                catch (JSONException impossible) { throw new IllegalStateException(impossible); }
            }
        }
        @JavascriptInterface public String devicesJSON() {
            synchronized (guard) {
                try { return devices().toString(); }
                catch (JSONException impossible) { throw new IllegalStateException(impossible); }
            }
        }
        @JavascriptInterface public int navigationAttempts() {
            synchronized (guard) { return navigationAttempts; }
        }
        @JavascriptInterface public String translationsJSON() throws IOException {
            return readAsset("fixture/translations.json");
        }
        @JavascriptInterface public void setState(String value) {
            if (!Arrays.asList("Running", "Stopped", "Starting", "NeedsLogin", "NeedsMachineAuth").contains(value)) return;
            synchronized (guard) { state = value; }
        }
        @JavascriptInterface public void setLoginMode(String value) {
            if (!Arrays.asList("waiting", "running", "error").contains(value)) return;
            synchronized (guard) { loginMode = value; }
        }
        @JavascriptInterface public void holdNextLogin() {
            synchronized (guard) { holdNextLogin = true; }
        }
        @JavascriptInterface public void releaseLogin() {
            synchronized (guard) {
                if (loginReply != null) {
                    Runnable reply = loginReply;
                    loginReply = null;
                    reply.run();
                }
                holdNextLogin = false;
            }
        }
        @JavascriptInterface public void nextUpStarts() {
            synchronized (guard) { nextUpStarts = true; }
        }
        @JavascriptInterface public void nextSettingsFails() {
            synchronized (guard) { nextSettingsFails = true; }
        }
        @JavascriptInterface public void nextSettingsResponseFails() {
            synchronized (guard) { nextSettingsResponseFails = true; }
        }
        @JavascriptInterface public void setStatusMode(String value) {
            if (!Arrays.asList("normal", "error", "stopped", "service-error", "invalid-settings").contains(value)) return;
            synchronized (guard) { statusMode = value; }
        }
        @JavascriptInterface public void holdNextSettings() {
            synchronized (guard) { holdNextSettings = true; }
        }
        @JavascriptInterface public void releaseSettings() {
            synchronized (guard) {
                if (settingsReply != null) {
                    Runnable reply = settingsReply;
                    settingsReply = null;
                    reply.run();
                }
                holdNextSettings = false;
            }
        }
        @JavascriptInterface public void holdNextStatus() {
            synchronized (guard) { holdNextStatus = true; }
        }
        @JavascriptInterface public void releaseStatus() {
            synchronized (guard) {
                if (statusReply != null) {
                    Runnable reply = statusReply;
                    statusReply = null;
                    reply.run();
                }
                holdNextStatus = false;
            }
        }
        @JavascriptInterface public void nextConsoleFails() {
            synchronized (guard) { nextConsoleFails = true; }
        }
        @JavascriptInterface public void nextAboutFails() {
            synchronized (guard) { nextAboutFails = true; }
        }
        @JavascriptInterface public void holdNextAbout() {
            synchronized (guard) { holdNextAbout = true; }
        }
        @JavascriptInterface public void releaseAbout() {
            synchronized (guard) {
                if (aboutReply != null) {
                    Runnable reply = aboutReply;
                    aboutReply = null;
                    reply.run();
                }
                holdNextAbout = false;
            }
        }
        @JavascriptInterface public void pressEnter() {
            web.post(() -> {
                web.requestFocus();
                web.dispatchKeyEvent(new KeyEvent(KeyEvent.ACTION_DOWN, KeyEvent.KEYCODE_ENTER));
                web.dispatchKeyEvent(new KeyEvent(KeyEvent.ACTION_UP, KeyEvent.KEYCODE_ENTER));
            });
        }
        @JavascriptInterface public void nextChannelFails() {
            synchronized (guard) { nextChannelFails = true; }
        }
        @JavascriptInterface public void setAcceptRoutes(boolean value) {
            synchronized (guard) { acceptRoutes = value; }
        }
        @JavascriptInterface public void setLoginURL(String value) {
            if (screenshotTab != null) return;
            Uri uri = Uri.parse(value);
            if (!value.isEmpty() && (!"https".equals(uri.getScheme()) || !"example.test".equals(uri.getHost()) ||
                    uri.getUserInfo() != null)) return;
            synchronized (guard) { loginURL = value; }
        }
        @JavascriptInterface public void setBinaryMode(String value) {
            if (!Arrays.asList("newer", "current", "revision", "unsupported", "error", "update-error").contains(value)) return;
            synchronized (guard) { binaryMode = value; binaryChecked = false; }
        }
        @JavascriptInterface public void holdNextBinary() {
            synchronized (guard) { holdNextBinary = true; }
        }
        @JavascriptInterface public void releaseBinary() {
            synchronized (guard) {
                if (binaryReply != null) {
                    Runnable reply = binaryReply;
                    binaryReply = null;
                    reply.run();
                }
                holdNextBinary = false;
            }
        }
        @JavascriptInterface public void report(String name, boolean success) { record(name, success); }
        @JavascriptInterface public void finish() { complete(); }
        @JavascriptInterface public void screenshotReady() {
            if (screenshotTab != null) Log.i(TAG, "SCREENSHOT READY " + screenshotTab + " " + screenshotLocale);
        }
        @JavascriptInterface public void screenshotError() {
            if (screenshotTab != null) Log.i(TAG, "SCREENSHOT ERROR initialization");
        }
    }

    @Override public void onDestroy() {
        if (web != null) {
            web.removeJavascriptInterface("ksu");
            web.removeJavascriptInterface("fixture");
            web.destroy();
        }
        super.onDestroy();
    }
}
