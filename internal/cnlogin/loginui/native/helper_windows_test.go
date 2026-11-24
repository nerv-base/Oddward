// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package native

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/nerv-base/Oddward/internal/cnlogin/loginui"
	"github.com/nerv-base/Oddward/internal/overlaywin"
	"github.com/nerv-base/Oddward/internal/process"
)

const (
	testEnvRole    = "ODDWARD_NATIVE_LOGINUI_TEST_ROLE"
	testEnvHostDPI = "ODDWARD_NATIVE_LOGINUI_TEST_DPI"
)

func TestMain(m *testing.M) {
	switch os.Getenv(testEnvRole) {
	case "run":
		handled, code := Run(os.Args[1:])
		if !handled {
			os.Exit(99)
		}
		os.Exit(code)
	case "host":
		os.Exit(runTestHostProcess())
	}
	os.Exit(m.Run())
}

// --- Real Win32 Host Subprocess (stand-in game window for HWND tests) ---

var (
	tUser32   = windows.NewLazySystemDLL("user32.dll")
	tGdi32    = windows.NewLazySystemDLL("gdi32.dll")
	tKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	tRegisterClassExW = tUser32.NewProc("RegisterClassExW")
	tCreateWindowExW  = tUser32.NewProc("CreateWindowExW")
	tDestroyWindow    = tUser32.NewProc("DestroyWindow")
	tDefWindowProcW   = tUser32.NewProc("DefWindowProcW")
	tShowWindow       = tUser32.NewProc("ShowWindow")
	tUpdateWindow     = tUser32.NewProc("UpdateWindow")
	tGetMessageW      = tUser32.NewProc("GetMessageW")
	tTranslateMessage = tUser32.NewProc("TranslateMessage")
	tDispatchMessageW = tUser32.NewProc("DispatchMessageW")
	tPostMessageW     = tUser32.NewProc("PostMessageW")
	tPostQuitMessage  = tUser32.NewProc("PostQuitMessage")
	tSetWindowPos     = tUser32.NewProc("SetWindowPos")
	tPrintWindow      = tUser32.NewProc("PrintWindow")
	tGetDC            = tUser32.NewProc("GetDC")
	tReleaseDC        = tUser32.NewProc("ReleaseDC")
	tGetWindowRect    = tUser32.NewProc("GetWindowRect")
	tSetThreadDpiCtx  = tUser32.NewProc("SetThreadDpiAwarenessContext")
	tGetModuleHandleW = tKernel32.NewProc("GetModuleHandleW")
	tCreateSolidBrush = tGdi32.NewProc("CreateSolidBrush")
	tCreateCompatDC   = tGdi32.NewProc("CreateCompatibleDC")
	tDeleteDC         = tGdi32.NewProc("DeleteDC")
	tCreateDIBSection = tGdi32.NewProc("CreateDIBSection")
	tSelectObject     = tGdi32.NewProc("SelectObject")
	tDeleteObject     = tGdi32.NewProc("DeleteObject")
)

const (
	wmUserCmd    = 0x0400 + 42
	swShowNoAct  = 4
	swMinimize   = 6
	swRestore    = 9
	swHide       = 0
	wsOverlapped = 0x00CF0000
	wsClipChild  = 0x02000000
)

type wndClassExW struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type winMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

type bitmapInfoHeader struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

type bitmapInfo struct {
	BmiHeader bitmapInfoHeader
	BmiColors [1]uint32
}

type hostCmd struct {
	op   string
	x, y int32
	w, h int32
	done chan hostReply
}

type hostReply struct {
	HWND uintptr `json:"hwnd"`
	OK   bool    `json:"ok"`
}

func runTestHostProcess() int {
	runtime.LockOSThread()
	if os.Getenv(testEnvHostDPI) == "system" && tSetThreadDpiCtx.Find() == nil {
		// DPI_AWARENESS_CONTEXT_SYSTEM_AWARE = -2
		dpiSys := ^uintptr(1)
		tSetThreadDpiCtx.Call(dpiSys)
	}
	inst, _, _ := tGetModuleHandleW.Call(0)
	cls, _ := windows.UTF16PtrFromString("OddwardNativeLoginTestHost")
	title, _ := windows.UTF16PtrFromString("Oddward Test Host")
	brush, _, _ := tCreateSolidBrush.Call(0x00201810) // BGR dark navy

	var (
		cmdMu   sync.Mutex
		pending []hostCmd
		curHWND uintptr
	)

	createHostWin := func(x, y, w, h int32) uintptr {
		hw, _, _ := tCreateWindowExW.Call(
			0,
			uintptr(unsafe.Pointer(cls)),
			uintptr(unsafe.Pointer(title)),
			wsOverlapped|wsClipChild,
			uintptr(x), uintptr(y), uintptr(w), uintptr(h),
			0, 0, inst, 0,
		)
		if hw != 0 {
			tShowWindow.Call(hw, swShowNoAct)
			tUpdateWindow.Call(hw)
		}
		return hw
	}

	wndProc := syscall.NewCallback(func(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
		if msg == wmUserCmd {
			cmdMu.Lock()
			batch := pending
			pending = nil
			cmdMu.Unlock()
			for _, cmd := range batch {
				switch cmd.op {
				case "move":
					tSetWindowPos.Call(curHWND, 0, uintptr(cmd.x), uintptr(cmd.y), uintptr(cmd.w), uintptr(cmd.h), 0x0014)
					cmd.done <- hostReply{HWND: curHWND, OK: true}
				case "minimize":
					tShowWindow.Call(curHWND, swMinimize)
					cmd.done <- hostReply{HWND: curHWND, OK: true}
				case "restore":
					tShowWindow.Call(curHWND, swRestore)
					cmd.done <- hostReply{HWND: curHWND, OK: true}
				case "recreate":
					old := curHWND
					curHWND = createHostWin(220, 180, 960, 680)
					if old != 0 {
						tDestroyWindow.Call(old)
					}
					cmd.done <- hostReply{HWND: curHWND, OK: curHWND != 0}
				case "switch":
					old := curHWND
					curHWND = createHostWin(240, 200, 920, 640)
					if old != 0 {
						tShowWindow.Call(old, swHide)
					}
					cmd.done <- hostReply{HWND: curHWND, OK: curHWND != 0}
				case "quit":
					if curHWND != 0 {
						tDestroyWindow.Call(curHWND)
						curHWND = 0
					}
					tPostQuitMessage.Call(0)
					cmd.done <- hostReply{OK: true}
				}
			}
			return 0
		}
		r, _, _ := tDefWindowProcW.Call(hwnd, uintptr(msg), wp, lp)
		return r
	})

	wc := wndClassExW{
		Size:       uint32(unsafe.Sizeof(wndClassExW{})),
		WndProc:    wndProc,
		Instance:   inst,
		Background: brush,
		ClassName:  cls,
	}
	if r, _, _ := tRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return 2
	}
	curHWND = createHostWin(160, 140, 900, 640)
	if curHWND == 0 {
		return 3
	}

	hProc, id, err := overlaywin.OpenIdentity(uint32(os.Getpid()))
	if err != nil {
		return 4
	}
	_ = overlaywin.CloseIdentity(hProc)

	// Publish initial host descriptor on stdout.
	fmt.Fprintf(os.Stdout, `{"pid":%d,"created":"%d","hwnd":%d}`+"\n", id.PID, id.CreationTime, uint64(curHWND))

	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) == 0 {
				continue
			}
			c := hostCmd{op: fields[0], done: make(chan hostReply, 1)}
			if c.op == "move" && len(fields) == 5 {
				x, _ := strconv.Atoi(fields[1])
				y, _ := strconv.Atoi(fields[2])
				w, _ := strconv.Atoi(fields[3])
				h, _ := strconv.Atoi(fields[4])
				c.x, c.y, c.w, c.h = int32(x), int32(y), int32(w), int32(h)
			}
			cmdMu.Lock()
			pending = append(pending, c)
			target := curHWND
			cmdMu.Unlock()
			if target == 0 {
				return
			}
			tPostMessageW.Call(target, wmUserCmd, 0, 0)
			rep := <-c.done
			b, _ := json.Marshal(rep)
			fmt.Fprintln(os.Stdout, string(b))
			if c.op == "quit" {
				return
			}
		}
		cmdMu.Lock()
		target := curHWND
		pending = append(pending, hostCmd{op: "quit", done: make(chan hostReply, 1)})
		cmdMu.Unlock()
		if target != 0 {
			tPostMessageW.Call(target, wmUserCmd, 0, 0)
		}
	}()

	var m winMsg
	for {
		ret, _, _ := tGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		tTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		tDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	return 0
}

type testHost struct {
	cmd     *exec.Cmd
	stdin   *os.File
	scanner *bufio.Scanner
	pid     uint32
	created string
	hwnd    uintptr
}

func startTestHost(t *testing.T, dpi string) *testHost {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), testEnvRole+"=host", testEnvHostDPI+"="+dpi)
	cmd.Stdin = inR
	cmd.Stdout = outW
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		t.Fatal(err)
	}
	inR.Close()
	outW.Close()

	sc := bufio.NewScanner(outR)
	if !sc.Scan() {
		inW.Close()
		outR.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("test host failed to publish initial info: %v", sc.Err())
	}
	var info struct {
		PID     uint32 `json:"pid"`
		Created string `json:"created"`
		HWND    uint64 `json:"hwnd"`
	}
	if err := json.Unmarshal(sc.Bytes(), &info); err != nil {
		inW.Close()
		outR.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("invalid host info: %v (%s)", err, sc.Text())
	}
	th := &testHost{
		cmd:     cmd,
		stdin:   inW,
		scanner: sc,
		pid:     info.PID,
		created: info.Created,
		hwnd:    uintptr(info.HWND),
	}
	t.Cleanup(func() {
		th.Close()
		outR.Close()
	})
	return th
}

func (h *testHost) Do(t *testing.T, line string) uintptr {
	t.Helper()
	if _, err := fmt.Fprintln(h.stdin, line); err != nil {
		t.Fatalf("host write %q: %v", line, err)
	}
	if !h.scanner.Scan() {
		t.Fatalf("host closed before reply to %q: %v", line, h.scanner.Err())
	}
	var rep hostReply
	if err := json.Unmarshal(h.scanner.Bytes(), &rep); err != nil {
		t.Fatalf("host bad reply to %q: %v", line, err)
	}
	if !rep.OK {
		t.Fatalf("host rejected %q", line)
	}
	if rep.HWND != 0 {
		h.hwnd = rep.HWND
	}
	return h.hwnd
}

func (h *testHost) Close() {
	if h.cmd == nil || h.cmd.Process == nil {
		return
	}
	if h.stdin != nil {
		_, _ = fmt.Fprintln(h.stdin, "quit")
		_ = h.stdin.Close()
		h.stdin = nil
	}
	done := make(chan error, 1)
	go func() { done <- h.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = h.cmd.Process.Kill()
		<-done
	}
	h.cmd = nil
}

func captureWindowRGBA(hwnd uintptr) (*image.RGBA, error) {
	var r struct{ Left, Top, Right, Bottom int32 }
	ok, _, _ := tGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	if ok == 0 {
		return nil, fmt.Errorf("GetWindowRect failed for %#x", hwnd)
	}
	w := int(r.Right - r.Left)
	h := int(r.Bottom - r.Top)
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("empty window rect %dx%d", w, h)
	}
	screenDC, _, _ := tGetDC.Call(0)
	if screenDC == 0 {
		return nil, fmt.Errorf("GetDC failed")
	}
	defer tReleaseDC.Call(0, screenDC)

	memDC, _, _ := tCreateCompatDC.Call(screenDC)
	if memDC == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC failed")
	}
	defer tDeleteDC.Call(memDC)

	bi := bitmapInfo{
		BmiHeader: bitmapInfoHeader{
			BiSize:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
			BiWidth:       int32(w),
			BiHeight:      -int32(h), // top-down
			BiPlanes:      1,
			BiBitCount:    32,
			BiCompression: 0, // BI_RGB
		},
	}
	var bits unsafe.Pointer
	dib, _, _ := tCreateDIBSection.Call(memDC, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if dib == 0 || bits == nil {
		return nil, fmt.Errorf("CreateDIBSection failed")
	}
	defer tDeleteObject.Call(dib)

	old, _, _ := tSelectObject.Call(memDC, dib)
	defer tSelectObject.Call(memDC, old)

	// PW_RENDERFULLCONTENT = 2 (works reliably even when desktop is locked).
	pw, _, _ := tPrintWindow.Call(hwnd, memDC, 2)
	if pw == 0 {
		return nil, fmt.Errorf("PrintWindow failed")
	}

	raw := unsafe.Slice((*byte)(bits), w*h*4)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		img.Pix[i*4+0] = raw[i*4+2] // R
		img.Pix[i*4+1] = raw[i*4+1] // G
		img.Pix[i*4+2] = raw[i*4+0] // B
		img.Pix[i*4+3] = 255
	}
	return img, nil
}

func waitUntil(t *testing.T, timeout time.Duration, desc string, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", desc)
}

func nearColor(got, want color.RGBA, tol int) bool {
	diff := func(a, b uint8) int {
		d := int(a) - int(b)
		if d < 0 {
			return -d
		}
		return d
	}
	return diff(got.R, want.R) <= tol && diff(got.G, want.G) <= tol && diff(got.B, want.B) <= tol
}

// --- Security & Protocol Hardening Tests ---

func TestRunSecurityHardening(t *testing.T) {
	// Non-role args return handled=false without touching stdin/stdout or UI.
	if handled, code := Run(nil); handled || code != 0 {
		t.Fatalf("nil args: handled=%v code=%d", handled, code)
	}
	if handled, code := Run([]string{"-demo"}); handled || code != 0 {
		t.Fatalf("unrelated args: handled=%v code=%d", handled, code)
	}
	// Extra/polluted arguments are rejected synchronously.
	if handled, code := Run([]string{loginui.Mode, "--extra"}); !handled || code != 1 {
		t.Fatalf("extra args must fail closed: handled=%v code=%d", handled, code)
	}

	// Non-pipe stdin/stdout (e.g. NUL device / interactive file) must fail closed with exit=1.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	devNullR, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devNullR.Close()
	devNullW, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devNullW.Close()

	cmd := exec.Command(exe, loginui.Mode)
	cmd.Env = append(os.Environ(), testEnvRole+"=run")
	cmd.Stdin = devNullR
	cmd.Stdout = devNullW
	cmd.Stderr = devNullW
	if err := cmd.Run(); err == nil {
		t.Fatal("expected non-pipe invocation to exit non-zero")
	} else if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
		t.Fatalf("expected exit code 1 for non-pipe invocation, got %v", err)
	}
}

func TestNativeHelperRejectsStaleGameIdentityAndMalformedFrames(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	host := startTestHost(t, "")
	createdVal, err := overlaywin.ParseCreationTime(host.created)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Stale GameCreationTime must fail closed without ever requesting binding.
	t.Run("stale-creation-time", func(t *testing.T) {
		t.Setenv(testEnvRole, "run")
		var bindCalled bool
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		sess, err := loginui.Start(ctx, loginui.Config{
			Executable: exe,
			Args:       []string{loginui.Mode},
			EmbedWindow: func(context.Context, process.Identity, uintptr, uintptr) error {
				bindCalled = true
				return nil
			},
			BindWindow: func(context.Context, process.Identity, uintptr, uintptr) error {
				bindCalled = true
				return nil
			},
		}, loginui.Model{
			Screen:           "phone",
			GameProcessID:    host.pid,
			GameCreationTime: strconv.FormatUint(createdVal+1, 10),
		})
		if err != nil {
			t.Fatal(err)
		}
		defer sess.Close()
		select {
		case <-sess.Done():
		case <-time.After(6 * time.Second):
			t.Fatal("helper did not exit on stale GameCreationTime")
		}
		if bindCalled {
			t.Fatal("helper requested window binding despite stale GameCreationTime")
		}
		if sess.Err() == nil {
			t.Fatal("expected non-nil helper error on stale GameCreationTime")
		}
	})

	// 2. Malformed frame with sensitive text exits non-zero and leaks nothing.
	t.Run("malformed-frame-no-secret-leak", func(t *testing.T) {
		inR, inW, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		outR, outW, err := os.Pipe()
		if err != nil {
			inR.Close()
			inW.Close()
			t.Fatal(err)
		}
		errR, errW, err := os.Pipe()
		if err != nil {
			inR.Close()
			inW.Close()
			outR.Close()
			outW.Close()
			t.Fatal(err)
		}
		cmd := exec.Command(exe, loginui.Mode)
		cmd.Env = append(os.Environ(), testEnvRole+"=run")
		cmd.Stdin = inR
		cmd.Stdout = outW
		cmd.Stderr = errW
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		inR.Close()
		outW.Close()
		errW.Close()
		defer outR.Close()
		defer errR.Close()

		// Read initial ready frame.
		env, err := loginui.ReadFrame(outR)
		if err != nil || env.Type != "event" || env.Event == nil || env.Event.Kind != "ready" {
			t.Fatalf("expected ready frame, got %+v err=%v", env, err)
		}
		// Write malformed length-prefixed JSON containing a synthetic phone secret.
		const secret = "13899998888"
		bad := fmt.Sprintf(`{"version":1,"type":"model","model":{"screen":"bogus","status":"%s"}}`, secret)
		hdr := []byte{byte(len(bad)), byte(len(bad) >> 8), byte(len(bad) >> 16), byte(len(bad) >> 24)}
		_, _ = inW.Write(append(hdr, bad...))
		inW.Close()

		waitErr := cmd.Wait()
		if waitErr == nil {
			t.Fatal("expected non-zero exit on malformed frame")
		}
		errBuf := make([]byte, 4096)
		n, _ := errR.Read(errBuf)
		if strings.Contains(string(errBuf[:n]), secret) {
			t.Fatalf("stderr leaked secret content: %q", string(errBuf[:n]))
		}
	})
}

// --- Real Win32 Native Child Overlay End-to-End Integration Test ---

func TestNativeHelperChildOverlayLifecycleAndRecreation(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	host := startTestHost(t, "")
	t.Setenv(testEnvRole, "run")

	var (
		bindMu       sync.Mutex
		boundOverlay uintptr
		boundGame    uintptr
		bindCount    int
	)
	embedFn := func(_ context.Context, _ process.Identity, overlayHWND, gameHWND uintptr) error {
		// Mirror broker's real invariants: hidden top-level, matching DPI, strip top-level
		// decorations and convert to WS_CHILD.
		if overlaywin.Visible(overlayHWND) {
			err := fmt.Errorf("overlay %#x must be hidden before embed", overlayHWND)
			t.Logf("embedFn error: %v", err)
			return err
		}
		if !overlaywin.SameDPI(overlayHWND, gameHWND) {
			err := fmt.Errorf("DPI context mismatch between overlay %#x and host %#x", overlayHWND, gameHWND)
			t.Logf("embedFn error: %v", err)
			return err
		}
		client, err := overlaywin.ClientRectOf(gameHWND)
		if err != nil {
			t.Logf("embedFn ClientRectOf(%#x) error: %v", gameHWND, err)
			return err
		}
		if err := overlaywin.SetChild(overlayHWND, gameHWND, client); err != nil {
			t.Logf("embedFn SetChild(%#x, %#x) error: %v", overlayHWND, gameHWND, err)
			return err
		}
		bindMu.Lock()
		boundOverlay = overlayHWND
		boundGame = gameHWND
		bindCount++
		bindMu.Unlock()
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	sess, err := loginui.Start(ctx, loginui.Config{
		Executable:  exe,
		Args:        []string{loginui.Mode},
		EmbedWindow: embedFn,
		BindWindow: func(context.Context, process.Identity, uintptr, uintptr) error {
			return fmt.Errorf("unexpected bind_overlay when child embed succeeds")
		},
	}, loginui.Model{
		Screen:           "phone",
		Subtitle:         "国服账号",
		Remember:         true,
		CanOfficial:      true,
		GameProcessID:    host.pid,
		GameCreationTime: host.created,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// 1. Expect ready frame.
	select {
	case ev := <-sess.Events():
		if ev.Kind != "ready" {
			t.Fatalf("expected ready, got %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ready")
	}

	// 2. Wait for native child binding & visibility.
	waitUntil(t, 8*time.Second, "initial WS_CHILD bind and show", func() bool {
		bindMu.Lock()
		ov, gm, n := boundOverlay, boundGame, bindCount
		bindMu.Unlock()
		return n == 1 && ov != 0 && gm == host.hwnd && overlaywin.ChildOf(ov, gm) && overlaywin.Visible(ov)
	})

	bindMu.Lock()
	ov1 := boundOverlay
	bindMu.Unlock()

	style, err := overlaywin.Style(ov1)
	if err != nil {
		t.Fatal(err)
	}
	if style&0x40000000 == 0 || style&overlaywin.ChildDecorationMask != 0 {
		t.Fatalf("unexpected child style 0x%x (want WS_CHILD set and decorations cleared)", style)
	}
	if overlaywin.IsCloaked(ov1) {
		t.Fatal("native child overlay remained DWM-cloaked (MyGo PR #2248 regression)")
	}
	surface := overlaywin.FindChildClass(ov1, "MyGoSurface")
	if surface == 0 || overlaywin.IsCloaked(surface) {
		t.Fatalf("expected uncloaked MyGoSurface child under %#x, got %#x", ov1, surface)
	}

	// 3. Verify real DirectComposition rendering via PrintWindow(PW_RENDERFULLCONTENT).
	var img *image.RGBA
	waitUntil(t, 5*time.Second, "non-blank DirectComposition card render", func() bool {
		var capErr error
		img, capErr = captureWindowRGBA(ov1)
		if capErr != nil || img == nil {
			return false
		}
		b := img.Bounds()
		// Center of the card (card is centered in the client area).
		cx := b.Dx() / 2
		// Header lies near top of the centered card; body below header.
		scale := OverlayCardScale(float32(b.Dx()), float32(b.Dy()))
		cardTop := int(float32(b.Dy())/2 - (cardHeight*scale)/2)
		bodyY := cardTop + int(100*scale)
		if bodyY < 0 || bodyY >= b.Dy() {
			return false
		}
		return nearColor(img.RGBAAt(cx, bodyY), color.RGBA{R: 0xE3, G: 0xE3, B: 0xE3, A: 255}, 6)
	})
	// Check translucent backdrop corner is NOT pure white or opaque grey card.
	corner := img.RGBAAt(4, 4)
	if corner.R > 80 || corner.G > 80 || corner.B > 80 {
		t.Fatalf("expected dark translucent mask at corner, got %+v", corner)
	}

	// 4. Resize host window -> child follows client rect.
	host.Do(t, "move 200 160 1020 720")
	wantClient, err := overlaywin.ClientRectOf(host.hwnd)
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 4*time.Second, "child resize sync", func() bool {
		r, err := overlaywin.RectOf(ov1)
		return err == nil && r.W == wantClient.W && r.H == wantClient.H
	})

	// 5. Minimize hides child; restore shows child.
	host.Do(t, "minimize")
	waitUntil(t, 4*time.Second, "hide when host minimized", func() bool {
		return !overlaywin.Visible(ov1)
	})
	host.Do(t, "restore")
	waitUntil(t, 4*time.Second, "show when host restored", func() bool {
		return overlaywin.Visible(ov1)
	})

	// 6. Live model update (accounts screen) keeps session healthy.
	if err := sess.Update(ctx, loginui.Model{
		Screen:            "accounts",
		Subtitle:          "国服账号",
		SelectedAccountID: "a1",
		Accounts:          []loginui.Account{{ID: "a1", Label: "测试账号", MaskedPhone: "138****0000"}},
		CanOfficial:       true,
		GameProcessID:     host.pid,
		GameCreationTime:  host.created,
	}); err != nil {
		t.Fatalf("model update failed: %v", err)
	}

	// 7. Parent HWND destruction + recreation must NOT emit cancel and must rebind.
	oldHost := host.hwnd
	newHost := host.Do(t, "recreate")
	if newHost == 0 || newHost == oldHost {
		t.Fatalf("host did not create a new HWND (old=%#x new=%#x)", oldHost, newHost)
	}
	waitUntil(t, 8*time.Second, "child recreate and rebind to new host HWND", func() bool {
		bindMu.Lock()
		ov, gm, n := boundOverlay, boundGame, bindCount
		bindMu.Unlock()
		return n >= 2 && ov != 0 && ov != ov1 && gm == newHost && overlaywin.ChildOf(ov, gm) && overlaywin.Visible(ov)
	})
	// Confirm no spurious cancel event was emitted during recreation.
	select {
	case ev := <-sess.Events():
		t.Fatalf("unexpected event during parent HWND recreation: %+v", ev)
	default:
	}

	// 8. Target switch (A hidden, B shown while A stays alive) must recreate child cleanly.
	prevOv := func() uintptr {
		bindMu.Lock()
		defer bindMu.Unlock()
		return boundOverlay
	}()
	switchedHost := host.Do(t, "switch")
	waitUntil(t, 8*time.Second, "child recreate on target switch A->B", func() bool {
		bindMu.Lock()
		ov, gm, n := boundOverlay, boundGame, bindCount
		bindMu.Unlock()
		return n >= 3 && ov != 0 && ov != prevOv && gm == switchedHost && overlaywin.ChildOf(ov, gm) && overlaywin.Visible(ov)
	})

	// 9. Game exit terminates helper cleanly with nil error.
	host.Close()
	select {
	case <-sess.Done():
	case <-time.After(6 * time.Second):
		t.Fatal("helper did not exit after game process exited")
	}
	if err := sess.Err(); err != nil {
		t.Fatalf("expected clean exit after game exit, got err=%v", err)
	}
}

// --- DPI Awareness Context & Owner Mode Fallback Test ---

func TestNativeHelperSystemDPIAndOwnerFallback(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	host := startTestHost(t, "system")
	t.Setenv(testEnvRole, "run")

	var (
		mu             sync.Mutex
		sameDPISeen    bool
		boundOverlay   uintptr
		boundOwnerHost uintptr
	)
	// First verify WithThreadDPI matched even a System-aware host, then exercise
	// broker's owner-mode fallback (SetOwner instead of SetChild).
	embedFallbackToOwner := func(_ context.Context, _ process.Identity, overlayHWND, gameHWND uintptr) error {
		mu.Lock()
		sameDPISeen = overlaywin.SameDPI(overlayHWND, gameHWND)
		mu.Unlock()
		if err := overlaywin.SetOwner(overlayHWND, gameHWND); err != nil {
			return err
		}
		mu.Lock()
		boundOverlay = overlayHWND
		boundOwnerHost = gameHWND
		mu.Unlock()
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sess, err := loginui.Start(ctx, loginui.Config{
		Executable:  exe,
		Args:        []string{loginui.Mode},
		EmbedWindow: embedFallbackToOwner,
	}, loginui.Model{
		Screen:           "phone",
		GameProcessID:    host.pid,
		GameCreationTime: host.created,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	select {
	case ev := <-sess.Events():
		if ev.Kind != "ready" {
			t.Fatalf("expected ready, got %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ready timed out")
	}

	waitUntil(t, 8*time.Second, "owner fallback placement and show", func() bool {
		mu.Lock()
		ov, gm := boundOverlay, boundOwnerHost
		mu.Unlock()
		return ov != 0 && overlaywin.Owner(ov) == gm && !overlaywin.HasChildStyle(ov) && overlaywin.Visible(ov)
	})

	mu.Lock()
	matchedDPI := sameDPISeen
	ov := boundOverlay
	mu.Unlock()
	if !matchedDPI {
		t.Fatal("expected WithThreadDPI to match System-aware host DPI context")
	}

	var client, ovRect overlaywin.Rect
	func() {
		// Query both System-DPI-aware windows in their own DPI context so an
		// unaware go test runner does not virtualize odd physical heights into
		// off-by-one GetClientRect vs GetWindowRect edges.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := overlaywin.WithThreadDPI(host.hwnd, func() error {
			var err error
			client, err = overlaywin.ClientRectOf(host.hwnd)
			if err != nil {
				return err
			}
			ovRect, err = overlaywin.RectOf(ov)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}()
	if ovRect != client {
		t.Fatalf("owner overlay rect %+v != host client %+v", ovRect, client)
	}

	if err := sess.Close(); err != nil {
		t.Fatalf("clean close failed: %v", err)
	}
}

// --- Real Win32 Welcome Banner Embedding, Visibility, Click-Through & Self-Exit Test ---

func TestNativeHelperWelcomeBannerIsolation(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wfp := tUser32.NewProc("WindowFromPoint")

	t.Run("prefers-child-embed-visible-over-topmost-host-and-click-through", func(t *testing.T) {
		host := startTestHost(t, "")
		// Mark host HWND_TOPMOST (-1) to simulate a fullscreen/topmost game window
		// where a non-topmost owned top-level banner would be occluded behind the game.
		hwndTopmost := ^uintptr(0)
		tSetWindowPos.Call(host.hwnd, hwndTopmost, 0, 0, 0, 0, 0x0013)
		t.Setenv(testEnvRole, "run")

		var (
			mu          sync.Mutex
			bannerHWND  uintptr
			embedCalled bool
			bindCalled  bool
		)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		sess, err := loginui.Start(ctx, loginui.Config{
			Executable: exe,
			Args:       []string{loginui.Mode},
			EmbedWindow: func(_ context.Context, _ process.Identity, ov, gm uintptr) error {
				if overlaywin.Visible(ov) {
					return fmt.Errorf("banner %#x must be hidden before embed", ov)
				}
				if !overlaywin.SameDPI(ov, gm) {
					return fmt.Errorf("DPI mismatch between banner %#x and host %#x", ov, gm)
				}
				client, err := overlaywin.ClientRectOf(gm)
				if err != nil {
					return err
				}
				if err := overlaywin.SetChild(ov, gm, client); err != nil {
					return err
				}
				mu.Lock()
				embedCalled = true
				bannerHWND = ov
				mu.Unlock()
				return nil
			},
			BindWindow: func(context.Context, process.Identity, uintptr, uintptr) error {
				mu.Lock()
				bindCalled = true
				mu.Unlock()
				return fmt.Errorf("unexpected bind_overlay when embed_overlay succeeds")
			},
		}, loginui.Model{
			Screen:           "welcome",
			Banner:           "夜航星，欢迎登录",
			GameProcessID:    host.pid,
			GameCreationTime: host.created,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer sess.Close()

		select {
		case ev := <-sess.Events():
			if ev.Kind != "ready" {
				t.Fatalf("expected ready, got %+v", ev)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("ready timed out")
		}

		waitUntil(t, 6*time.Second, "banner child-embedded and visible", func() bool {
			mu.Lock()
			hw := bannerHWND
			mu.Unlock()
			return hw != 0 && overlaywin.ChildOf(hw, host.hwnd) && overlaywin.Visible(hw)
		})

		mu.Lock()
		hw := bannerHWND
		usedEmbed := embedCalled
		usedBind := bindCalled
		mu.Unlock()
		if !usedEmbed || usedBind {
			t.Fatalf("welcome banner must prefer embed_overlay (embed=%v bind=%v)", usedEmbed, usedBind)
		}
		if overlaywin.IsCloaked(hw) {
			t.Fatal("welcome banner child remained DWM-cloaked")
		}
		ex, err := overlaywin.ExStyle(hw)
		if err != nil {
			t.Fatal(err)
		}
		const (
			wsExTopmost     = 0x00000008
			wsExTransparent = 0x00000020
			wsExToolWindow  = 0x00000080
			wsExLayered     = 0x00080000
			wsExNoActivate  = 0x08000000
		)
		if ex&wsExTopmost != 0 {
			t.Fatalf("welcome banner must never be global topmost: ex=0x%x", ex)
		}
		wantEx := uintptr(wsExTransparent | wsExToolWindow | wsExLayered | wsExNoActivate)
		if ex&wantEx != wantEx {
			t.Fatalf("welcome banner missing click-through/non-activating styles: ex=0x%x want bits 0x%x", ex, wantEx)
		}

		// Verify the pill visibly composites on top of the topmost host client area.
		waitUntil(t, 3*time.Second, "visible banner pill rendered onto host", func() bool {
			hostImg, err := captureWindowRGBA(host.hwnd)
			if err != nil || hostImg == nil {
				return false
			}
			b := hostImg.Bounds()
			lightPixels := 0
			for y := 40; y < 160 && y < b.Dy(); y++ {
				for x := 0; x < b.Dx(); x++ {
					c := hostImg.RGBAAt(x, y)
					if c.R > 180 && c.G > 180 && c.B > 180 {
						lightPixels++
					}
				}
			}
			return lightPixels > 1000
		})

		// Verify mouse hit-testing passes completely through both pill and transparent area to host.
		hostClient, err := overlaywin.ClientRectOf(host.hwnd)
		if err != nil {
			t.Fatal(err)
		}
		for _, pt := range []struct {
			name string
			x, y int32
		}{
			{"pill", hostClient.X + hostClient.W/2, hostClient.Y + 68},
			{"body", hostClient.X + hostClient.W/2, hostClient.Y + 260},
		} {
			p := struct{ X, Y int32 }{X: pt.x, Y: pt.y}
			hit, _, _ := wfp.Call(*(*uintptr)(unsafe.Pointer(&p)))
			if hit != host.hwnd {
				t.Fatalf("click-through failed at %s (%d,%d): WindowFromPoint=%#x want host=%#x (banner=%#x)",
					pt.name, pt.x, pt.y, hit, host.hwnd, hw)
			}
		}

		// Wait for the banner to auto-dismiss and exit cleanly without emitting cancel.
		select {
		case <-sess.Done():
		case <-time.After(8 * time.Second):
			t.Fatal("welcome banner did not self-exit after hold+fade")
		}
		if err := sess.Err(); err != nil {
			t.Fatalf("welcome banner exited with error: %v", err)
		}
		for ev := range sess.Events() {
			t.Fatalf("welcome banner emitted unexpected business event: %+v", ev)
		}
		if !overlaywin.IsWindow(host.hwnd) {
			t.Fatal("dismissing child welcome banner destroyed host window")
		}
	})

	t.Run("owner-fallback-still-displays-and-self-exits", func(t *testing.T) {
		host := startTestHost(t, "")
		t.Setenv(testEnvRole, "run")

		var (
			mu         sync.Mutex
			bannerHWND uintptr
		)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		sess, err := loginui.Start(ctx, loginui.Config{
			Executable: exe,
			Args:       []string{loginui.Mode},
			EmbedWindow: func(_ context.Context, _ process.Identity, ov, gm uintptr) error {
				// Simulate broker falling back to SetOwner on DPI context mismatch.
				if err := overlaywin.SetOwner(ov, gm); err != nil {
					return err
				}
				mu.Lock()
				bannerHWND = ov
				mu.Unlock()
				return nil
			},
		}, loginui.Model{
			Screen:           "welcome",
			Banner:           "夜航星，欢迎登录",
			GameProcessID:    host.pid,
			GameCreationTime: host.created,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer sess.Close()

		select {
		case ev := <-sess.Events():
			if ev.Kind != "ready" {
				t.Fatalf("expected ready, got %+v", ev)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("ready timed out")
		}

		waitUntil(t, 6*time.Second, "owner-fallback banner visible", func() bool {
			mu.Lock()
			hw := bannerHWND
			mu.Unlock()
			return hw != 0 && overlaywin.Owner(hw) == host.hwnd && overlaywin.Visible(hw)
		})

		select {
		case <-sess.Done():
		case <-time.After(8 * time.Second):
			t.Fatal("owner-fallback welcome banner did not self-exit")
		}
		if err := sess.Err(); err != nil {
			t.Fatalf("owner-fallback welcome banner exited with error: %v", err)
		}
	})
}
