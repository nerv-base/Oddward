# SPDX-License-Identifier: GPL-3.0-or-later
# Invoke the project-local, verified toolchain without changing system PATH.
# Do not use advanced parameter binding: it would consume Go's -v as -Verbose.
$GoArgs = @($args)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$keys = @('GOTOOLCHAIN','GOOS','GOARCH','CGO_ENABLED','GOCACHE','GOMODCACHE')
$old = @{}
foreach ($key in $keys) { $old[$key] = [Environment]::GetEnvironmentVariable($key, 'Process') }
$go = Join-Path $root '.tools\go1.27.3\go\bin\go.exe'
$private = Test-Path -LiteralPath $go -PathType Leaf
if (-not $private) {
    # CI installs the same pinned release with actions/setup-go and owns its caches.
    $onPath = (Get-Command go -ErrorAction SilentlyContinue).Source
    $env:GOTOOLCHAIN = 'local'
    if (-not $onPath -or (& $onPath env GOVERSION) -ne 'go1.27.3') {
        throw 'Install the verified Go 1.27.3 windows-amd64 toolchain described in README.md first.'
    }
    $go = $onPath
}
Push-Location $root
try {
    $env:GOTOOLCHAIN = 'local'
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    $env:CGO_ENABLED = '0'
    if ($private) {
        $env:GOCACHE = Join-Path $root '.tools\gocache'
        $env:GOMODCACHE = Join-Path $root '.tools\gomod'
    }
    & $go @GoArgs
    $code = $LASTEXITCODE
} finally {
    Pop-Location
    foreach ($key in $keys) { [Environment]::SetEnvironmentVariable($key, $old[$key], 'Process') }
}
exit $code
