//go:build darwin

package cli

import (
	neturl "net/url"
	"runtime"

	webview "github.com/webview/webview_go"
)

// openWindow shows url in a native WKWebView window (cgo/Cocoa) and blocks
// until the window is closed.
func openWindow(url, title string) bool {
	// Cocoa requires the UI to run on the main OS thread (locked in
	// main.init).
	runtime.LockOSThread()

	w := webview.New(false)
	if w == nil {
		return false
	}
	defer w.Destroy()
	w.SetTitle(title)
	w.SetSize(780, 640, webview.HintNone)
	// The dashboard page calls this instead of following links, so e.g.
	// opencode server URLs open in the default browser rather than
	// navigating the app window (see the click handler in index.html).
	w.Bind("ocmOpenExternal", func(raw string) {
		if u, err := neturl.Parse(raw); err == nil &&
			(u.Scheme == "http" || u.Scheme == "https") {
			openBrowser(raw)
		}
	})
	w.Navigate(url)
	w.Run() // blocks until the window is closed
	return true
}
