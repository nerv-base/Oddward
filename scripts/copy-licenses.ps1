# SPDX-License-Identifier: GPL-3.0-or-later
# Copy the reviewed, pinned license bundle into an application's licenses folder.
# No dependency cache, network access or build tool is needed at packaging time.
param(
    [Parameter(Mandatory)][string]$Destination
)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$sourceRoot = Join-Path $root 'licenses'
$destinationRoot = [IO.Path]::GetFullPath($Destination, (Get-Location).Path)
if ($destinationRoot.TrimEnd('\', '/') -eq $sourceRoot.TrimEnd('\', '/')) {
    throw 'The license destination must not be the source license directory.'
}
$manifestPath = Join-Path $sourceRoot 'SOURCES.json'
$manifest = Get-Content -LiteralPath $manifestPath -Raw -Encoding utf8 | ConvertFrom-Json
if ($manifest.schema -ne 1 -or !$manifest.files.Count) { throw 'Invalid license source manifest.' }
$copies = [Collections.Generic.List[object]]::new()
$utf8 = [Text.UTF8Encoding]::new($false, $true)
foreach ($entry in $manifest.files) {
    if ($entry.file -notmatch '^[a-zA-Z0-9._-]+(/[a-zA-Z0-9._-]+)+$' -or $entry.file -match '(^|/)\.\.(/|$)') {
        throw "Invalid license path: $($entry.file)"
    }
    $source = Join-Path $sourceRoot $entry.file
    $text = [IO.File]::ReadAllText($source, $utf8).Replace("`r`n", "`n")
    $hash = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($utf8.GetBytes($text))).ToLowerInvariant()
    if ($hash -ne $entry.sha256Lf) { throw "License content does not match its source manifest: $($entry.file)" }
    $copies.Add(@{ Source = $source; Relative = $entry.file })
}
$copies.Add(@{ Source = $manifestPath; Relative = 'SOURCES.json' })
$copies.Add(@{ Source = (Join-Path $root 'THIRD_PARTY_NOTICES.md'); Relative = 'THIRD_PARTY_NOTICES.md' })
$copies.Add(@{ Source = (Join-Path $root 'LICENSE'); Relative = 'Oddward.LICENSE.txt' })
$copies.Add(@{ Source = (Join-Path $root 'compat/loginui/LICENSES.md'); Relative = 'loginui/LICENSES.md' })
foreach ($copy in $copies) {
    if (!(Test-Path -LiteralPath $copy.Source -PathType Leaf)) { throw "Missing license notice: $($copy.Source)" }
}
foreach ($copy in $copies) {
    $target = Join-Path $destinationRoot $copy.Relative
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $target) | Out-Null
    Copy-Item -LiteralPath $copy.Source -Destination $target -Force
}
Write-Host "Copied $($copies.Count) license and attribution files to $Destination"
