# SPDX-License-Identifier: GPL-3.0-or-later
# Creates the Ed25519 key that signs update manifests. Run once, on your own
# machine, before the first release.
#
# The public key is written into internal/buildinfo/buildinfo.go (commit it).
# The private seed is printed once and never written to disk: store it as the
# GitHub Actions secret ODDWARD_UPDATE_KEY and keep an offline backup.
# Losing it means installed launchers can no longer be updated in place.
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    $buildinfo = 'internal/buildinfo/buildinfo.go'
    $source = Get-Content -LiteralPath $buildinfo -Raw
    if ($source -notmatch 'const UpdatePublicKey = ""') {
        throw "$buildinfo already has a public key. Replacing it strands every installed launcher; edit it by hand only if you mean to."
    }
    $out = & "$PSScriptRoot/go.ps1" run ./tools/updatesign keygen
    if ($LASTEXITCODE -ne 0) { throw 'keygen failed.' }
    $public = (($out | Where-Object { $_ -like 'public:*' }) -replace '^public:\s*', '').Trim()
    $private = (($out | Where-Object { $_ -like 'private:*' }) -replace '^private:\s*', '').Trim()
    $source = $source.Replace('const UpdatePublicKey = ""', "const UpdatePublicKey = `"$public`"")
    Set-Content -LiteralPath $buildinfo -Value $source -NoNewline -Encoding utf8NoBOM
    Write-Host "Public key written to $buildinfo. Commit it."
    Write-Host ''
    Write-Host 'Private key (shown once). Save it now:'
    Write-Host "  $private"
    Write-Host ''
    Write-Host '  gh secret set ODDWARD_UPDATE_KEY --repo nerv-base/oddward   # paste the line above'
    Write-Host 'Then keep an offline backup. Clear this terminal afterwards.'
}
finally {
    Pop-Location
}
