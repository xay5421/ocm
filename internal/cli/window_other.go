//go:build !windows && !darwin

package cli

// openWindow is implemented on Windows (WebView2) and macOS (WKWebView).
// Elsewhere the dashboard just serves HTTP; open the printed URL manually.
func openWindow(url, title string) bool { return false }
