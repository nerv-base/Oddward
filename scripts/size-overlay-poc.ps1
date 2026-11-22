# Repeatable size comparison for the MyGo overlay PoC.
#
#   pwsh -File scripts/size-overlay-poc.ps1 [-SkipWPF]
#
# Builds, with the same Go toolchain and Release flags:
#   1. the current product launcher with the self-contained WPF helper
#      embedded (scripts/build-launcher.ps1),
#   2. the same launcher without the loginui_embed tag (no WPF payload),
#   3. the PoC overlay and its test host.
#
# Every executable is also measured after ZIP (Deflate, optimal) compression
# to approximate the final distribution size. This is a compressed-archive
# comparison, not the installer format produced by scripts/build-release.ps1.
#
# Results are printed and saved to artifacts/size/size-report.json.

param(
    [switch]$SkipWPF
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root
$out = Join-Path $root 'artifacts\size'
New-Item -ItemType Directory -Force $out | Out-Null

function Get-MiB([long]$bytes) {
    return [Math]::Round($bytes / 1MB, 2)
}

function Build-Go([string]$name, [string]$pkg) {
    $exe = Join-Path $out "$name.exe"
    & pwsh -NoProfile -File scripts/go.ps1 build -trimpath -tags mygo_noinspector `
        -ldflags '-H=windowsgui -s -w' -o $exe $pkg
    if ($LASTEXITCODE -ne 0) { throw "go build failed: $pkg" }
    return $exe
}

function Measure-Artifact([string]$name, [string]$exe) {
    if (-not (Test-Path $exe)) { throw "missing artifact: $exe" }
    $zip = Join-Path $out "$name.zip"
    Remove-Item $zip -ErrorAction SilentlyContinue
    Compress-Archive -Path $exe -DestinationPath $zip -CompressionLevel Optimal
    $raw = (Get-Item $exe).Length
    $compressed = (Get-Item $zip).Length
    return [pscustomobject]@{
        name             = $name
        exe_bytes        = $raw
        exe_mib          = Get-MiB $raw
        zip_bytes        = $compressed
        zip_mib          = Get-MiB $compressed
    }
}

$pocOverlay = Build-Go 'overlay-poc' './cmd/overlay-poc'
$pocHost = Build-Go 'overlay-host' './cmd/overlay-host'
$noPayload = Build-Go 'launcher-nopayload' './cmd/launcher'

$wpfExe = Join-Path $out 'oddward-wpf.exe'
if ($SkipWPF) {
    if (-not (Test-Path $wpfExe)) { throw '-SkipWPF requires an existing oddward-wpf.exe' }
} else {
    & pwsh -NoProfile -File scripts/build-launcher.ps1 -Output $wpfExe
    if ($LASTEXITCODE -ne 0) { throw 'WPF-embedded launcher build failed' }
}

$rows = @(
    Measure-Artifact 'oddward-wpf' $wpfExe
    Measure-Artifact 'launcher-nopayload' $noPayload
    Measure-Artifact 'overlay-poc' $pocOverlay
    Measure-Artifact 'overlay-host' $pocHost
)

$full = $rows[0]
$bare = $rows[1]
$overlay = $rows[2]
$hostExe = $rows[3]

$report = [pscustomobject]@{
    when = (Get-Date).ToString('o')
    go_version = (& .tools/go1.27.3/go/bin/go.exe version)
    arch = 'windows/amd64'
    build_flags = '-trimpath -tags mygo_noinspector -ldflags "-H=windowsgui -s -w"'
    compression = 'ZIP Deflate Optimal (archive comparison, not the installer format)'
    artifacts = $rows
    deltas = [pscustomobject]@{
        wpf_payload_exe_mib = [Math]::Round($full.exe_mib - $bare.exe_mib, 2)
        wpf_payload_zip_mib = [Math]::Round($full.zip_mib - $bare.zip_mib, 2)
        poc_total_exe_mib = [Math]::Round($overlay.exe_mib + $hostExe.exe_mib, 2)
        poc_total_zip_mib = [Math]::Round($overlay.zip_mib + $hostExe.zip_mib, 2)
        full_vs_poc_exe_mib = [Math]::Round($full.exe_mib - ($overlay.exe_mib + $hostExe.exe_mib), 2)
        full_vs_poc_zip_mib = [Math]::Round($full.zip_mib - ($overlay.zip_mib + $hostExe.zip_mib), 2)
    }
}
$report | ConvertTo-Json -Depth 5 | Set-Content (Join-Path $out 'size-report.json')

Write-Host ''
Write-Host '| Artifact | EXE bytes | EXE MiB | ZIP bytes | ZIP MiB |'
Write-Host '|---|---:|---:|---:|---:|'
foreach ($row in $rows) {
    Write-Host ("| {0} | {1} | {2} | {3} | {4} |" -f $row.name, $row.exe_bytes, $row.exe_mib, $row.zip_bytes, $row.zip_mib)
}
Write-Host ''
Write-Host ("WPF payload: {0} MiB EXE, {1} MiB ZIP" -f $report.deltas.wpf_payload_exe_mib, $report.deltas.wpf_payload_zip_mib)
Write-Host ("PoC total: {0} MiB EXE, {1} MiB ZIP" -f $report.deltas.poc_total_exe_mib, $report.deltas.poc_total_zip_mib)
Write-Host ("Full product minus PoC: {0} MiB EXE, {1} MiB ZIP" -f $report.deltas.full_vs_poc_exe_mib, $report.deltas.full_vs_poc_zip_mib)
Write-Host ("Report: {0}" -f (Join-Path $out 'size-report.json'))
