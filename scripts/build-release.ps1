# SPDX-License-Identifier: GPL-3.0-or-later
# Builds one release: the installed layout, the signed update package and,
# when Inno Setup is available, the first-install setup.
#
#   pwsh -File scripts/build-release.ps1 -Version 0.1.0 [-Notes notes.md] [-Unsigned]
#
# Output (artifacts/release/<version>/):
#   stage/Oddward.exe, stage/app-<version>/...   installed layout
#   publish/oddward-<version>-app.zip, update.json, update.json.sig
#   publish/oddward-<version>-setup.exe
# Signing reads ODDWARD_UPDATE_KEY; -Unsigned skips it for local trials and
# produces nothing an installed launcher would accept.
param(
    [Parameter(Mandatory)][string]$Version,
    [string]$Notes = '',
    [string]$OutDir = 'artifacts/release',
    [switch]$Unsigned,
    [switch]$NoInstaller,
    [switch]$EmbedWPF
)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    if ($Version -notmatch '^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$') { throw "Version must be SemVer without v: $Version" }
    $release = Join-Path $OutDir $Version
    $stage = Join-Path $release 'stage'
    $app = Join-Path $stage "app-$Version"
    $publish = Join-Path $release 'publish'
    if (Test-Path -LiteralPath $release) { Remove-Item -LiteralPath $release -Recurse -Force }
    New-Item -ItemType Directory -Force -Path $app, $publish | Out-Null

    $launcherArgs = @{
        Output  = (Join-Path $app 'oddward-launcher.exe')
        Version = $Version
    }
    if ($EmbedWPF) { $launcherArgs['EmbedWPF'] = $true }
    & "$PSScriptRoot/build-launcher.ps1" @launcherArgs

    & "$PSScriptRoot/go.ps1" build -trimpath -ldflags '-H=windowsgui -s -w' -o (Join-Path $stage 'Oddward.exe') ./cmd/stub
    if ($LASTEXITCODE -ne 0) { throw 'stub build failed.' }

    if (!$Unsigned) {
        if (!$Notes) {
            $Notes = Join-Path $release 'notes.md'
            Set-Content -LiteralPath $Notes -Value "Oddward $Version" -Encoding utf8NoBOM
        }
        & "$PSScriptRoot/go.ps1" run ./tools/updatesign release -app $app -version $Version -notes $Notes -out $publish
        if ($LASTEXITCODE -ne 0) { throw 'signing failed; see updatesign output.' }
    }

    if (!$NoInstaller) {
        if (!$env:ODDWARD_ISCC) { & "$PSScriptRoot/install-inno.ps1" }
        $iscc = $env:ODDWARD_ISCC
        & $iscc "/DAppVersion=$Version" "/DStageDir=$((Resolve-Path $stage).Path)" "/O$((Resolve-Path $publish).Path)" 'installer/oddward.iss'
        if ($LASTEXITCODE -ne 0) { throw 'Inno Setup failed.' }
    }
    Get-ChildItem -LiteralPath $publish | ForEach-Object {
        Write-Host ('{0}  {1}' -f (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant(), $_.Name)
    }
}
finally {
    Pop-Location
}
