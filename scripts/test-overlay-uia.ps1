# SPDX-License-Identifier: GPL-3.0-or-later
# Smoke-test the Windows UI Automation provider of the shipping helper, with synthetic models.
# Run on a test desktop. This opens one standalone helper and never drives the keyboard/clipboard.
param([Parameter(Mandatory)][string]$Launcher)
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName UIAutomationClient
Add-Type -AssemblyName UIAutomationTypes
$start = [Diagnostics.ProcessStartInfo]::new((Resolve-Path -LiteralPath $Launcher).Path, '--cnlogin-ui')
$start.UseShellExecute = $false
$start.RedirectStandardInput = $true
$start.RedirectStandardOutput = $true
$start.CreateNoWindow = $true
$helper = [Diagnostics.Process]::Start($start)
function Send-Frame($frame) {
    $body = [Text.Encoding]::UTF8.GetBytes(($frame | ConvertTo-Json -Depth 6 -Compress))
    $stream = $helper.StandardInput.BaseStream
    $stream.Write([BitConverter]::GetBytes([uint32]$body.Length), 0, 4)
    $stream.Write($body, 0, $body.Length)
    $stream.Flush()
}
function Find-Control([string]$name, [System.Windows.Automation.ControlType]$type) {
    $deadline = [DateTime]::UtcNow.AddSeconds(8)
    $condition = [Windows.Automation.AndCondition]::new(
        [Windows.Automation.PropertyCondition]::new([Windows.Automation.AutomationElement]::NameProperty, $name),
        [Windows.Automation.PropertyCondition]::new([Windows.Automation.AutomationElement]::ControlTypeProperty, $type))
    do {
        $helper.Refresh()
        if ($helper.HasExited) { throw "helper exited unexpectedly ($($helper.ExitCode))" }
        if ($helper.MainWindowHandle -ne [IntPtr]::Zero) {
            $root = [Windows.Automation.AutomationElement]::FromHandle($helper.MainWindowHandle)
            $node = $root.FindFirst([Windows.Automation.TreeScope]::Descendants, $condition)
            if ($null -ne $node) { return $node }
        }
        Start-Sleep -Milliseconds 80
    } while ([DateTime]::UtcNow -lt $deadline)
    throw "provider did not expose '$name' as $($type.ProgrammaticName)"
}
try {
    Send-Frame @{version=1; type='model'; model=@{screen='phone'; remember=$true; primaryLabel='登录'; status='合成状态'}}
    foreach ($label in '请输入手机号', '请输入验证码') {
        $field = Find-Control $label ([Windows.Automation.ControlType]::Edit)
        $rect = $field.Current.BoundingRectangle
        if ($rect.IsEmpty -or $rect.Height -le 0 -or $rect.Width -le 0 -or -not $field.Current.IsKeyboardFocusable) {
            throw "$label has no focus target or bounds"
        }
    }
    $remember = Find-Control '在此设备保存账号' ([Windows.Automation.ControlType]::CheckBox)
    $toggle = $remember.GetCurrentPattern([Windows.Automation.TogglePattern]::Pattern)
    if ($toggle.Current.ToggleState -ne [Windows.Automation.ToggleState]::On) { throw 'remember state is not exposed as checked' }
    $login = Find-Control '登录' ([Windows.Automation.ControlType]::Button)
    if ($login.Current.IsEnabled) { throw 'empty phone form exposes an enabled login action' }
    $null = Find-Control '登录状态' ([Windows.Automation.ControlType]::Text)
    Send-Frame @{version=1; type='model'; model=@{screen='accounts'; selectedAccountId='fixture'; accounts=@(@{id='fixture';label='合成账号'})}}
    $rename = Find-Control '重命名' ([Windows.Automation.ControlType]::Button)
    $invoke = $rename.GetCurrentPattern([Windows.Automation.InvokePattern]::Pattern)
    $invoke.Invoke()
    $null = Find-Control '账号名称' ([Windows.Automation.ControlType]::Edit)
    Send-Frame @{version=1; type='close'}
    if (-not $helper.WaitForExit(5000) -or $helper.ExitCode -ne 0) { throw 'helper did not exit cleanly' }
    Write-Output 'PASS UIA: edit names, bounds and focusability; checked state; disabled button; status label; rename Invoke and edit.'
} finally {
    $helper.StandardInput.Close()
    if (-not $helper.HasExited -and -not $helper.WaitForExit(3000)) { $helper.Kill(); $helper.WaitForExit() }
    $helper.Dispose()
}
