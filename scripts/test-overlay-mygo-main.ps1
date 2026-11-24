# Builds and tests the overlay PoC against a local MyGo checkout (usually
# artifacts/mygo-main at origin/main). The checkout is wired in with a
# temporary go.mod replace; the module files are restored in finally so the
# repository keeps its pinned v0.10.0 baseline.
#
#   pwsh -File scripts/test-overlay-mygo-main.ps1 [-MyGo artifacts/mygo-main] [-SkipOwner]
#
# The child run passes -child-uncloak=false on purpose: it proves the MyGo
# checkout clears the first-frame cloak itself instead of relying on
# cmd/overlay-poc's v0.10.0 workaround.

param(
    [string]$MyGo = 'artifacts/mygo-main',
    [switch]$SkipOwner
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$mygoPath = (Resolve-Path $MyGo).Path
if (-not (Test-Path (Join-Path $mygoPath 'go.mod'))) {
    throw "not a MyGo checkout: $mygoPath"
}
$replacePath = './' + ((Resolve-Path -Relative $mygoPath) -replace '\\', '/')
if (-not $replacePath.StartsWith('./')) { $replacePath = './' + $replacePath }

$out = Join-Path $root 'artifacts\overlay-poc'
New-Item -ItemType Directory -Force $out | Out-Null
$modBackup = Join-Path $out 'go.mod.mygo-main.bak'
$sumBackup = Join-Path $out 'go.sum.mygo-main.bak'

Copy-Item (Join-Path $root 'go.mod') $modBackup -Force
Copy-Item (Join-Path $root 'go.sum') $sumBackup -Force
$commit = & git -C $mygoPath log -1 --format='%h %s'
Write-Host "MyGo checkout: $mygoPath"
Write-Host "MyGo commit:   $commit"

try {
    & go mod edit -replace "github.com/egoist/mygo=$replacePath"
    if ($LASTEXITCODE -ne 0) { throw 'go mod edit -replace failed' }

    foreach ($target in @(
            @('overlay-host', 'cmd/overlay-host'),
            @('overlay-poc', 'cmd/overlay-poc'))) {
        & pwsh -NoProfile -File scripts/go.ps1 build -trimpath -tags mygo_noinspector `
            -ldflags '-H=windowsgui -s -w' -o (Join-Path $out "$($target[0]).exe") "./$($target[1])"
        if ($LASTEXITCODE -ne 0) { throw "build failed: $($target[1])" }
    }

    Write-Host '== child mode (MyGo must uncloak by itself) =='
    & pwsh -NoProfile -File scripts/test-overlay-child.ps1 -SkipBuild `
        -ExtraOverlayArgs '-child-uncloak=false'
    if ($LASTEXITCODE -ne 0) { throw 'child overlay test failed' }

    if (-not $SkipOwner) {
        Write-Host '== owner mode regression =='
        & pwsh -NoProfile -File scripts/test-overlay-poc.ps1 -SkipBuild
        if ($LASTEXITCODE -ne 0) { throw 'owner overlay test failed' }
    }
} finally {
    Copy-Item $modBackup (Join-Path $root 'go.mod') -Force
    Copy-Item $sumBackup (Join-Path $root 'go.sum') -Force
}
