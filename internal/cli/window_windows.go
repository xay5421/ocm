//go:build windows

package cli

import (
	neturl "net/url"
	"os"
	"path/filepath"
	"runtime"

	webview2 "github.com/jchv/go-webview2"
)

// openWindow shows url in a native WebView2 window and blocks until the
// window is closed. It returns false if the WebView2 runtime is unavailable
// (very old Windows without Edge); the caller then reports an error.
func openWindow(url, title string) bool {
	// The Win32 message loop must stay on one OS thread.
	runtime.LockOSThread()

	// WebView2 needs a writable user-data directory; the default (next to
	// the exe) may be read-only, e.g. under Program Files.
	dataPath := ""
	if dir, err := os.UserCacheDir(); err == nil {
		dataPath = filepath.Join(dir, "ocm", "webview2")
	}

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		DataPath:  dataPath,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  title,
			Width:  780,
			Height: 640,
			IconId: 1, // app icon resource, see winres/winres.json
			Center: true,
		},
	})
	if w == nil {
		return false
	}
	defer w.Destroy()
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
