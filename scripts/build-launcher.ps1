# SPDX-License-Identifier: GPL-3.0-or-later
# Release build of the native launcher with the native CN login flow compiled in
# and the authored WPF login window embedded as a self-contained payload.
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

    # Build the authored WPF window and embed it in the launcher. The extracted
    # copy lives in the user's cache; no external login-compat folder is needed.
    $loginCompat = Join-Path $outDir 'loginui-build'
    & "$PSScriptRoot/../compat/loginui/Build.ps1" -Output $loginCompat
    $payload = Join-Path $PSScriptRoot '../cmd/launcher/loginui_payload.zip'
    if (Test-Path -LiteralPath $payload) { Remove-Item -LiteralPath $payload -Force }
    Compress-Archive -Path (Join-Path $loginCompat '*') -DestinationPath $payload -CompressionLevel Optimal
    try {
        $ldflags = '-H=windowsgui -s -w'
        if ($Version) { $ldflags += " -X github.com/nerv-base/Oddward/internal/buildinfo.Version=$Version" }
        & "$PSScriptRoot/go.ps1" build -trimpath -tags 'mygo_noinspector loginui_embed' -ldflags $ldflags -o $Output ./cmd/launcher
        if ($LASTEXITCODE -ne 0) { throw 'go build failed.' }
    }
    finally {
        if (Test-Path -LiteralPath $payload) { Remove-Item -LiteralPath $payload -Force }
        if (Test-Path -LiteralPath $loginCompat) { Remove-Item -LiteralPath $loginCompat -Recurse -Force }
    }

    & "$PSScriptRoot/copy-licenses.ps1" -Destination (Join-Path $outDir 'licenses')
    $hash = (Get-FileHash -LiteralPath $Output -Algorithm SHA256).Hash.ToLowerInvariant()
    Write-Host "Built $Output (native CN login, WPF window embedded) sha256 $hash"
}
finally {
    Pop-Location
}
