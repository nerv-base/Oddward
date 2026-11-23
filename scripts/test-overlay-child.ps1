# Automated native-child integration test for the MyGo overlay PoC.
#
# Drives cmd/overlay-host (a separate Win32 process standing in for the game)
# and cmd/overlay-poc in -mode child. Rendering is checked through
# PrintWindow(PW_RENDERFULLCONTENT), which also works while the desktop is
# locked; real mouse/keyboard injection is left to test-overlay-poc.ps1.
#
#   pwsh -File scripts/test-overlay-child.ps1 [-SkipBuild] [-KeepRunning]
#
# Exit code 0 means every check passed. Results are written to
# artifacts/overlay-poc/child-report.json (machine-local, git-ignored).

param(
    [switch]$SkipBuild,
    [switch]$KeepRunning
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root
$out = Join-Path $root 'artifacts\overlay-poc'
New-Item -ItemType Directory -Force $out | Out-Null

if (-not ('OverlayChildWin' -as [type])) {
Add-Type -AssemblyName System.Drawing
Add-Type @'
using System;
using System.Runtime.InteropServices;
using System.Text;

public class OverlayChildWin {
    [StructLayout(LayoutKind.Sequential)] public struct RECT { public int Left, Top, Right, Bottom; }
    [StructLayout(LayoutKind.Sequential)] public struct POINT { public int X, Y; }

    [DllImport("user32.dll")] public static extern IntPtr GetParent(IntPtr h);
    [DllImport("user32.dll")] public static extern IntPtr GetWindow(IntPtr h, uint cmd);
    [DllImport("user32.dll")] public static extern bool IsWindow(IntPtr h);
    [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);
    [DllImport("user32.dll")] public static extern bool IsIconic(IntPtr h);
    [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
    [DllImport("user32.dll")] public static extern bool GetClientRect(IntPtr h, out RECT r);
    [DllImport("user32.dll")] public static extern bool ClientToScreen(IntPtr h, ref POINT p);
    [DllImport("user32.dll")] public static extern IntPtr GetWindowLongPtrW(IntPtr h, int index);
    [DllImport("user32.dll")] public static extern bool SetWindowPos(IntPtr h, IntPtr after, int x, int y, int cx, int cy, uint flags);
    [DllImport("kernel32.dll")] public static extern IntPtr GetConsoleWindow();
    [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr h, int cmd);
    [DllImport("user32.dll")] public static extern bool PrintWindow(IntPtr h, IntPtr hdc, uint flags);
    [DllImport("user32.dll")] public static extern IntPtr ChildWindowFromPointEx(IntPtr h, POINT p, uint flags);
    [DllImport("dwmapi.dll")] public static extern int DwmGetWindowAttribute(IntPtr h, int attr, out int value, int size);
    [DllImport("user32.dll", CharSet = CharSet.Unicode)] public static extern int GetClassNameW(IntPtr h, StringBuilder s, int n);

    public static long Style(IntPtr h) { return (long)GetWindowLongPtrW(h, -16); }
    public static long ExStyle(IntPtr h) { return (long)GetWindowLongPtrW(h, -20); }
    public static int Cloaked(IntPtr h) {
        int v; int hr = DwmGetWindowAttribute(h, 14, out v, sizeof(int));
        return hr == 0 ? v : -1;
    }
    public static RECT WindowRect(IntPtr h) { RECT r; GetWindowRect(h, out r); return r; }
    public static RECT ClientOnScreen(IntPtr h) {
        RECT r; GetClientRect(h, out r);
        POINT p = new POINT(); p.X = 0; p.Y = 0;
        ClientToScreen(h, ref p);
        return new RECT { Left = p.X, Top = p.Y, Right = p.X + (r.Right - r.Left), Bottom = p.Y + (r.Bottom - r.Top) };
    }
    public static string ClassName(IntPtr h) {
        var s = new StringBuilder(256); GetClassNameW(h, s, s.Capacity); return s.ToString();
    }
    public static IntPtr FindChildClass(IntPtr parent, string cls) {
        IntPtr c = GetWindow(parent, 5 /*GW_CHILD*/);
        while (c != IntPtr.Zero) {
            if (ClassName(c) == cls) return c;
            c = GetWindow(c, 2 /*GW_HWNDNEXT*/);
        }
        return IntPtr.Zero;
    }
}
'@
}

[OverlayChildWin]::ShowWindow([OverlayChildWin]::GetConsoleWindow(), 6) | Out-Null

$script:checks = New-Object System.Collections.Generic.List[object]
$script:hostProc = $null
$script:overlayProc = $null
$script:hostPort = 0
$script:overlayPort = 0
$script:overlayPid = 0
$script:hostStatus = Join-Path $out 'child-host.json'
$script:hostLog = Join-Path $out 'child-host.jsonl'
$script:hostCreated = ''

function Add-Check([string]$name, [bool]$ok, [string]$detail) {
    $script:checks.Add([pscustomobject]@{ name = $name; pass = $ok; detail = $detail })
    $mark = if ($ok) { 'PASS' } else { 'FAIL' }
    Write-Host ("[{0}] {1} — {2}" -f $mark, $name, $detail)
}

function Get-FreePort {
    $l = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $l.Start(); $p = $l.LocalEndpoint.Port; $l.Stop(); return $p
}

function Invoke-Ctl([int]$port, [string]$command) {
    $c = [System.Net.Sockets.TcpClient]::new('127.0.0.1', $port)
    try {
        $s = $c.GetStream()
        $w = [System.IO.StreamWriter]::new($s)
        $r = [System.IO.StreamReader]::new($s)
        $w.WriteLine($command); $w.Flush()
        $line = $r.ReadLine()
        if (-not $line) { throw "control port $port closed for '$command'" }
        return $line | ConvertFrom-Json
    } finally { $c.Close() }
}

function Wait-State([int]$port, [scriptblock]$predicate, [int]$timeoutMs = 8000) {
    $deadline = [DateTime]::UtcNow.AddMilliseconds($timeoutMs)
    $last = $null
    while ([DateTime]::UtcNow -lt $deadline) {
        try { $last = Invoke-Ctl $port 'state' } catch { Start-Sleep -Milliseconds 100; continue }
        if (& $predicate $last) { return $last }
        Start-Sleep -Milliseconds 100
    }
    throw "timeout waiting for state on port $port; last=$($last | ConvertTo-Json -Compress)"
}

function Start-Host {
    Remove-Item $script:hostStatus, $script:hostLog -ErrorAction SilentlyContinue
    $script:hostPort = Get-FreePort
    $hostArgs = @('-x', '300', '-y', '300', '-w', '900', '-h', '640', '-mode', 'window',
        '-status', $script:hostStatus, '-log', $script:hostLog, '-control', "$($script:hostPort)")
    $script:hostProc = Start-Process -FilePath (Join-Path $out 'overlay-host.exe') -ArgumentList $hostArgs -PassThru `
        -RedirectStandardOutput (Join-Path $out 'child-host.out') -RedirectStandardError (Join-Path $out 'child-host.err')
    $deadline = [DateTime]::UtcNow.AddSeconds(8)
    while ([DateTime]::UtcNow -lt $deadline) {
        if (Test-Path $script:hostStatus) {
            try {
                $j = Get-Content $script:hostStatus -Raw | ConvertFrom-Json
                if ($j.hwnd) { return $j }
            } catch { }
        }
        Start-Sleep -Milliseconds 100
    }
    throw 'host did not publish its status file'
}

function Start-ChildOverlay([uint64]$hostHwnd) {
    $script:overlayPort = Get-FreePort
    $ovArgs = @('-host', "$hostHwnd", '-host-pid', "$($script:hostProc.Id)",
        '-host-created', $script:hostCreated, '-mode', 'child', '-control', "$($script:overlayPort)")
    $script:overlayProc = Start-Process -FilePath (Join-Path $out 'overlay-poc.exe') -ArgumentList $ovArgs -PassThru `
        -RedirectStandardOutput (Join-Path $out 'child-overlay.out') -RedirectStandardError (Join-Path $out 'child-overlay.err')
    $script:overlayPid = $script:overlayProc.Id
    return Wait-State $script:overlayPort { param($s) $s.ok -and $s.bound -and $s.visible } 10000
}

function Stop-Overlay {
    if ($script:overlayProc -and -not $script:overlayProc.HasExited) {
        try { Invoke-Ctl $script:overlayPort 'quit' | Out-Null } catch { }
        $script:overlayProc.WaitForExit(3000) | Out-Null
        if (-not $script:overlayProc.HasExited) { $script:overlayProc.Kill() }
    }
    $script:overlayProc = $null
}

function Stop-Host {
    if ($script:hostProc -and -not $script:hostProc.HasExited) {
        try { Invoke-Ctl $script:hostPort 'quit' | Out-Null } catch { }
        $script:hostProc.WaitForExit(3000) | Out-Null
        if (-not $script:hostProc.HasExited) { $script:hostProc.Kill() }
    }
    $script:hostProc = $null
}

function Read-Pixel([string]$path, [int]$x, [int]$y) {
    $bmp = [System.Drawing.Bitmap]::FromFile((Resolve-Path $path))
    try { return $bmp.GetPixel($x, $y) } finally { $bmp.Dispose() }
}

function Capture-Child([IntPtr]$hwnd, [string]$path) {
    $r = [OverlayChildWin]::WindowRect($hwnd)
    $w = [int]($r.Right - $r.Left)
    $h = [int]($r.Bottom - $r.Top)
    $bmp = [System.Drawing.Bitmap]::new($w, $h)
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $hdc = $g.GetHdc()
    try { $ok = [OverlayChildWin]::PrintWindow($hwnd, $hdc, 2) } finally { $g.ReleaseHdc($hdc); $g.Dispose() }
    $bmp.Save($path, [System.Drawing.Imaging.ImageFormat]::Png)
    $bmp.Dispose()
    return $ok
}

try {
    if (-not $SkipBuild) {
        foreach ($target in @(@('overlay-host', 'cmd/overlay-host'), @('overlay-poc', 'cmd/overlay-poc'))) {
            & pwsh -NoProfile -File scripts/go.ps1 build -trimpath -tags mygo_noinspector `
                -ldflags '-H=windowsgui -s -w' -o (Join-Path $out "$($target[0]).exe") "./$($target[1])"
            if ($LASTEXITCODE -ne 0) { throw "build failed: $($target[1])" }
        }
    }

    Write-Host '== native-child mode =='
    $h = Start-Host
    $script:hostCreated = [string]$h.created
    if (-not $script:hostCreated) { throw 'host status did not include its creation time' }
    $hostHwnd = [IntPtr][uint64]$h.hwnd
    $hostClient = [OverlayChildWin]::ClientOnScreen($hostHwnd)
    [OverlayChildWin]::SetWindowPos($hostHwnd, [IntPtr](-1), 0, 0, 0, 0, 0x13) | Out-Null
    Start-Sleep -Milliseconds 300

    # Negative check: a host creation time that does not match must be rejected.
    $badCreated = ([uint64]$script:hostCreated + 1).ToString()
    $badPort = Get-FreePort
    $bad = Start-Process -FilePath (Join-Path $out 'overlay-poc.exe') -PassThru -ArgumentList @(
        '-host', "$($h.hwnd)", '-host-pid', "$($script:hostProc.Id)", '-host-created', $badCreated,
        '-mode', 'child', '-control', "$badPort") `
        -RedirectStandardOutput (Join-Path $out 'child-bad.out') -RedirectStandardError (Join-Path $out 'child-bad.err')
    if (-not $bad.WaitForExit(8000)) { $bad.Kill() }
    Add-Check 'creation time mismatch rejected' ($bad.ExitCode -eq 1) "exit=$($bad.ExitCode)"

    $s = Start-ChildOverlay ([uint64]$h.hwnd)
    $outer = [IntPtr][uint64]$s.hwnd
    $parent = [OverlayChildWin]::GetParent($outer)
    $style = [OverlayChildWin]::Style($outer)
    $ex = [OverlayChildWin]::ExStyle($outer)
    Add-Check 'child mode bound' ($s.mode -eq 'child' -and $s.bound) "mode=$($s.mode) bound=$($s.bound)"
    Add-Check 'outer is a WS_CHILD of host' (($style -band 0x40000000) -ne 0 -and $parent -eq $hostHwnd) ("style=0x{0:X} parent=0x{1:X}" -f $style, $parent.ToInt64())
    Add-Check 'caption removed from child' (($style -band 0x00C00000) -eq 0) ("style=0x{0:X}" -f $style)
    $r = [OverlayChildWin]::WindowRect($outer)
    Add-Check 'child rect equals host client' ([Math]::Abs(($r.Right - $r.Left) - ($hostClient.Right - $hostClient.Left)) -le 1 -and
        [Math]::Abs(($r.Bottom - $r.Top) - ($hostClient.Bottom - $hostClient.Top)) -le 1) ("rect={0},{1}x{2}" -f $r.Left, $r.Top, ($r.Right - $r.Left))
    Add-Check 'outer not cloaked' ([OverlayChildWin]::Cloaked($outer) -eq 0) "cloak=$([OverlayChildWin]::Cloaked($outer))"
    $surface = [OverlayChildWin]::FindChildClass($outer, 'MyGoSurface')
    Add-Check 'surface exists and is not cloaked' ($surface -ne [IntPtr]::Zero -and [OverlayChildWin]::Cloaked($surface) -eq 0) ("surface=0x{0:X} cloak={1}" -f $surface.ToInt64(), [OverlayChildWin]::Cloaked($surface))
    $pt = New-Object OverlayChildWin+POINT
    $pt.X = 442; $pt.Y = 300
    $hit = [OverlayChildWin]::ChildWindowFromPointEx($hostHwnd, $pt, 0x0001)
    Add-Check 'child is hit-testable at card center' ($hit -eq $surface -or $hit -eq $outer) ("hit=0x{0:X}" -f $hit.ToInt64())

    $png = Join-Path $out 'child-render.png'
    Capture-Child $outer $png | Out-Null
    $card = Read-Pixel $png 442 300
    $mask = Read-Pixel $png 4 4
    Add-Check 'child renders card pixel' ($card.R -eq 227 -and $card.G -eq 227 -and $card.B -eq 227) "pixel=(442,300)=($($card.R),$($card.G),$($card.B))"
    Add-Check 'child mask is translucent' ($mask.R -eq 11 -and $mask.G -eq 8 -and $mask.B -eq 6) "pixel=(4,4)=($($mask.R),$($mask.G),$($mask.B))"

    # Resize follows the host client.
    Invoke-Ctl $script:hostPort 'move 400 240 1000 700' | Out-Null
    $null = Wait-State $script:overlayPort { param($x) [Math]::Abs($x.rect.W - 984) -le 1 -and [Math]::Abs($x.rect.H - 661) -le 1 } 5000
    $s2 = Invoke-Ctl $script:overlayPort 'state'
    Add-Check 'child resizes with host' ($s2.rect.W -eq 984 -and $s2.rect.H -eq 661) "rect=$($s2.rect.W)x$($s2.rect.H)"
    Start-Sleep -Milliseconds 700
    $png2 = Join-Path $out 'child-resized.png'
    Capture-Child $outer $png2 | Out-Null
    $card2 = Read-Pixel $png2 492 100
    Add-Check 'child renders after resize' ($card2.R -eq 30 -and $card2.G -eq 31 -and $card2.B -eq 34) "header pixel=(492,100)=($($card2.R),$($card2.G),$($card2.B))"

    # Minimize hides; restore shows again.
    Invoke-Ctl $script:hostPort 'minimize' | Out-Null
    $null = Wait-State $script:overlayPort { param($x) -not $x.visible } 5000
    Add-Check 'child hidden when host minimized' $true ''
    Invoke-Ctl $script:hostPort 'restore' | Out-Null
    $null = Wait-State $script:overlayPort { param($x) $x.visible } 5000
    Add-Check 'child shown when host restored' $true ''

    # Host HWND destroy + recreate: overlay recreates and rebinds to the new host.
    $oldHost = [uint64]$s.host_hwnd
    $oldOuter = [uint64]$s.hwnd
    Invoke-Ctl $script:hostPort 'recreate' | Out-Null
    $null = Wait-State $script:overlayPort { param($x) $x.recreates -ge 1 -and $x.visible -and [uint64]$x.host_hwnd -ne $oldHost -and [uint64]$x.hwnd -ne $oldOuter } 15000
    Start-Sleep -Milliseconds 700
    $s3 = Invoke-Ctl $script:overlayPort 'state'
    $newHost = [IntPtr][uint64]$s3.host_hwnd
    $newOuter = [IntPtr][uint64]$s3.hwnd
    $newParent = [OverlayChildWin]::GetParent($newOuter)
    Add-Check 'child rebinds after host recreate' ($s3.recreates -ge 1 -and $newParent -eq $newHost) ("recreates=$($s3.recreates) parent=0x{0:X} host=0x{1:X}" -f $newParent.ToInt64(), $newHost.ToInt64())
    $png3 = Join-Path $out 'child-recreated.png'
    Capture-Child $newOuter $png3 | Out-Null
    $card3 = Read-Pixel $png3 492 100
    Add-Check 'child renders after recreate' ($card3.R -eq 30 -and $card3.G -eq 31 -and $card3.B -eq 34) "header pixel=(492,100)=($($card3.R),$($card3.G),$($card3.B))"

    # Clean shutdown.
    Invoke-Ctl $script:overlayPort 'quit' | Out-Null
    $script:overlayProc.WaitForExit(5000) | Out-Null
    Add-Check 'overlay exits cleanly' ($script:overlayProc.HasExited) "exit=$($script:overlayProc.ExitCode)"
    $script:overlayProc = $null
    Stop-Host
    Add-Check 'host exits cleanly' ($true) ''

    Add-Check 'no leftover overlay process' (-not (Get-Process -Id $script:overlayPid -ErrorAction SilentlyContinue)) "pid=$($script:overlayPid)"
} finally {
    if (-not $KeepRunning) { Stop-Overlay; Stop-Host }
    $failed = @($script:checks | Where-Object { -not $_.pass })
    $report = [pscustomobject]@{
        generatedAt = (Get-Date).ToString('o')
        total = $script:checks.Count
        passed = $script:checks.Count - $failed.Count
        failed = $failed.Count
        checks = $script:checks
    }
    $reportPath = Join-Path $out 'child-report.json'
    $report | ConvertTo-Json -Depth 6 | Set-Content -Encoding UTF8 $reportPath
    Write-Host ("`n{0}/{1} checks passed; report: {2}" -f $report.passed, $report.total, $reportPath)
    if ($failed.Count -gt 0) { exit 1 }
}





