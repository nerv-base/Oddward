# SPDX-License-Identifier: GPL-3.0-or-later
# Production A/B benchmark and 100-cycle stability verification comparing the
# legacy WPF login helper (compat/loginui) against the pure Go + MyGo Native UI
# login helper (oddward.exe --cnlogin-ui) over the real length-prefixed JSON
# frame protocol and real Win32 HWNDs.
#
#   pwsh -File scripts/bench-loginui-migration.ps1 [-SkipBuild] [-StressCycles 50]
#
# Output: artifacts/bench/migration-bench-report.json (git-ignored).

param(
    [switch]$SkipBuild,
    [int]$Trials = 5,
    [int]$SpawnCycles = 50,
    [int]$RecreateCycles = 50,
    [int]$IdleSeconds = 15
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$out = Join-Path $root 'artifacts\bench'
$wpfDir = Join-Path $out 'wpf-helper'
New-Item -ItemType Directory -Force $out | Out-Null

if (-not ('LoginUiBenchHelper' -as [type])) {
Add-Type @'
using System;
using System.IO;
using System.Runtime.InteropServices;
using System.Text;
using System.Threading;

public static class LoginUiBenchHelper {
    [StructLayout(LayoutKind.Sequential)] public struct RECT { public int Left, Top, Right, Bottom; }
    [StructLayout(LayoutKind.Sequential)] public struct POINT { public int X, Y; }
    [StructLayout(LayoutKind.Sequential)] public struct BITMAPINFOHEADER {
        public uint biSize; public int biWidth; public int biHeight;
        public ushort biPlanes; public ushort biBitCount; public uint biCompression;
        public uint biSizeImage; public int biXPelsPerMeter; public int biYPelsPerMeter;
        public uint biClrUsed; public uint biClrImportant;
    }

    [DllImport("user32.dll")] public static extern bool IsWindow(IntPtr h);
    [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);
    [DllImport("user32.dll")] public static extern IntPtr GetParent(IntPtr h);
    [DllImport("user32.dll")] public static extern IntPtr GetWindow(IntPtr h, uint cmd);
    [DllImport("user32.dll")] public static extern IntPtr GetWindowLongPtrW(IntPtr h, int idx);
    [DllImport("user32.dll", SetLastError = true)] public static extern IntPtr SetWindowLongPtrW(IntPtr h, int idx, IntPtr val);
    [DllImport("user32.dll", SetLastError = true)] public static extern IntPtr SetParent(IntPtr child, IntPtr parent);
    [DllImport("user32.dll", SetLastError = true)] public static extern bool SetWindowPos(IntPtr h, IntPtr after, int x, int y, int cx, int cy, uint flags);
    [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
    [DllImport("user32.dll")] public static extern bool GetClientRect(IntPtr h, out RECT r);
    [DllImport("user32.dll")] public static extern bool ClientToScreen(IntPtr h, ref POINT p);
    [DllImport("user32.dll")] public static extern bool PrintWindow(IntPtr h, IntPtr hdc, uint flags);
    [DllImport("user32.dll")] public static extern IntPtr GetDC(IntPtr h);
    [DllImport("user32.dll")] public static extern int ReleaseDC(IntPtr h, IntPtr dc);
    [DllImport("gdi32.dll")] public static extern IntPtr CreateCompatibleDC(IntPtr dc);
    [DllImport("gdi32.dll")] public static extern bool DeleteDC(IntPtr dc);
    [DllImport("gdi32.dll")] public static extern IntPtr CreateDIBSection(IntPtr dc, ref BITMAPINFOHEADER bmi, uint usage, out IntPtr bits, IntPtr section, uint offset);
    [DllImport("gdi32.dll")] public static extern IntPtr SelectObject(IntPtr dc, IntPtr obj);
    [DllImport("gdi32.dll")] public static extern bool DeleteObject(IntPtr obj);
    [DllImport("user32.dll")] public static extern uint GetGuiResources(IntPtr hProcess, uint uiFlags);
    [DllImport("kernel32.dll")] public static extern bool GetProcessHandleCount(IntPtr hProcess, out uint count);
    [DllImport("dwmapi.dll")] public static extern int DwmGetWindowAttribute(IntPtr h, int attr, out int val, int size);

    const int GWL_STYLE = -16;
    const int GWL_EXSTYLE = -20;
    const int GWLP_HWNDPARENT = -8;
    const ulong WS_CHILD = 0x40000000UL;
    const ulong CHILD_CLEAR = 0x80000000UL | 0x00C00000UL | 0x00080000UL | 0x00040000UL | 0x00020000UL | 0x00010000UL;

    public static void WriteFrame(Stream s, string json) {
        byte[] payload = Encoding.UTF8.GetBytes(json);
        byte[] hdr = new byte[4];
        hdr[0] = (byte)(payload.Length & 0xff);
        hdr[1] = (byte)((payload.Length >> 8) & 0xff);
        hdr[2] = (byte)((payload.Length >> 16) & 0xff);
        hdr[3] = (byte)((payload.Length >> 24) & 0xff);
        s.Write(hdr, 0, 4);
        s.Write(payload, 0, payload.Length);
        s.Flush();
    }

    public static string ReadFrame(Stream s, int timeoutMs) {
        var task = System.Threading.Tasks.Task.Run(() => {
            byte[] hdr = new byte[4];
            int got = 0;
            while (got < 4) {
                int n = s.Read(hdr, got, 4 - got);
                if (n <= 0) return (string)null;
                got += n;
            }
            int len = hdr[0] | (hdr[1] << 8) | (hdr[2] << 16) | (hdr[3] << 24);
            if (len <= 0 || len > 65536) throw new InvalidDataException("bad frame len " + len);
            byte[] buf = new byte[len];
            got = 0;
            while (got < len) {
                int n = s.Read(buf, got, len - got);
                if (n <= 0) return (string)null;
                got += n;
            }
            return Encoding.UTF8.GetString(buf);
        });
        if (!task.Wait(timeoutMs)) throw new TimeoutException("ReadFrame timeout");
        return task.Result;
    }

    public static string BindChildDiag(IntPtr child, IntPtr host) {
        RECT cr;
        if (!GetClientRect(host, out cr)) return "GetClientRect failed err=" + Marshal.GetLastWin32Error() + " hostIsWin=" + IsWindow(host);
        int w = cr.Right - cr.Left;
        int h = cr.Bottom - cr.Top;
        ulong style = (ulong)GetWindowLongPtrW(child, GWL_STYLE).ToInt64();
        ulong next = (style & ~CHILD_CLEAR) | WS_CHILD;
        SetWindowLongPtrW(child, GWL_STYLE, new IntPtr(unchecked((long)next)));
        int eStyle = Marshal.GetLastWin32Error();
        IntPtr prev = SetParent(child, host);
        int eParent = Marshal.GetLastWin32Error();
        bool spOk = SetWindowPos(child, IntPtr.Zero, 0, 0, w, h, 0x0004 | 0x0010 | 0x0200 | 0x0020);
        int ePos = Marshal.GetLastWin32Error();
        IntPtr got = GetParent(child);
        if (got == host) return "";
        return string.Format("child=0x{0:x}(win={1}) host=0x{2:x}(win={3}) style0=0x{4:x} eStyle={5} prev=0x{6:x} eParent={7} spOk={8} ePos={9} gotParent=0x{10:x}",
            child.ToInt64(), IsWindow(child), host.ToInt64(), IsWindow(host), style, eStyle, prev.ToInt64(), eParent, spOk, ePos, got.ToInt64());
    }

    public static bool BindChild(IntPtr child, IntPtr host) {
        return BindChildDiag(child, host) == "";
    }

    public static bool BindOwner(IntPtr overlay, IntPtr host) {
        SetWindowLongPtrW(overlay, GWLP_HWNDPARENT, host);
        return GetWindow(overlay, 4) == host;
    }

    public static bool HasCardBodyPixel(IntPtr hwnd) {
        RECT r;
        if (!GetWindowRect(hwnd, out r)) return false;
        int w = r.Right - r.Left;
        int h = r.Bottom - r.Top;
        if (w < 100 || h < 100) return false;
        IntPtr screenDC = GetDC(IntPtr.Zero);
        if (screenDC == IntPtr.Zero) return false;
        try {
            IntPtr memDC = CreateCompatibleDC(screenDC);
            if (memDC == IntPtr.Zero) return false;
            try {
                var bmi = new BITMAPINFOHEADER {
                    biSize = (uint)Marshal.SizeOf<BITMAPINFOHEADER>(),
                    biWidth = w,
                    biHeight = -h,
                    biPlanes = 1,
                    biBitCount = 32,
                    biCompression = 0
                };
                IntPtr bits;
                IntPtr dib = CreateDIBSection(memDC, ref bmi, 0, out bits, IntPtr.Zero, 0);
                if (dib == IntPtr.Zero || bits == IntPtr.Zero) return false;
                try {
                    IntPtr old = SelectObject(memDC, dib);
                    try {
                        if (!PrintWindow(hwnd, memDC, 2)) return false;
                    } finally {
                        SelectObject(memDC, old);
                    }
                    double scale = Math.Min(1.0, Math.Min((w * 0.90) / 420.0, (h * 0.80) / 520.0));
                    int cx = w / 2;
                    int cardTop = (int)(h / 2.0 - (520.0 * scale) / 2.0);
                    int y = cardTop + (int)(100.0 * scale);
                    if (y < 0 || y >= h) return false;
                    int off = (y * w + cx) * 4;
                    byte b = Marshal.ReadByte(bits, off + 0);
                    byte g = Marshal.ReadByte(bits, off + 1);
                    byte red = Marshal.ReadByte(bits, off + 2);
                    return Math.Abs(red - 0xE3) <= 10 && Math.Abs(g - 0xE3) <= 10 && Math.Abs(b - 0xE3) <= 10;
                } finally {
                    DeleteObject(dib);
                }
            } finally {
                DeleteDC(memDC);
            }
        } finally {
            ReleaseDC(IntPtr.Zero, screenDC);
        }
    }

    public static uint[] GetHandleCounts(IntPtr hProcess) {
        uint gdi = GetGuiResources(hProcess, 0);
        uint user = GetGuiResources(hProcess, 1);
        uint handles = 0;
        GetProcessHandleCount(hProcess, out handles);
        return new uint[] { gdi, user, handles };
    }
}
'@
}

function Sample-Proc($p) {
    $p.Refresh()
    $hc = [LoginUiBenchHelper]::GetHandleCounts($p.Handle)
    return @{
        private_bytes     = $p.PrivateMemorySize64
        private_mib       = [Math]::Round($p.PrivateMemorySize64 / 1MB, 2)
        working_set_bytes = $p.WorkingSet64
        working_set_mib   = [Math]::Round($p.WorkingSet64 / 1MB, 2)
        gdi_objects       = [uint32]$hc[0]
        user_objects      = [uint32]$hc[1]
        kernel_handles    = [uint32]$hc[2]
        cpu_ms            = [Math]::Round($p.TotalProcessorTime.TotalMilliseconds, 2)
        threads           = $p.Threads.Count
    }
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

$mygoExe = Join-Path $out 'oddward-mygo.exe'
$hostExe = Join-Path $out 'overlay-host.exe'
$wpfExe = Join-Path $wpfDir 'Oddward.LoginUi.exe'

if (-not $SkipBuild) {
    Write-Host '== Building test binaries =='
    & pwsh -NoProfile -File scripts/build-launcher.ps1 -Output $mygoExe
    if ($LASTEXITCODE -ne 0) { throw 'build oddward-mygo failed' }
    & pwsh -NoProfile -File scripts/go.ps1 build -trimpath -tags mygo_noinspector -ldflags '-H=windowsgui -s -w' -o $hostExe ./cmd/overlay-host
    if ($LASTEXITCODE -ne 0) { throw 'build overlay-host failed' }
    & pwsh -NoProfile -File compat/loginui/Build.ps1 -Output $wpfDir
    if ($LASTEXITCODE -ne 0) { throw 'build compat/loginui failed' }
}

function Start-HostProc {
    $statusFile = Join-Path $out 'bench-host.json'
    $logFile = Join-Path $out 'bench-host.jsonl'
    Remove-Item $statusFile, $logFile -ErrorAction SilentlyContinue
    $port = Get-FreePort
    $proc = Start-Process -FilePath $hostExe -ArgumentList @(
        '-x', '200', '-y', '160', '-w', '960', '-h', '680', '-mode', 'window',
        '-status', $statusFile, '-log', $logFile, '-control', "$port"
    ) -PassThru
    $deadline = [DateTime]::UtcNow.AddSeconds(8)
    while ([DateTime]::UtcNow -lt $deadline) {
        if (Test-Path $statusFile) {
            try {
                $j = Get-Content $statusFile -Raw | ConvertFrom-Json
                if ($j.hwnd -and $j.created) {
                    return @{ Proc = $proc; Port = $port; Info = $j }
                }
            } catch {}
        }
        Start-Sleep -Milliseconds 50
    }
    $proc.Kill()
    throw 'overlay-host failed to start'
}

function Stop-HostProc($h) {
    if ($h -and $h.Proc -and -not $h.Proc.HasExited) {
        try { Invoke-Ctl $h.Port 'quit' | Out-Null } catch {}
        if (-not $h.Proc.WaitForExit(3000)) { $h.Proc.Kill() }
    }
}

function Start-HelperProc([string]$exe, [string]$argsStr) {
    $psi = [System.Diagnostics.ProcessStartInfo]::new($exe, $argsStr)
    $psi.UseShellExecute = $false
    $psi.RedirectStandardInput = $true
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    $psi.CreateNoWindow = $true
    $p = [System.Diagnostics.Process]::Start($psi)
    return $p
}

function Run-OneTrial([string]$label, [string]$exe, [string]$argsStr, $hostObj, [bool]$measureIdle) {
    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    $p = Start-HelperProc $exe $argsStr
    try {
        $stdin = $p.StandardInput.BaseStream
        $stdout = $p.StandardOutput.BaseStream

        # 1. Wait for ready
        $readyRaw = [LoginUiBenchHelper]::ReadFrame($stdout, 10000)
        $readyMs = $sw.Elapsed.TotalMilliseconds

        # 2. Send phone model
        $modelJson = @{
            version = 1
            type = 'model'
            model = @{
                screen = 'phone'
                subtitle = '国服账号'
                remember = $true
                canOfficial = $true
                gameProcessId = [uint32]$hostObj.Info.pid
                gameCreationTime = [string]$hostObj.Info.created
            }
        } | ConvertTo-Json -Depth 5 -Compress
        [LoginUiBenchHelper]::WriteFrame($stdin, $modelJson)

        # 3. Wait for embed_overlay or bind_overlay
        $reqRaw = [LoginUiBenchHelper]::ReadFrame($stdout, 10000)
        $reqMs = $sw.Elapsed.TotalMilliseconds
        $req = $reqRaw | ConvertFrom-Json
        $ovHwnd = [IntPtr][uint64]$req.event.window
        $gmHwnd = [IntPtr][uint64]$req.event.gameWindow
        if ($req.event.kind -eq 'embed_overlay') {
            $ok = [LoginUiBenchHelper]::BindChild($ovHwnd, $gmHwnd)
        } else {
            $ok = [LoginUiBenchHelper]::BindOwner($ovHwnd, $gmHwnd)
        }
        if (-not $ok) { throw "bind failed for $label" }

        $ackJson = @{
            version = 1
            type = 'window_bound'
            event = $req.event
        } | ConvertTo-Json -Depth 5 -Compress
        [LoginUiBenchHelper]::WriteFrame($stdin, $ackJson)

        # 4. Wait for visible & non-blank rendered card pixel
        $visDeadline = [DateTime]::UtcNow.AddSeconds(8)
        while (-not [LoginUiBenchHelper]::IsWindowVisible($ovHwnd)) {
            if ([DateTime]::UtcNow -gt $visDeadline) { throw "$label did not become visible" }
            Start-Sleep -Milliseconds 10
        }
        $visibleMs = $sw.Elapsed.TotalMilliseconds

        $pixDeadline = [DateTime]::UtcNow.AddSeconds(8)
        $rendered = $false
        while ([DateTime]::UtcNow -lt $pixDeadline) {
            if ([LoginUiBenchHelper]::HasCardBodyPixel($ovHwnd)) {
                $rendered = $true
                break
            }
            Start-Sleep -Milliseconds 15
        }
        $firstFrameMs = $sw.Elapsed.TotalMilliseconds

        $sampleStart = Sample-Proc($p)
        $idleReport = $null
        if ($measureIdle) {
            Start-Sleep -Seconds 5
            $sampleEnd = Sample-Proc($p)
            $cpuDelta = [Math]::Round($sampleEnd['cpu_ms'] - $sampleStart['cpu_ms'], 2)
            $cpuPct = [Math]::Round(($cpuDelta / (5000.0 * [Environment]::ProcessorCount)) * 100.0, 4)
            $idleReport = [pscustomobject]@{
                window_seconds = 5
                cpu_delta_ms = $cpuDelta
                cpu_percent = $cpuPct
                sample_after_5s = $sampleEnd
            }
        }

        $closeJson = '{"version":1,"type":"close"}'
        [LoginUiBenchHelper]::WriteFrame($stdin, $closeJson)
        $stdin.Close()
        if (-not $p.WaitForExit(5000)) { $p.Kill(); throw "$label failed to exit cleanly" }

        return [pscustomobject]@{
            ready_ms = [Math]::Round($readyMs, 2)
            bind_req_ms = [Math]::Round($reqMs, 2)
            visible_ms = [Math]::Round($visibleMs, 2)
            first_frame_ms = [Math]::Round($firstFrameMs, 2)
            rendered_card = $rendered
            bind_kind = $req.event.kind
            sample = $sampleStart
            idle = $idleReport
            exit_code = $p.ExitCode
        }
    } finally {
        if (-not $p.HasExited) { $p.Kill() }
        $p.Dispose()
    }
}

function Run-BannerTrial([string]$label, [string]$exe, [string]$argsStr, $hostObj) {
    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    $p = Start-HelperProc $exe $argsStr
    try {
        $stdin = $p.StandardInput.BaseStream
        $stdout = $p.StandardOutput.BaseStream
        $null = [LoginUiBenchHelper]::ReadFrame($stdout, 10000)
        $readyMs = $sw.Elapsed.TotalMilliseconds
        $modelJson = @{
            version = 1
            type = 'model'
            model = @{
                screen = 'welcome'
                banner = '夜航星，欢迎登录'
                gameProcessId = [uint32]$hostObj.Info.pid
                gameCreationTime = [string]$hostObj.Info.created
            }
        } | ConvertTo-Json -Depth 5 -Compress
        [LoginUiBenchHelper]::WriteFrame($stdin, $modelJson)
        $reqRaw = [LoginUiBenchHelper]::ReadFrame($stdout, 10000)
        $req = $reqRaw | ConvertFrom-Json
        $ovHwnd = [IntPtr][uint64]$req.event.window
        $gmHwnd = [IntPtr][uint64]$req.event.gameWindow
        if ($req.event.kind -eq 'embed_overlay') {
            $ok = [LoginUiBenchHelper]::BindChild($ovHwnd, $gmHwnd)
        } else {
            $ok = [LoginUiBenchHelper]::BindOwner($ovHwnd, $gmHwnd)
        }
        if (-not $ok) { throw "banner bind failed for $label" }
        $ackJson = @{ version = 1; type = 'window_bound'; event = $req.event } | ConvertTo-Json -Depth 5 -Compress
        [LoginUiBenchHelper]::WriteFrame($stdin, $ackJson)
        $visDeadline = [DateTime]::UtcNow.AddSeconds(6)
        while (-not [LoginUiBenchHelper]::IsWindowVisible($ovHwnd)) {
            if ([DateTime]::UtcNow -gt $visDeadline) { throw "$label banner never visible" }
            Start-Sleep -Milliseconds 10
        }
        $visibleMs = $sw.Elapsed.TotalMilliseconds
        $sample = Sample-Proc($p)
        $ex = [LoginUiBenchHelper]::GetWindowLongPtrW($ovHwnd, -20).ToInt64()
        $exOk = (($ex -band (0x20 -bor 0x80 -bor 0x08000000)) -eq (0x20 -bor 0x80 -bor 0x08000000)) -and (($ex -band 0x08) -eq 0)
        $selfExited = $p.WaitForExit(8000)
        $totalMs = $sw.Elapsed.TotalMilliseconds
        return [pscustomobject]@{
            ready_ms = [Math]::Round($readyMs, 2)
            visible_ms = [Math]::Round($visibleMs, 2)
            total_ms = [Math]::Round($totalMs, 2)
            bind_kind = $req.event.kind
            ex_style = ('0x{0:x}' -f $ex)
            ex_style_ok = $exOk
            self_exited = $selfExited
            exit_code = if ($selfExited) { $p.ExitCode } else { -1 }
            sample = $sample
        }
    } finally {
        if (-not $p.HasExited) { $p.Kill() }
        $p.Dispose()
    }
}

$hostObj = Start-HostProc
try {
    Write-Host "== Running $Trials A/B trials: WPF vs MyGo --cnlogin-ui =="
    $wpfTrials = @()
    $mygoTrials = @()
    for ($i = 1; $i -le $Trials; $i++) {
        $isLast = ($i -eq $Trials)
        $rW = Run-OneTrial "WPF#$i" $wpfExe '' $hostObj $isLast
        $wpfTrials += $rW
        Write-Host ("  WPF  #{0}: ready={1}ms first_frame={2}ms priv={3}MiB ws={4}MiB gdi={5} user={6} handles={7}" -f `
            $i, $rW.ready_ms, $rW.first_frame_ms, $rW.sample['private_mib'], $rW.sample['working_set_mib'], `
            $rW.sample['gdi_objects'], $rW.sample['user_objects'], $rW.sample['kernel_handles'])
        $rM = Run-OneTrial "MyGo#$i" $mygoExe '--cnlogin-ui' $hostObj $isLast
        $mygoTrials += $rM
        Write-Host ("  MyGo #{0}: ready={1}ms first_frame={2}ms priv={3}MiB ws={4}MiB gdi={5} user={6} handles={7}" -f `
            $i, $rM.ready_ms, $rM.first_frame_ms, $rM.sample['private_mib'], $rM.sample['working_set_mib'], `
            $rM.sample['gdi_objects'], $rM.sample['user_objects'], $rM.sample['kernel_handles'])
    }

    Write-Host '== Running Welcome Banner A/B =='
    $bannerWpf = Run-BannerTrial 'WPF-Banner' $wpfExe '' $hostObj
    $bannerMygo = Run-BannerTrial 'MyGo-Banner' $mygoExe '--cnlogin-ui' $hostObj
    Write-Host ("  WPF  Banner: visible={0}ms total={1}ms exOk={2} selfExit={3} priv={4}MiB" -f $bannerWpf.visible_ms, $bannerWpf.total_ms, $bannerWpf.ex_style_ok, $bannerWpf.self_exited, $bannerWpf.sample['private_mib'])
    Write-Host ("  MyGo Banner: visible={0}ms total={1}ms exOk={2} selfExit={3} priv={4}MiB" -f $bannerMygo.visible_ms, $bannerMygo.total_ms, $bannerMygo.ex_style_ok, $bannerMygo.self_exited, $bannerMygo.sample['private_mib'])

    Write-Host "== Stress Part A: $SpawnCycles fresh process spawn/bind/render/close cycles =="
    $spawnPass = 0
    $spawnPids = New-Object System.Collections.Generic.List[int]
    $hostSampleBefore = Sample-Proc($hostObj.Proc)
    for ($i = 1; $i -le $SpawnCycles; $i++) {
        $r = Run-OneTrial "Spawn#$i" $mygoExe '--cnlogin-ui' $hostObj $false
        if ($r.rendered_card -and $r.exit_code -eq 0) { $spawnPass++ }
        if ($i % 10 -eq 0) { Write-Host ("  spawn cycle {0}/{1} passed={2}" -f $i, $SpawnCycles, $spawnPass) }
    }
    $hostSampleAfter = Sample-Proc($hostObj.Proc)
    $leftover = @(Get-Process -Name 'oddward-mygo' -ErrorAction SilentlyContinue).Count

    Write-Host "== Stress Part B: $RecreateCycles continuous HWND recreate cycles + ${IdleSeconds}s idle observation =="
    $p = Start-HelperProc $mygoExe '--cnlogin-ui'
    $recreatePass = 0
    $samples = @()
    try {
        $stdin = $p.StandardInput.BaseStream
        $stdout = $p.StandardOutput.BaseStream
        $null = [LoginUiBenchHelper]::ReadFrame($stdout, 10000)
        $modelJson = @{
            version = 1
            type = 'model'
            model = @{
                screen = 'phone'
                subtitle = '国服账号'
                remember = $true
                canOfficial = $true
                gameProcessId = [uint32]$hostObj.Info.pid
                gameCreationTime = [string]$hostObj.Info.created
            }
        } | ConvertTo-Json -Depth 5 -Compress
        [LoginUiBenchHelper]::WriteFrame($stdin, $modelJson)
        $reqRaw = [LoginUiBenchHelper]::ReadFrame($stdout, 10000)
        $req = $reqRaw | ConvertFrom-Json
        $ov = [IntPtr][uint64]$req.event.window
        $gm = [IntPtr][uint64]$req.event.gameWindow
        if (-not [LoginUiBenchHelper]::BindChild($ov, $gm)) { throw 'initial stress child bind failed' }
        [LoginUiBenchHelper]::WriteFrame($stdin, (@{ version = 1; type = 'window_bound'; event = $req.event } | ConvertTo-Json -Depth 5 -Compress))
        Start-Sleep -Milliseconds 300
        $samples += [pscustomobject]@{ stage = 'start'; metrics = Sample-Proc($p) }

        for ($i = 1; $i -le $RecreateCycles; $i++) {
            $null = Invoke-Ctl $hostObj.Port 'recreate'
            $nextRaw = [LoginUiBenchHelper]::ReadFrame($stdout, 10000)
            $nextReq = $nextRaw | ConvertFrom-Json
            if ($nextReq.event.kind -ne 'embed_overlay') { throw "unexpected event at recreate #$i : $nextRaw" }
            $ov = [IntPtr][uint64]$nextReq.event.window
            $gm = [IntPtr][uint64]$nextReq.event.gameWindow
            $diag = [LoginUiBenchHelper]::BindChildDiag($ov, $gm)
            if ($diag -ne '') { throw "recreate #$i bind failed: $diag (req=$nextRaw)" }
            [LoginUiBenchHelper]::WriteFrame($stdin, (@{ version = 1; type = 'window_bound'; event = $nextReq.event } | ConvertTo-Json -Depth 5 -Compress))
            $recreatePass++
            if ($i -eq [int]($RecreateCycles / 2)) {
                $samples += [pscustomobject]@{ stage = "mid_$i"; metrics = Sample-Proc($p) }
            }
            if ($i % 10 -eq 0) {
                $cur = Sample-Proc($p)
                Write-Host ("  recreate cycle {0}/{1}: priv={2}MiB gdi={3} user={4} handles={5}" -f `
                    $i, $RecreateCycles, $cur['private_mib'], $cur['gdi_objects'], $cur['user_objects'], $cur['kernel_handles'])
            }
        }
        Start-Sleep -Milliseconds 300
        $samples += [pscustomobject]@{ stage = "after_$RecreateCycles"; metrics = Sample-Proc($p) }

        # Extended steady-state idle measurement
        $idleStart = Sample-Proc($p)
        Start-Sleep -Seconds $IdleSeconds
        $idleEnd = Sample-Proc($p)
        $idleCpuMs = [Math]::Round($idleEnd['cpu_ms'] - $idleStart['cpu_ms'], 2)
        $idleCpuPct = [Math]::Round(($idleCpuMs / ($IdleSeconds * 1000.0 * [Environment]::ProcessorCount)) * 100.0, 4)
        $samples += [pscustomobject]@{
            stage = "after_idle_${IdleSeconds}s"
            idle_cpu_delta_ms = $idleCpuMs
            idle_cpu_percent = $idleCpuPct
            metrics = $idleEnd
        }

        [LoginUiBenchHelper]::WriteFrame($stdin, '{"version":1,"type":"close"}')
        $stdin.Close()
        if (-not $p.WaitForExit(5000)) { $p.Kill(); throw 'stress helper failed to exit cleanly' }
    } finally {
        if (-not $p.HasExited) { $p.Kill() }
        $p.Dispose()
    }

    $leftoverAfterAll = @(Get-Process -Name 'oddward-mygo' -ErrorAction SilentlyContinue).Count

    $report = [pscustomobject]@{
        when = (Get-Date).ToString('o')
        cpu_count = [Environment]::ProcessorCount
        wpf_trials = $wpfTrials
        mygo_trials = $mygoTrials
        banner_wpf = $bannerWpf
        banner_mygo = $bannerMygo
        stress = [pscustomobject]@{
            spawn_cycles_total = $SpawnCycles
            spawn_cycles_passed = $spawnPass
            leftover_after_spawn = $leftover
            host_before = $hostSampleBefore
            host_after = $hostSampleAfter
            recreate_cycles_total = $RecreateCycles
            recreate_cycles_passed = $recreatePass
            total_cycles = $SpawnCycles + $RecreateCycles
            recreate_samples = $samples
            leftover_after_all = $leftoverAfterAll
        }
    }
    $reportPath = Join-Path $out 'migration-bench-report.json'
    $report | ConvertTo-Json -Depth 8 | Set-Content -Encoding UTF8 $reportPath
    Write-Host "`nBenchmark & stress report written to $reportPath"
    Write-Host ("Total cycles: spawn={0}/{1}, recreate={2}/{3}, leftover={4}" -f $spawnPass, $SpawnCycles, $recreatePass, $RecreateCycles, $leftoverAfterAll)
    if ($spawnPass -ne $SpawnCycles -or $recreatePass -ne $RecreateCycles -or $leftoverAfterAll -ne 0) {
        exit 1
    }
} finally {
    Stop-HostProc $hostObj
}
