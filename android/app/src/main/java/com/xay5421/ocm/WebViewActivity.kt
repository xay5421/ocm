package com.xay5421.ocm

import android.annotation.SuppressLint
import android.content.Intent
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.webkit.ConsoleMessage
import android.webkit.HttpAuthHandler
import android.webkit.WebChromeClient
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.FrameLayout
import android.widget.TextView
import androidx.appcompat.app.AlertDialog
import androidx.activity.OnBackPressedCallback
import androidx.appcompat.app.AppCompatActivity

class WebViewActivity : AppCompatActivity() {

    private lateinit var webView: WebView
    private lateinit var status: TextView
    private lateinit var diagBtn: TextView
    private var host: Host? = null
    private val handler = Handler(Looper.getMainLooper())
    private var loaded = false
    private var waitedMs = 0
    private var retries = 0
    private var hadError = false
    private var lastBackAt = 0L
    private val diag = ArrayDeque<String>()

    // Battery: a backgrounded tunnel burns radio power with 30s keepalives,
    // and sessions live on the server anyway. After a grace period in the
    // background the tunnel is torn down; returning reconnects automatically.
    private var bgStopped = false
    private val bgStopRunnable = Runnable {
        bgStopped = true
        log("后台超过 ${BG_DISCONNECT_MS / 60000} 分钟，断开隧道以省电")
        try {
            startService(Intent(this, SshTunnelService::class.java).apply {
                action = SshTunnelService.ACTION_STOP
            })
        } catch (_: Exception) {}
    }

    companion object {
        private const val BG_DISCONNECT_MS = 3 * 60_000L
    }

    private fun log(line: String) {
        synchronized(diag) {
            diag.addLast("${android.text.format.DateFormat.format("HH:mm:ss", System.currentTimeMillis())} $line")
            while (diag.size > 80) diag.removeFirst()
        }
    }

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val name = intent.getStringExtra("name") ?: run { finish(); return }
        host = HostStore(this).get(name) ?: run { finish(); return }

        WebView.setWebContentsDebuggingEnabled(BuildConfig.DEBUG)
        webView = WebView(this)
        status = TextView(this).apply {
            text = "连接 $name …"
            textSize = 16f
            gravity = android.view.Gravity.CENTER_HORIZONTAL
            setPadding(48, 48, 48, 48)
        }
        // Diagnostics button: only shown while the native status overlay is
        // relevant (connecting / load errors). Once the page is up it is
        // hidden so it never covers the web UI's own top-right content.
        diagBtn = TextView(this).apply {
            text = "ⓘ"
            textSize = 18f
            alpha = 0.35f
            setPadding(28, 28, 28, 28)
            setOnClickListener { showDiag() }
        }
        setContentView(FrameLayout(this).apply {
            addView(webView)
            // Centered vertically so it never sits under the top-right ⓘ button.
            addView(status, FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.MATCH_PARENT, FrameLayout.LayoutParams.WRAP_CONTENT,
                android.view.Gravity.CENTER,
            ))
            addView(diagBtn, FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.WRAP_CONTENT, FrameLayout.LayoutParams.WRAP_CONTENT,
                android.view.Gravity.TOP or android.view.Gravity.END,
            ))
        })

        webView.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            databaseEnabled = true
            mediaPlaybackRequiresUserGesture = false
        }
        webView.webViewClient = object : WebViewClient() {
            override fun onReceivedHttpAuthRequest(
                view: WebView, h: HttpAuthHandler, hostName: String, realm: String,
            ) {
                val hc = host
                log("httpAuth challenge host=$hostName realm=$realm")
                if (hc != null && hc.authUser.isNotEmpty()) {
                    h.proceed(hc.authUser, hc.authPass)
                } else {
                    h.cancel()
                }
            }

            override fun onReceivedHttpError(
                view: WebView, request: WebResourceRequest, errorResponse: WebResourceResponse,
            ) {
                log("HTTP ${errorResponse.statusCode} ${request.method} ${request.url}")
            }

            override fun shouldInterceptRequest(
                view: WebView, request: WebResourceRequest,
            ): WebResourceResponse? {
                // Per-request logging is only worth its cost in debug builds.
                if (BuildConfig.DEBUG) {
                    val u = request.url.toString()
                    if (!u.contains("/assets/") && !u.contains("favicon")) {
                        log("REQ ${request.method} $u")
                    }
                }
                return null
            }

            override fun onReceivedError(
                view: WebView, request: WebResourceRequest, error: WebResourceError,
            ) {
                log("ERR ${error.errorCode} ${error.description} ${request.method} ${request.url}" +
                    if (request.isForMainFrame) " [main]" else "")
                if (!request.isForMainFrame) return
                hadError = true
                status.visibility = TextView.VISIBLE
                diagBtn.visibility = TextView.VISIBLE
                val desc = "${error.errorCode} ${error.description}"
                if (retries < 5) {
                    retries++
                    status.text = "加载失败（$desc），重试 $retries/5 …"
                    handler.postDelayed({ loadTarget() }, 1000)
                } else {
                    status.text = "加载失败：$desc\n" +
                        "隧道状态：${SshTunnelService.state}\n" +
                        "最近错误：${SshTunnelService.lastError}"
                }
            }

            override fun onPageStarted(view: WebView, url: String, favicon: android.graphics.Bitmap?) {
                injectProjectSeed(view)
            }

            override fun onPageFinished(view: WebView, url: String) {
                log("pageFinished $url")
                if (!hadError) {
                    retries = 0
                    status.visibility = TextView.GONE
                    diagBtn.visibility = TextView.GONE
                }
                injectProjectSeed(view)
                injectProbe(view)
            }
        }
        webView.webChromeClient = object : WebChromeClient() {
            override fun onConsoleMessage(msg: ConsoleMessage): Boolean {
                val m = msg.message()
                if (BuildConfig.DEBUG ||
                    msg.messageLevel() != ConsoleMessage.MessageLevel.LOG ||
                    m.startsWith("SEED") || m.startsWith("PROBE") || m.startsWith("SNAP")
                ) {
                    log("JS[${msg.messageLevel()}] $m (${msg.sourceId()}:${msg.lineNumber()})")
                }
                return true
            }
        }

        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                // Never delegate to webView.goBack(): the opencode web UI is an
                // SPA that keeps pushing history entries, so in-page back would
                // trap the user here forever. Back means "leave this host";
                // double-press guards against accidental back gestures (an exit
                // tears down the tunnel, reconnecting takes seconds).
                val now = android.os.SystemClock.uptimeMillis()
                if (now - lastBackAt < 2000) {
                    finish()
                    return
                }
                lastBackAt = now
                android.widget.Toast.makeText(
                    this@WebViewActivity, "再按一次返回主机列表", android.widget.Toast.LENGTH_SHORT,
                ).show()
            }
        })

        startService(Intent(this, SshTunnelService::class.java).apply {
            action = SshTunnelService.ACTION_CONNECT
            putExtra("name", name)
        })
        handler.postDelayed(::poll, 500)
    }

    /**
     * The opencode web UI only lists projects/sessions that were "opened" in this
     * browser (localStorage `opencode.global.dat:server`). A fresh WebView therefore
     * shows an empty sidebar even though the server has sessions. Ask the server for
     * every directory that has sessions and seed them all into that registry, so the
     * sidebar mirrors the server ("full sync") on first use.
     */
    private fun injectProjectSeed(view: WebView) {
        val dir = host?.directory ?: ""
        val dirJson = org.json.JSONObject.quote(dir)
        val js = """
            (function(){
              if (window.__ocmSeedRan) return; window.__ocmSeedRan = true;
              var hostDir = $dirJson;
              var KEY = 'opencode.global.dat:server';
              fetch('/session', { headers: { accept: 'application/json' } })
                .then(function(r){ return r.json(); })
                .then(function(all){
                  var dirs = {};
                  if (hostDir) dirs[hostDir] = true;
                  (Array.isArray(all) ? all : []).forEach(function(s){
                    if (s && s.directory && !s.parentID && !(s.time && s.time.archived)) {
                      dirs[s.directory] = true;
                    }
                  });
                  var cur = null;
                  try { cur = JSON.parse(localStorage.getItem(KEY) || 'null'); } catch (e) {}
                  var next = (cur && typeof cur === 'object' && !Array.isArray(cur)) ? cur
                    : { list: [], projects: {}, lastProject: {}, recentlyClosed: {} };
                  next.projects = next.projects || {};
                  var list = Array.isArray(next.projects.local) ? next.projects.local : [];
                  var have = {};
                  list.forEach(function(p){ if (p && p.worktree) have[p.worktree] = true; });
                  var added = 0;
                  Object.keys(dirs).forEach(function(d){
                    if (!have[d]) { list.push({ worktree: d, expanded: true }); added++; }
                  });
                  if (!added) return;
                  next.projects.local = list;
                  next.lastProject = next.lastProject || {};
                  if (!next.lastProject.local) next.lastProject.local = hostDir || Object.keys(dirs)[0];
                  localStorage.setItem(KEY, JSON.stringify(next));
                  console.log('SEED added ' + added + ' projects: ' + Object.keys(dirs).join(', '));
                  if (!sessionStorage.getItem('ocmSeedReload')) {
                    sessionStorage.setItem('ocmSeedReload', '1');
                    location.reload();
                  }
                })
                .catch(function(e){ console.log('SEED fail ' + e); });
            })();
        """.trimIndent()
        view.evaluateJavascript(js, null)
    }

    /** In-page probe: does fetch work? is data reachable from JS? */
    private fun injectProbe(view: WebView) {
        // Error listeners are cheap and always useful for the ⓘ dialog; the
        // fetch wrapper and DOM probing below are debug-only overhead.
        if (!BuildConfig.DEBUG) {
            view.evaluateJavascript("""
                (function(){
                  if (window.__ocmProbed) return; window.__ocmProbed = true;
                  window.addEventListener('error', function(e){
                    console.log('PROBE window.onerror: ' + e.message + ' @' + e.filename + ':' + e.lineno);
                  });
                  window.addEventListener('unhandledrejection', function(e){
                    console.log('PROBE unhandledrejection: ' + e.reason);
                  });
                })();
            """.trimIndent(), null)
            return
        }
        val js = """
            (function(){
              if (window.__ocmProbed) return; window.__ocmProbed = true;
              console.log('PROBE UA=' + navigator.userAgent);
              window.addEventListener('error', function(e){
                console.log('PROBE window.onerror: ' + e.message + ' @' + e.filename + ':' + e.lineno);
              });
              window.addEventListener('unhandledrejection', function(e){
                console.log('PROBE unhandledrejection: ' + e.reason);
              });
              var of = window.fetch;
              window.fetch = function(u, o) {
                return of.apply(this, arguments).then(function(r){
                  console.log('FETCH ' + (r.status) + ' ' + (u && u.url ? u.url : u));
                  return r;
                }, function(e){ console.log('FETCH FAIL ' + u + ' ' + e); throw e; });
              };
              setTimeout(function(){
                var dir = location.pathname.split('/')[1] || '';
                var q = '/session?roots=true&limit=55';
                try {
                  if (dir && dir !== 'new-session') {
                    var pad = dir.length % 4 === 2 ? '==' : dir.length % 4 === 3 ? '=' : '';
                    var decoded = atob(dir.replace(/-/g,'+').replace(/_/g,'/') + pad);
                    q = '/session?directory=' + encodeURIComponent(decoded) + '&roots=true&limit=55';
                  }
                } catch (e) { console.log('PROBE dir decode fail ' + e); }
                of(q).then(function(r){ return r.json(); }).then(function(a){
                  console.log('PROBE listQuery count=' + (a && a.length !== undefined ? a.length : JSON.stringify(a).slice(0,120)));
                }).catch(function(e){ console.log('PROBE listQuery FAIL ' + e); });
                var t = (document.body.innerText || '').replace(/\s+/g, ' ');
                console.log('PROBE bodyText(' + t.length + '): ' + t.slice(0, 220));
                console.log('PROBE sessionLinks=' + document.querySelectorAll('a[href*="ses_"]').length
                  + ' allLinks=' + document.querySelectorAll('a').length
                  + ' viewport=' + window.innerWidth + 'x' + window.innerHeight);
                console.log('PROBE localStorage keys=' + Object.keys(localStorage).length);
              }, 3000);
            })();
        """.trimIndent()
        view.evaluateJavascript(js, null)
    }

    private fun showDiag() {
        // Snapshot the live DOM first so the drawer/list state at press time is captured.
        val snapJs = """
            (function(){
              var t = (document.body.innerText || '').replace(/\s+/g, ' ');
              console.log('SNAP bodyText(' + t.length + '): ' + t.slice(0, 400));
              console.log('SNAP sessionLinks=' + document.querySelectorAll('a[href*="ses_"]').length
                + ' allLinks=' + document.querySelectorAll('a').length);
            })();
        """.trimIndent()
        webView.evaluateJavascript(snapJs, null)
        webView.postDelayed({ showDiagDialog() }, 400)
    }

    private fun showDiagDialog() {
        val text = synchronized(diag) { diag.joinToString("\n").ifEmpty { "（暂无记录）" } }
        AlertDialog.Builder(this)
            .setTitle("诊断日志")
            .setMessage(text)
            .setPositiveButton("复制") { _, _ ->
                val cm = getSystemService(CLIPBOARD_SERVICE) as android.content.ClipboardManager
                cm.setPrimaryClip(android.content.ClipData.newPlainText("ocm-diag", text))
            }
            .setNegativeButton("关闭", null)
            .show()
    }

    private fun loadTarget() {
        hadError = false
        val dir = host?.directory ?: ""
        val path = if (dir.isEmpty()) "/" else {
            // Same encoding as the web UI's route token: base64url, no padding.
            val token = android.util.Base64.encodeToString(
                dir.toByteArray(Charsets.UTF_8),
                android.util.Base64.URL_SAFE or android.util.Base64.NO_PADDING or android.util.Base64.NO_WRAP,
            )
            "/$token/session"
        }
        log("load http://127.0.0.1:${SshTunnelService.localPort}$path")
        webView.loadUrl("http://127.0.0.1:${SshTunnelService.localPort}$path")
    }

    private fun poll() {
        if (isFinishing || loaded) return
        when (SshTunnelService.state) {
            SshTunnelService.STATE_RUNNING -> {
                loaded = true
                status.text = ""
                status.visibility = TextView.GONE
                loadTarget()
                return
            }
            SshTunnelService.STATE_ERROR -> {
                status.text = "连接失败：${SshTunnelService.lastError}"
                return
            }
        }
        waitedMs += 500
        if (waitedMs >= 30000) {
            status.text = "连接超时"
            return
        }
        handler.postDelayed(::poll, 500)
    }

    override fun onStop() {
        super.onStop()
        if (!::webView.isInitialized || isFinishing) return
        // Freeze page JS/timers in the background; the SSE reconnects on resume.
        webView.onPause()
        webView.pauseTimers()
        handler.postDelayed(bgStopRunnable, BG_DISCONNECT_MS)
    }

    override fun onStart() {
        super.onStart()
        if (!::webView.isInitialized) return
        handler.removeCallbacks(bgStopRunnable)
        webView.resumeTimers()
        webView.onResume()
        if (bgStopped) {
            bgStopped = false
            loaded = false
            waitedMs = 0
            log("回到前台，重连隧道")
            status.text = "重新连接 ${host?.name} …"
            status.visibility = TextView.VISIBLE
            startService(Intent(this, SshTunnelService::class.java).apply {
                action = SshTunnelService.ACTION_CONNECT
                putExtra("name", host?.name)
            })
            handler.postDelayed(::poll, 500)
        }
    }

    override fun onDestroy() {
        handler.removeCallbacksAndMessages(null)
        if (isFinishing) {
            startService(Intent(this, SshTunnelService::class.java).apply {
                action = SshTunnelService.ACTION_STOP
            })
        }
        webView.destroy()
        super.onDestroy()
    }
}
