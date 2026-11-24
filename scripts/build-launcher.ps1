# SPDX-License-Identifier: GPL-3.0-or-later
# Release build of the native launcher with pure Go + MyGo Native UI CN login
# and welcome banner built in (--cnlogin-ui). No .NET SDK or WPF runtime is
# required for the default build.
#
# Pass -EmbedWPF during Phase 3 migration validation to also build and embed the
# temporary WPF fallback (compat/loginui) for -login-ui=wpf A/B testing.
param(
    [string]$Output = 'artifacts/native-launcher/oddward.exe',
    # Release SemVer without v; empty builds a development exe that never self-updates.
    [string]$Version = '',
    # Temporary Phase 3 fallback: also embed the self-contained WPF login helper.
    [switch]$EmbedWPF
)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    $outDir = Split-Path -Parent $Output
    New-Item -ItemType Directory -Force -Path $outDir | Out-Null

    $tags = 'mygo_noinspector'
    $loginCompat = Join-Path $outDir 'loginui-build'
    $payload = Join-Path $PSScriptRoot '../cmd/launcher/loginui_payload.zip'
    if ($EmbedWPF) {
        & "$PSScriptRoot/../compat/loginui/Build.ps1" -Output $loginCompat
        if (Test-Path -LiteralPath $payload) { Remove-Item -LiteralPath $payload -Force }
        Compress-Archive -Path (Join-Path $loginCompat '*') -DestinationPath $payload -CompressionLevel Optimal
        $tags = 'mygo_noinspector loginui_embed'
    }
    try {
        $ldflags = '-H=windowsgui -s -w'
        if ($Version) { $ldflags += " -X github.com/nerv-base/Oddward/internal/buildinfo.Version=$Version" }
        & "$PSScriptRoot/go.ps1" build -trimpath -tags $tags -ldflags $ldflags -o $Output ./cmd/launcher
        if ($LASTEXITCODE -ne 0) { throw 'go build failed.' }
    }
    finally {
        if ($EmbedWPF) {
            if (Test-Path -LiteralPath $payload) { Remove-Item -LiteralPath $payload -Force }
            if (Test-Path -LiteralPath $loginCompat) { Remove-Item -LiteralPath $loginCompat -Recurse -Force }
        }
    }

    $licenseArgs = @{ Destination = (Join-Path $outDir 'licenses') }
    if ($EmbedWPF) { $licenseArgs['IncludeWPFLicense'] = $true }
    & "$PSScriptRoot/copy-licenses.ps1" @licenseArgs
    $hash = (Get-FileHash -LiteralPath $Output -Algorithm SHA256).Hash.ToLowerInvariant()
    $modeNote = if ($EmbedWPF) { 'MyGo default + WPF fallback embedded' } else { 'pure Go + MyGo Native UI login' }
    Write-Host "Built $Output ($modeNote) sha256 $hash"
}
finally {
    Pop-Location
}
