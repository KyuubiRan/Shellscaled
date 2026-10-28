package dev.shellscaled.webuitest;

import android.app.Activity;
import android.graphics.Bitmap;
import android.net.Uri;
import android.os.Bundle;
import android.os.SystemClock;
import android.util.Base64;
import android.util.Log;
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
    private boolean acceptRoutes;
    private boolean shieldsUp;
    private boolean allowLAN;
    private boolean nextUpStarts;
    private boolean testsStarted;
    private boolean finished;
    private int passed;
    private int failed;
    private String screenshotTab;
    private String screenshotLocale;
    private WebView web;

    @Override public void onCreate(Bundle saved) {
        super.onCreate(saved);
        screenshotTab = getIntent().getStringExtra("screenshot");
        screenshotLocale = getIntent().getStringExtra("locale");
        if (screenshotLocale == null) screenshotLocale = "zh-CN";
        if (screenshotTab != null) {
            if (!Arrays.asList("overview", "settings", "command").contains(screenshotTab) ||
                    !Arrays.asList("zh-CN", "zh-TW", "en").contains(screenshotLocale)) {
                Log.i(TAG, "SCREENSHOT ERROR invalid-options");
                finish();
                return;
            }
            acceptRoutes = true;
            Log.i(TAG, "SCREENSHOT START " + screenshotTab + " " + screenshotLocale);
        } else {
            Log.i(TAG, "START run=" + SystemClock.elapsedRealtime());
        }
        web = new WebView(this);
        web.getSettings().setJavaScriptEnabled(true);
        web.getSettings().setDomStorageEnabled(true);
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
        return new JSONObject().put("ok", true).put("state", state)
                .put("version", screenshotTab == null ? "1.104.2-fixture" : "1.104.2")
                .put("daemonRunning", true).put("exitNodeConfigured", true).put("exitCode", 0)
                .put("ips", new JSONArray().put("100.64.0.10").put("fd7a:115c:a1e0::10"))
                .put("settings", settings);
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
                    requests.put(request);
                    switch (request.getString("action")) {
                        case "status": break;
                        case "down": state = "Stopped"; break;
                        case "up": state = nextUpStarts ? "Starting" : "Running"; nextUpStarts = false; break;
                        case "settings":
                            JSONObject patch = request.getJSONObject("settings");
                            if (patch.has("acceptRoutes")) acceptRoutes = patch.getBoolean("acceptRoutes");
                            if (patch.has("shieldsUp")) shieldsUp = patch.getBoolean("shieldsUp");
                            if (patch.has("exitNodeAllowLanAccess")) allowLAN = patch.getBoolean("exitNodeAllowLanAccess");
                            break;
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
        @JavascriptInterface public String translationsJSON() throws IOException {
            return readAsset("fixture/translations.json");
        }
        @JavascriptInterface public void setState(String value) {
            if (!Arrays.asList("Running", "Stopped", "Starting").contains(value)) return;
            synchronized (guard) { state = value; }
        }
        @JavascriptInterface public void nextUpStarts() {
            synchronized (guard) { nextUpStarts = true; }
        }
        @JavascriptInterface public void setAcceptRoutes(boolean value) {
            synchronized (guard) { acceptRoutes = value; }
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
