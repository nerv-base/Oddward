# SPDX-License-Identifier: GPL-3.0-or-later
# Authored helper and synthetic fixtures only; no network, LoginHost, SDK, or game.
[CmdletBinding()]
param([ValidatePattern('^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$')][string]$RunName='synthetic',[switch]$MotionOnly,[switch]$ChildOnly)
$ErrorActionPreference='Stop'
$repo=(Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$run=Join-Path $repo "artifacts/loginui/$RunName"
$out=Join-Path $run 'tests'; $obj=Join-Path $run 'obj-tests'; $render=Join-Path $run 'render'
$uiOut=Join-Path $run 'ui'; $uiObj=Join-Path $run 'obj-ui'
New-Item -ItemType Directory -Force $out,$obj,$render,$uiOut,$uiObj | Out-Null
$env:DOTNET_CLI_TELEMETRY_OPTOUT='1';$env:DOTNET_SKIP_FIRST_TIME_EXPERIENCE='1'
Push-Location $PSScriptRoot
try {
 if ((& dotnet --version).Trim() -ne '10.0.303') {throw 'Expected offline SDK 10.0.303'}
 $config=Join-Path $PSScriptRoot 'NuGet.Config'
 & dotnet build (Join-Path $PSScriptRoot 'Oddward.LoginUi.csproj') -c Release --no-incremental --configfile $config "-p:BaseIntermediateOutputPath=$uiObj/" "-p:MSBuildProjectExtensionsPath=$uiObj/" "-p:OutputPath=$uiOut/" -v:minimal
 if($LASTEXITCODE -ne 0){throw 'Login UI helper build failed'}
 & dotnet build (Join-Path $PSScriptRoot 'Tests/Tests.csproj') -c Release --no-incremental --configfile $config "-p:BaseIntermediateOutputPath=$obj/" "-p:MSBuildProjectExtensionsPath=$obj/" "-p:OutputPath=$out/" -v:minimal
 if($LASTEXITCODE -ne 0){throw 'Synthetic UI build failed'}
 $testArguments=@($render,(Join-Path $uiOut 'Oddward.LoginUi.exe'))
 if($MotionOnly -and $ChildOnly){throw 'Choose MotionOnly or ChildOnly, not both'}
 if($MotionOnly){$testArguments+='--motion-only'}
 if($ChildOnly){$testArguments+='--child-only'}
 & (Join-Path $out 'Oddward.LoginUi.Tests.exe') @testArguments
 if($LASTEXITCODE -ne 0){throw "Synthetic behavioral checks failed: $render"}
 Write-Output "Synthetic UI evidence: $render"
} finally {Pop-Location}
