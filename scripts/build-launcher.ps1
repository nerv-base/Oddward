# SPDX-License-Identifier: GPL-3.0-or-later
# Release build of the native launcher with pure Go + MyGo Native UI CN login
# and welcome banner built in (--cnlogin-ui).
param(
    [string]$Output = 'artifacts/native-launcher/oddward.exe',
    # Release SemVer without v; empty builds a development exe that never self-updates.
    [string]$Version = ''
)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    $outDir = Split-Path -Parent $Output
    New-Item -ItemType Directory -Force -Path $outDir | Out-Null

    $ldflags = '-H=windowsgui -s -w'
    if ($Version) { $ldflags += " -X github.com/nerv-base/Oddward/internal/buildinfo.Version=$Version" }
    & "$PSScriptRoot/go.ps1" build -trimpath -tags mygo_noinspector -ldflags $ldflags -o $Output ./cmd/launcher
    if ($LASTEXITCODE -ne 0) { throw 'go build failed.' }

    & "$PSScriptRoot/copy-licenses.ps1" -Destination (Join-Path $outDir 'licenses')
    $hash = (Get-FileHash -LiteralPath $Output -Algorithm SHA256).Hash.ToLowerInvariant()
    Write-Host "Built $Output sha256 $hash"
}
finally {
    Pop-Location
}
