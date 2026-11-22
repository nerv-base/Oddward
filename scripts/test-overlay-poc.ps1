# Automated Windows integration test for the MyGo overlay PoC.
#
# Runs cmd/overlay-host (a separate Win32 process standing in for the game)
# and cmd/overlay-poc, then drives both through their loopback control ports.
# Everything asserted here is real HWND behaviour: no mocks, no fake surface.
#
#   pwsh -File scripts/test-overlay-poc.ps1 [-SkipBuild] [-KeepRunning]
#
# Exit code 0 means every check passed. Results are written to
# artifacts/overlay-poc/report.json (machine-local, git-ignored).

param(
    [switch]$SkipBuild,
    [switch]$KeepRunning
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root
$out = Join-Path $root 'artifacts\overlay-poc'
New-Item -ItemType Directory -Force $out | Out-Null

if (-not ('OverlayTestWin' -as [type])) {
Add-Type @'
using System;
using System.Runtime.InteropServices;
using System.Text;

public class OverlayTestWin {
    [StructLayout(LayoutKind.Sequential)] public struct RECT { public int Left, Top, Right, Bottom; }
    [StructLayout(LayoutKind.Sequential)] public struct POINT { public int X, Y; }

    [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h);
    [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
    [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
    [DllImport("user32.dll")] public static extern void mouse_event(uint flags, uint dx, uint dy, uint data, UIntPtr extra);
    [DllImport("user32.dll")] public static extern void keybd_event(byte vk, byte scan, uint flags, UIntPtr extra);
    [DllImport("user32.dll")] public static extern short VkKeyScanW(char ch);
    [DllImport("user32.dll")] public static extern IntPtr WindowFromPoint(POINT p);
    [DllImport("user32.dll")] public static extern IntPtr GetWindow(IntPtr h, uint cmd);
    [DllImport("user32.dll")] public static extern IntPtr GetParent(IntPtr h);
    [DllImport("user32.dll")] public static extern IntPtr GetAncestor(IntPtr h, uint flags);
    [DllImport("user32.dll")] public static extern bool IsWindow(IntPtr h);
    [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);
    [DllImport("user32.dll")] public static extern bool IsIconic(IntPtr h);
    [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
    [DllImport("user32.dll")] public static extern bool GetClientRect(IntPtr h, out RECT r);
    [DllImport("user32.dll")] public static extern bool ClientToScreen(IntPtr h, ref POINT p);
    [DllImport("user32.dll")] public static extern IntPtr GetWindowLongPtrW(IntPtr h, int index);
    [DllImport("kernel32.dll")] public static extern IntPtr GetConsoleWindow();
    [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr h, int cmd);
    [DllImport("user32.dll")] public static extern uint GetDpiForWindow(IntPtr h);
    [DllImport("user32.dll")] public static extern bool SetWindowPos(IntPtr h, IntPtr after, int x, int y, int cx, int cy, uint flags);

    public static void Tap(byte vk) {
        keybd_event(vk, 0, 0, UIntPtr.Zero);
        System.Threading.Thread.Sleep(12);
        keybd_event(vk, 0, 2, UIntPtr.Zero);
        System.Threading.Thread.Sleep(12);
    }
    public static void Down(byte vk) { keybd_event(vk, 0, 0, UIntPtr.Zero); }
    public static void Up(byte vk) { keybd_event(vk, 0, 2, UIntPtr.Zero); }
    public static void Click(int x, int y) {
        SetCursorPos(x, y);
        mouse_event(2, 0, 0, 0, UIntPtr.Zero);
        mouse_event(4, 0, 0, 0, UIntPtr.Zero);
    }
    public static void Text(string s) {
        foreach (char ch in s) {
            short scan = VkKeyScanW(ch);
            byte vk = (byte)(scan & 0xff);
            bool shift = (scan & 0x100) != 0;
            if (shift) Down(0x10);
            Tap(vk);
            if (shift) Up(0x10);
            System.Threading.Thread.Sleep(30);
        }
    }
    public static long Style(IntPtr h) { return (long)GetWindowLongPtrW(h, -16); }
    public static long ExStyle(IntPtr h) { return (long)GetWindowLongPtrW(h, -20); }
    public static RECT ClientOnScreen(IntPtr h) {
        RECT r; GetClientRect(h, out r);
        POINT p = new POINT(); p.X = 0; p.Y = 0;
        ClientToScreen(h, ref p);
        return new RECT { Left = p.X, Top = p.Y, Right = p.X + (r.Right - r.Left), Bottom = p.Y + (r.Bottom - r.Top) };
    }
    public static IntPtr HitTest(int x, int y) {
        POINT p; p.X = x; p.Y = y;
        return WindowFromPoint(p);
    }
}
'@
}

# Keep the test host and overlay unobstructed by the console window running
# this script; the file log still receives every line.
[OverlayTestWin]::ShowWindow([OverlayTestWin]::GetConsoleWindow(), 6) | Out-Null

$script:checks = New-Object System.Collections.Generic.List[object]
$script:hostProc = $null
$script:overlayProc = $null
$script:hostPort = 0
$script:overlayPort = 0
$script:hostStatus = Join-Path $out 'host.json'
$script:hostLog = Join-Path $out 'host.jsonl'
$script:hostCreated = ''
$script:lastOverlayExited = $false
$script:lastHostExited = $false

function Add-Check([string]$name, [bool]$ok, [string]$detail) {
    $script:checks.Add([pscustomobject]@{ name = $name; pass = $ok; detail = $detail })
    $mark = if ($ok) { 'PASS' } else { 'FAIL' }
    Write-Host ("[{0}] {1} — {2}" -f $mark, $name, $detail)
}

function Get-FreePort {
    $l = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $l.Start()
    $p = $l.LocalEndpoint.Port
    $l.Stop()
    return $p
}

function Invoke-Ctl([int]$port, [string]$command) {
    $c = [System.Net.Sockets.TcpClient]::new('127.0.0.1', $port)
    try {
        $s = $c.GetStream()
        $w = [System.IO.StreamWriter]::new($s)
        $r = [System.IO.StreamReader]::new($s)
        $w.WriteLine($command)
        $w.Flush()
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

function Start-Host([string]$mode) {
    Remove-Item $script:hostStatus, $script:hostLog -ErrorAction SilentlyContinue
    $script:hostPort = Get-FreePort
    $hostArgs = @('-x', '300', '-y', '300', '-w', '900', '-h', '640', '-mode', $mode,
        '-status', $script:hostStatus, '-log', $script:hostLog, '-control', "$($script:hostPort)")
    $script:hostProc = Start-Process -FilePath (Join-Path $out 'overlay-host.exe') -ArgumentList $hostArgs -PassThru `
        -RedirectStandardOutput (Join-Path $out 'host.out') -RedirectStandardError (Join-Path $out 'host.err')
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

function Start-Overlay([uint64]$hostHwnd, [string[]]$extra) {
    $script:overlayPort = Get-FreePort
    $ovArgs = @('-host', "$hostHwnd", '-host-created', $script:hostCreated,
        '-mode', 'owner', '-control', "$($script:overlayPort)") + $extra
    $script:overlayProc = Start-Process -FilePath (Join-Path $out 'overlay-poc.exe') -ArgumentList $ovArgs -PassThru `
        -RedirectStandardOutput (Join-Path $out 'overlay.out') -RedirectStandardError (Join-Path $out 'overlay.err')
    $st = Wait-State $script:overlayPort { param($s) $s.ok -and $s.bound -and $s.visible } 8000
    # Real games are foreground when the login window appears. The test
    # machine may have an always-on-top desktop window over the fixed test
    # position, so put the host/overlay pair in the topmost band for the
    # duration of the test. This is test-rig z-order management only; the PoC
    # itself creates a non-topmost window (its ex-style is asserted before
    # this line).
    [OverlayTestWin]::SetWindowPos([IntPtr][uint64]$st.host_hwnd, [IntPtr](-1), 0, 0, 0, 0, 0x13) | Out-Null
    [OverlayTestWin]::SetWindowPos([IntPtr][uint64]$st.hwnd, [IntPtr](-1), 0, 0, 0, 0, 0x13) | Out-Null
    Start-Sleep -Milliseconds 700
}

function Stop-Overlay {
    $script:lastOverlayExited = $false
    if ($script:overlayProc -and -not $script:overlayProc.HasExited) {
        try { Invoke-Ctl $script:overlayPort 'quit' | Out-Null } catch { }
        $script:overlayProc.WaitForExit(3000) | Out-Null
        if (-not $script:overlayProc.HasExited) { $script:overlayProc.Kill() }
        $script:lastOverlayExited = $script:overlayProc.HasExited
    }
    $script:overlayProc = $null
}

function Stop-Host {
    $script:lastHostExited = $false
    if ($script:hostProc -and -not $script:hostProc.HasExited) {
        try { Invoke-Ctl $script:hostPort 'quit' | Out-Null } catch { }
        $script:hostProc.WaitForExit(3000) | Out-Null
        if (-not $script:hostProc.HasExited) { $script:hostProc.Kill() }
        $script:lastHostExited = $script:hostProc.HasExited
    }
    $script:hostProc = $null
}

function Host-ClickCount {
    if (-not (Test-Path $script:hostLog)) { return 0 }
    @(Get-Content $script:hostLog | Where-Object { $_ -match '"event":"click"' }).Count
}

function Get-ScreenPixel([int]$x, [int]$y) {
    Add-Type -AssemblyName System.Drawing
    $bmp = [System.Drawing.Bitmap]::new(1, 1)
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.CopyFromScreen($x, $y, 0, 0, [System.Drawing.Size]::new(1, 1))
    $p = $bmp.GetPixel(0, 0)
    $g.Dispose(); $bmp.Dispose()
    return $p
}

try {
    if (-not $SkipBuild) {
        foreach ($target in @(@('overlay-host', 'cmd/overlay-host'), @('overlay-poc', 'cmd/overlay-poc'))) {
            & pwsh -NoProfile -File scripts/go.ps1 build -trimpath -tags mygo_noinspector `
                -ldflags '-H=windowsgui -s -w' -o (Join-Path $out "$($target[0]).exe") "./$($target[1])"
            if ($LASTEXITCODE -ne 0) { throw "build failed: $($target[1])" }
        }
    }

    Write-Host '== owner-mode overlay =='
    $h = Start-Host 'window'
    $script:hostCreated = [string]$h.created
    if (-not $script:hostCreated) { throw 'host status did not include its creation time' }
    $hostHwnd = [IntPtr][uint64]$h.hwnd
    # Raise the host above unrelated desktop windows so the real screen capture
    # and synthetic clicks see the host/overlay pair, not whatever window the
    # developer left open over the test position.
    [OverlayTestWin]::SetWindowPos($hostHwnd, [IntPtr]::Zero, 0, 0, 0, 0, 0x13) | Out-Null
    [OverlayTestWin]::SetForegroundWindow($hostHwnd) | Out-Null
    Start-Sleep -Milliseconds 300
    $badCreated = ([uint64]$script:hostCreated + 1).ToString()
    $badProc = Start-Process -FilePath (Join-Path $out 'overlay-poc.exe') `
        -ArgumentList @('-host', "$($h.hwnd)", '-host-created', $badCreated, '-mode', 'owner') -PassThru `
        -RedirectStandardOutput (Join-Path $out 'overlay-bad.out') -RedirectStandardError (Join-Path $out 'overlay-bad.err')
    $badExited = $badProc.WaitForExit(8000)
    if (-not $badExited) { $badProc.Kill() }
    Add-Check 'creation time mismatch rejected' ($badExited -and $badProc.ExitCode -ne 0) "exit=$($badProc.ExitCode)"
    $hostClient = [OverlayTestWin]::ClientOnScreen($hostHwnd)
    $bg = Get-ScreenPixel ([int]$hostClient.Left + 10) ([int]$hostClient.Top + 10)
    Start-Overlay $h.hwnd @()
    $s = Invoke-Ctl $script:overlayPort 'state'
    $client = [OverlayTestWin]::ClientOnScreen($hostHwnd)
    $r = $s.rect
    $close = [Math]::Abs($r.X - $client.Left) -le 1 -and [Math]::Abs($r.Y - $client.Top) -le 1 -and
             [Math]::Abs($r.W - ($client.Right - $client.Left)) -le 1 -and [Math]::Abs($r.H - ($client.Bottom - $client.Top)) -le 1
    Add-Check 'transparent DirectComposition window' (($s.ex -band 0x200000) -ne 0) ("style=0x{0:x} ex=0x{1:x}" -f $s.style, $s.ex)
    Add-Check 'owner binding' ([OverlayTestWin]::GetWindow([IntPtr][uint64]$s.hwnd, 4) -eq $hostHwnd) 'GW_OWNER == host HWND'
    Add-Check 'position matches host client' $close ("overlay=$($r.X),$($r.Y),$($r.W),$($r.H) host=$($client.Left),$($client.Top),$($client.Right-$client.Left),$($client.Bottom-$client.Top)")
    $dpiOverlay = [OverlayTestWin]::GetDpiForWindow([IntPtr][uint64]$s.hwnd)
    $dpiHost = [OverlayTestWin]::GetDpiForWindow($hostHwnd)
    Add-Check 'DPI context matches host' ($dpiOverlay -gt 0 -and $dpiOverlay -eq $dpiHost) "overlay=$dpiOverlay host=$dpiHost"

    Add-Type -AssemblyName System.Drawing
    $bmp = [System.Drawing.Bitmap]::new([int]$r.W, [int]$r.H)
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.CopyFromScreen([int]$r.X, [int]$r.Y, 0, 0, [System.Drawing.Size]::new([int]$r.W, [int]$r.H))
    $cardX = [int](($r.W - 420) / 2)
    $cardY = [int](($r.H - 520) / 2)
    $body = $bmp.GetPixel($cardX + 30, $cardY + 300)
    $mask = $bmp.GetPixel(10, 10)
    $edge = $bmp.GetPixel($cardX - 3, $cardY + 250)
    $g.Dispose(); $bmp.Dispose()
    Add-Check 'card renders opaque body' ([Math]::Abs($body.R - 0xE3) -le 4 -and [Math]::Abs($body.G - 0xE3) -le 4 -and [Math]::Abs($body.B - 0xE3) -le 4) ("pixel=({0},{1},{2})" -f $body.R, $body.G, $body.B)
    # The mask is RGBA(0,0,0,0.65): over the host's 0x00101820 client fill it
    # must be roughly 35% of the background captured before the overlay existed.
    $expR = [int][Math]::Round($bg.R * 0.35); $expG = [int][Math]::Round($bg.G * 0.35); $expB = [int][Math]::Round($bg.B * 0.35)
    $maskOk = $bg.R -ge 20 -and [Math]::Abs($mask.R - $expR) -le 4 -and [Math]::Abs($mask.G - $expG) -le 4 -and [Math]::Abs($mask.B - $expB) -le 4 -and $edge.R -lt 250
    Add-Check 'mask is translucent, not black or white edge' $maskOk ("bg=({0},{1},{2}) mask=({3},{4},{5}) expected=({6},{7},{8}) edge=({9},{10},{11})" -f $bg.R, $bg.G, $bg.B, $mask.R, $mask.G, $mask.B, $expR, $expG, $expB, $edge.R, $edge.G, $edge.B)

    # Click the phone input itself: this activates the overlay and puts the
    # caret in the field exactly as a user would.
    $cardLeft = [int]($r.X + ($r.W - 420) / 2)
    $cardTop = [int]($r.Y + ($r.H - 520) / 2)
    [OverlayTestWin]::SetForegroundWindow([IntPtr][uint64]$s.hwnd) | Out-Null
    Start-Sleep -Milliseconds 300
    [OverlayTestWin]::Click($cardLeft + 210, $cardTop + 184) | Out-Null
    $s = Wait-State $script:overlayPort { param($x) $x.focused -eq 'phone' } 4000
    $fg = [OverlayTestWin]::GetForegroundWindow()
    Add-Check 'overlay takes keyboard focus' ($fg -eq [IntPtr][uint64]$s.hwnd) ("foreground=0x{0:x} overlay=0x{1:x}" -f $fg.ToInt64(), $s.hwnd)

    $hostClicks = Host-ClickCount
    [OverlayTestWin]::Text('13800138000')
    Start-Sleep -Milliseconds 500
    $s = Invoke-Ctl $script:overlayPort 'state'
    Add-Check 'typing into phone' ($s.phone -eq '13800138000') "phone=$($s.phone)"
    Add-Check 'typing does not reach host' ((Host-ClickCount) -eq $hostClicks -and -not (Select-String -Path $script:hostLog -Pattern '"event":"key"' -Quiet)) 'no key events in host log'

    [OverlayTestWin]::Tap(0x08); [OverlayTestWin]::Tap(0x08); [OverlayTestWin]::Text('99')
    Start-Sleep -Milliseconds 400
    $s = Invoke-Ctl $script:overlayPort 'state'
    Add-Check 'backspace editing' ($s.phone -eq '13800138099') "phone=$($s.phone)"

    [OverlayTestWin]::Down(0x11); [OverlayTestWin]::Tap(0x41); [OverlayTestWin]::Up(0x11)
    Start-Sleep -Milliseconds 300
    $s = Invoke-Ctl $script:overlayPort 'state'
    Add-Check 'select all' ($s.sel_phone[0] -eq 0 -and $s.sel_phone[1] -eq 11) "selection=$($s.sel_phone -join ',')"

    Set-Clipboard -Value '13900139000'
    [OverlayTestWin]::Down(0x11); [OverlayTestWin]::Tap(0x56); [OverlayTestWin]::Up(0x11)
    Start-Sleep -Milliseconds 500
    $s = Invoke-Ctl $script:overlayPort 'state'
    Add-Check 'clipboard paste replaces selection' ($s.phone -eq '13900139000') "phone=$($s.phone)"

    [OverlayTestWin]::Tap(0x24); Start-Sleep -Milliseconds 150
    [OverlayTestWin]::Down(0x10); 1..3 | ForEach-Object { [OverlayTestWin]::Tap(0x27) }; [OverlayTestWin]::Up(0x10)
    Start-Sleep -Milliseconds 400
    $s = Invoke-Ctl $script:overlayPort 'state'
    Add-Check 'shift selection' ($s.sel_phone[0] -eq 0 -and $s.sel_phone[1] -eq 3) "selection=$($s.sel_phone -join ',')"

    Invoke-Ctl $script:overlayPort 'focus captcha' | Out-Null
    Start-Sleep -Milliseconds 200
    [OverlayTestWin]::Text('1234')
    Start-Sleep -Milliseconds 400
    $s = Invoke-Ctl $script:overlayPort 'state'
    Add-Check 'captcha input' ($s.captcha -eq '1234') "captcha=$($s.captcha)"

    Invoke-Ctl $script:hostPort 'move 200 180 1000 700' | Out-Null
    Start-Sleep -Milliseconds 900
    $s = Wait-State $script:overlayPort { param($x) [Math]::Abs($x.rect.W - 984) -le 1 } 5000
    $client = [OverlayTestWin]::ClientOnScreen($hostHwnd)
    Add-Check 'move and resize sync' ([Math]::Abs($s.rect.X - $client.Left) -le 1 -and [Math]::Abs($s.rect.Y - $client.Top) -le 1) "overlay=$($s.rect.X),$($s.rect.Y),$($s.rect.W),$($s.rect.H) client=$($client.Left),$($client.Top),$($client.Right-$client.Left),$($client.Bottom-$client.Top)"

    Invoke-Ctl $script:hostPort 'minimize' | Out-Null
    $s = Wait-State $script:overlayPort { param($x) -not $x.visible } 4000
    Add-Check 'hide when host minimized' (-not $s.visible) 'visible=false'
    Invoke-Ctl $script:hostPort 'restore' | Out-Null
    $s = Wait-State $script:overlayPort { param($x) $x.visible } 4000
    Add-Check 'show when host restored' $s.visible 'visible=true'

    $old = [uint64]$s.hwnd
    $oldHost = [uint64]$s.host_hwnd
    Invoke-Ctl $script:hostPort 'recreate' | Out-Null
    $s = Wait-State $script:overlayPort { param($x) $x.bound -and $x.visible -and [uint64]$x.hwnd -ne 0 -and [uint64]$x.host_hwnd -ne $oldHost } 8000
    Add-Check 'overlay recreated for new host HWND' ($s.recreates -ge 1 -and [OverlayTestWin]::GetWindow([IntPtr][uint64]$s.hwnd, 4) -eq [IntPtr][uint64]$s.host_hwnd) ("recreates=$($s.recreates) hwnd=0x{0:x} host=0x{1:x}" -f [uint64]$s.hwnd, [uint64]$s.host_hwnd)
    $hostHwnd = [IntPtr][uint64]$s.host_hwnd
    Add-Check 'form survives recreation' ($s.phone -eq '13900139000' -and $s.captcha -eq '1234') "phone=$($s.phone) captcha=$($s.captcha)"
    Add-Check 'selection survives recreation' ($s.sel_phone[0] -eq 0 -and $s.sel_phone[1] -eq 3) "selection=$($s.sel_phone -join ',')"
    Add-Check 'watcher received events' ($s.hooks -gt 0) "callbacks=$($s.hooks)"

    Stop-Overlay
    Add-Check 'overlay process exits cleanly' $script:lastOverlayExited 'control quit'
    $fg = [OverlayTestWin]::GetForegroundWindow()
    Add-Check 'focus returns to host after close' ($fg -eq $hostHwnd) ("foreground=0x{0:x} host=0x{1:x}" -f $fg.ToInt64(), $hostHwnd.ToInt64())

    Write-Host '== banner mode =='
    $hs = Invoke-Ctl $script:hostPort 'status'
    $b = Start-Overlay $hs.hwnd @('-banner', '测试账号，欢迎登录')
    $s = Invoke-Ctl $script:overlayPort 'state'
    Add-Check 'banner screen active' ($s.screen -eq 'banner') "screen=$($s.screen) banner=$($s.banner)"
    $want = 0x20 -bor 0x80 -bor 0x08000000
    Add-Check 'banner window styles' (($s.ex -band $want) -eq $want) ("ex=0x{0:x}" -f $s.ex)
    $s = Wait-State $script:overlayPort { param($x) $x.opacity -ge 0.99 } 3000
    Add-Check 'banner fade-in' ($s.opacity -ge 0.99) "opacity=$($s.opacity)"

    $hostHwnd = [IntPtr][uint64]$hs.hwnd
    [OverlayTestWin]::SetForegroundWindow($hostHwnd) | Out-Null
    Start-Sleep -Milliseconds 500
    $client = [OverlayTestWin]::ClientOnScreen($hostHwnd)
    $before = Host-ClickCount
    $px = $client.Left + [int](($client.Right - $client.Left) / 2)
    $py = $client.Top + [int](($client.Bottom - $client.Top) / 2)
    [OverlayTestWin]::Click($px, $py) | Out-Null
    Start-Sleep -Milliseconds 350
    $hit = [OverlayTestWin]::HitTest($px, $py)
    $hitRoot = [OverlayTestWin]::GetAncestor($hit, 2)
    Add-Check 'banner is click-through' ((Host-ClickCount) -gt $before) 'host received the click'
    Add-Check 'hit test reaches host' ($hitRoot -eq $hostHwnd) ("WindowFromPoint=0x{0:x} root=0x{1:x} host=0x{2:x}" -f $hit.ToInt64(), $hitRoot.ToInt64(), $hostHwnd.ToInt64())
    Add-Check 'banner does not steal focus' ([OverlayTestWin]::GetForegroundWindow() -eq $hostHwnd) 'foreground is host'

    $script:overlayProc.WaitForExit(6000) | Out-Null
    Add-Check 'banner exits by itself' ($script:overlayProc.HasExited) 'process gone after fade-out'
    $script:overlayProc = $null

    Stop-Host
    Add-Check 'host process exits cleanly' $script:lastHostExited 'control quit'
    Add-Check 'no leftover PoC processes' (-not (Get-Process overlay-poc, overlay-host -ErrorAction SilentlyContinue)) 'process table clean'
} finally {
    if (-not $KeepRunning) {
        Stop-Overlay
        Stop-Host
    }
    $passed = @($script:checks | Where-Object { $_.pass }).Count
    $total = $script:checks.Count
    $report = [pscustomobject]@{
        when = (Get-Date).ToString('o')
        passed = $passed
        total = $total
        checks = $script:checks
    }
    $report | ConvertTo-Json -Depth 4 | Set-Content (Join-Path $out 'report.json')
    Write-Host ("`n{0}/{1} checks passed; report: {2}" -f $passed, $total, (Join-Path $out 'report.json'))
    if ($passed -ne $total) { exit 1 }
}





