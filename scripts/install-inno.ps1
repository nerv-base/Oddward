# SPDX-License-Identifier: GPL-3.0-or-later
# Finds Inno Setup 6, installing it with Chocolatey on CI runners that lack
# it, and exports its compiler path as ODDWARD_ISCC for later steps.
$ErrorActionPreference = 'Stop'
function Find-Iscc {
    $candidates = @(
        (Get-Command iscc -ErrorAction SilentlyContinue).Source,
        (Join-Path ${env:ProgramFiles(x86)} 'Inno Setup 6/ISCC.exe'),
        (Join-Path $env:ProgramFiles 'Inno Setup 6/ISCC.exe'),
        (Join-Path $env:LOCALAPPDATA 'Programs/Inno Setup 6/ISCC.exe')
    )
    $candidates | Where-Object { $_ -and (Test-Path -LiteralPath $_ -PathType Leaf) } | Select-Object -First 1
}
$iscc = Find-Iscc
if (!$iscc) {
    if (!$env:CI) { throw 'Inno Setup 6 not found. Install it from https://jrsoftware.org/isdl.php.' }
    choco install innosetup --no-progress -y
    if ($LASTEXITCODE -ne 0) { throw 'Installing Inno Setup failed.' }
    $iscc = Find-Iscc
    if (!$iscc) { throw 'Inno Setup installed but ISCC.exe was not found.' }
}
Write-Host "Inno Setup compiler: $iscc"
if ($env:GITHUB_ENV) { "ODDWARD_ISCC=$iscc" | Out-File -FilePath $env:GITHUB_ENV -Append -Encoding utf8 }
$env:ODDWARD_ISCC = $iscc
