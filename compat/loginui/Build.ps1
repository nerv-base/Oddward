# SPDX-License-Identifier: GPL-3.0-or-later
# Publishes the authored WPF login window helper as a self-contained win-x64 app.
# Release builds package this output into the launcher; development builds may
# read it from login-compat/Oddward.LoginUi.exe next to the executable.
param(
    [string]$Output = 'artifacts/loginui/build',
    [switch]$FrameworkDependent
)
$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$out = if ([IO.Path]::IsPathRooted($Output)) { [IO.Path]::GetFullPath($Output) } else { [IO.Path]::GetFullPath((Join-Path $repo $Output)) }
$obj = Join-Path $repo 'artifacts/loginui/obj-build'
if (Test-Path -LiteralPath $out) { Remove-Item -LiteralPath $out -Recurse -Force }
New-Item -ItemType Directory -Force -Path $out, $obj | Out-Null
$env:DOTNET_CLI_TELEMETRY_OPTOUT = '1'
$env:DOTNET_SKIP_FIRST_TIME_EXPERIENCE = '1'
Push-Location $PSScriptRoot
try {
    if ((& dotnet --version).Trim() -ne '10.0.303') { throw 'Expected offline SDK 10.0.303' }
    $config = Join-Path $PSScriptRoot 'NuGet.Config'
    $selfContained = if ($FrameworkDependent) { 'false' } else { 'true' }
    & dotnet publish (Join-Path $PSScriptRoot 'Oddward.LoginUi.csproj') -c Release -r win-x64 "--self-contained=$selfContained" --configfile $config "-p:BaseIntermediateOutputPath=$obj/" "-p:MSBuildProjectExtensionsPath=$obj/" "-p:PublishDir=$out/" -v:minimal
    if ($LASTEXITCODE -ne 0) { throw 'Login UI helper publish failed' }
    if (!(Test-Path -LiteralPath (Join-Path $out 'Oddward.LoginUi.exe') -PathType Leaf)) { throw 'Login UI helper exe missing' }
    Write-Output "Login UI helper: $out"
}
finally { Pop-Location }
