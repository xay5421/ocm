// Command makeapp packs a darwin ocm binary into a macOS application
// bundle (ocm.app) and writes it as a zip that preserves the executable
// bit. It is wired into goreleaser as a post-build hook for every build,
// and exits as a no-op unless -os is darwin.
//
// Usage:
//
//	go run ./tools/makeapp -os darwin -arch arm64 -version 1.2.3 \
//	    -binary dist/ocm -icns assets/ocm.icns -out dist/ocm_darwin_arm64_app.zip
//
// The app launches `ocm dashboard` (via the GUI-launch detection in
// internal/cli), which opens the dashboard in a native WKWebView window;
// closing the window quits the app. The binary must therefore be built on
// macOS with CGO_ENABLED=1 (see .github/workflows/release.yml).
package main

import (
	"archive/zip"
	"flag"
	"fmt"
	"os"
	"strings"
)

// launcherScript is the bundle entry point: it execs the real binary with
// an explicit `dashboard` argument, sidestepping tty-based GUI detection
// (LaunchServices attaches /dev/null, which looks like a terminal). The
// plain `ocm` binary next to it stays usable as a CLI.
const launcherScript = `#!/bin/sh
exec "$(dirname "$0")/ocm" dashboard
`

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleExecutable</key>
	<string>ocm-launcher</string>
	<key>CFBundleIconFile</key>
	<string>ocm.icns</string>
	<key>CFBundleIdentifier</key>
	<string>com.github.xay5421.ocm</string>
	<key>CFBundleInfoDictionaryVersion</key>
	<string>6.0</string>
	<key>CFBundleName</key>
	<string>ocm</string>
	<key>CFBundleDisplayName</key>
	<string>ocm</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>%s</string>
	<key>CFBundleVersion</key>
	<string>%s</string>
	<key>LSMinimumSystemVersion</key>
	<string>11.0</string>
	<key>NSHighResolutionCapable</key>
	<true/>
	<key>NSAppTransportSecurity</key>
	<dict>
		<key>NSAllowsLocalNetworking</key>
		<true/>
	</dict>
</dict>
</plist>
`

func main() {
	goos := flag.String("os", "", "target GOOS of the binary (no-op unless darwin)")
	arch := flag.String("arch", "", "target GOARCH, informational only")
	version := flag.String("version", "0.0.0", "bundle version")
	binary := flag.String("binary", "", "path to the darwin ocm binary")
	icns := flag.String("icns", "assets/ocm.icns", "path to the app icon")
	out := flag.String("out", "", "output zip path")
	flag.Parse()

	if *goos != "darwin" {
		return // called for every goreleaser build; only darwin gets an app
	}
	if *binary == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "makeapp: -binary and -out are required")
		os.Exit(1)
	}
	if err := run(*binary, *icns, *out, *version); err != nil {
		fmt.Fprintln(os.Stderr, "makeapp:", err)
		os.Exit(1)
	}
	fmt.Printf("makeapp: wrote %s (ocm.app for darwin/%s)\n", *out, *arch)
}

func run(binary, icns, out, version string) error {
	binData, err := os.ReadFile(binary)
	if err != nil {
		return err
	}
	icnsData, err := os.ReadFile(icns)
	if err != nil {
		return err
	}
	// CFBundleShortVersionString wants plain x.y.z.
	shortVersion := strings.TrimPrefix(version, "v")
	plist := fmt.Sprintf(plistTemplate, shortVersion, shortVersion)

	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	files := []struct {
		name string
		mode os.FileMode
		data []byte
	}{
		{"ocm.app/Contents/Info.plist", 0o644, []byte(plist)},
		{"ocm.app/Contents/PkgInfo", 0o644, []byte("APPL????")},
		{"ocm.app/Contents/MacOS/ocm-launcher", 0o755, []byte(launcherScript)},
		{"ocm.app/Contents/MacOS/ocm", 0o755, binData},
		{"ocm.app/Contents/Resources/ocm.icns", 0o644, icnsData},
	}
	for _, file := range files {
		hdr := &zip.FileHeader{Name: file.name, Method: zip.Deflate}
		hdr.SetMode(file.mode)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		if _, err := w.Write(file.data); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return f.Close()
}
