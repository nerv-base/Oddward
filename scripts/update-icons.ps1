# SPDX-License-Identifier: GPL-3.0-or-later
# Regenerate the checked-in Windows/amd64 icon resources after changing icon.ico.
# Ordinary Go builds link these .syso files without running this tool.
# Generator: github.com/akavel/rsrc v0.10.2 (MIT)
# License: https://github.com/akavel/rsrc/blob/v0.10.2/LICENSE.txt
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$icon = Join-Path $root 'internal/branding/icon.ico'
if (-not (Test-Path -LiteralPath $icon -PathType Leaf)) {
    throw "Canonical icon not found: $icon"
}
foreach ($entry in @('launcher', 'stub')) {
    $output = Join-Path $root "cmd/$entry/icon_windows_amd64.syso"
    & "$PSScriptRoot/go.ps1" run github.com/akavel/rsrc@v0.10.2 -arch amd64 -ico $icon -o $output
    if ($LASTEXITCODE -ne 0) { throw "Icon resource generation failed for $entry." }
}
