# Builds a release ocm.exe into dist\ with the app icon and version info.
#
#   .\build.ps1                # version from `git describe`
#   .\build.ps1 -Version v1.2.3
#
# Requires Go; go-winres is fetched on demand via `go run`.
param(
    [string]$Version = ""
)

$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

if (-not $Version) {
    $Version = git describe --tags --always --dirty 2>$null
    if (-not $Version) { $Version = "dev" }
}
# The PE version resource needs a numeric x.y.z[.w]; fall back to 0.0.0 for
# untagged builds (the full string still goes into `ocm version`).
$resVersion = "0.0.0"
if ($Version -match '^v?(\d+\.\d+\.\d+)') { $resVersion = $Matches[1] }

Write-Host "building ocm $Version (resource version $resVersion)"

# Regenerate the icon and the Windows resource objects (.syso), for both
# the CLI (repo root) and the GUI launcher (cmd/ocm-dashboard).
go run ./tools/genicon
if ($LASTEXITCODE -ne 0) { exit 1 }
go run github.com/tc-hib/go-winres@v0.3.3 make --arch amd64,arm64 `
    --product-version $resVersion --file-version $resVersion
if ($LASTEXITCODE -ne 0) { exit 1 }
go run github.com/tc-hib/go-winres@v0.3.3 make --arch amd64,arm64 `
    --product-version $resVersion --file-version $resVersion `
    --out cmd/ocm-dashboard/rsrc
if ($LASTEXITCODE -ne 0) { exit 1 }

New-Item -ItemType Directory -Force -Path dist | Out-Null
$env:CGO_ENABLED = "0"
$ldVersion = "-X github.com/xay5421/ocm/internal/cli.Version=$Version"

# ocm.exe: the CLI (console subsystem).
go build -trimpath -ldflags "-s -w $ldVersion" -o dist\ocm.exe .
if ($LASTEXITCODE -ne 0) { exit 1 }

# ocm-dashboard.exe: double-click launcher (GUI subsystem, no console flash).
go build -trimpath -ldflags "-s -w -H=windowsgui $ldVersion" `
    -o dist\ocm-dashboard.exe .\cmd\ocm-dashboard
if ($LASTEXITCODE -ne 0) { exit 1 }

Write-Host "done: dist\ocm.exe, dist\ocm-dashboard.exe"
