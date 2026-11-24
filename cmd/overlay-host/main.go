//go:build windows

// Command overlay-host is a standalone Win32 window that stands in for the
// game window while the MyGo overlay PoC is exercised. It is deliberately not
// the real game: it only provides a normal / frameless top-level window with
// observable clicks and keys, plus a local control channel so automated tests
// can move, resize, minimize, restore and recreate it.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	gdi32                = windows.NewLazySystemDLL("gdi32.dll")
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procRegisterClassEx  = user32.NewProc("RegisterClassExW")
	procCreateWindowEx   = user32.NewProc("CreateWindowExW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procDefWindowProc    = user32.NewProc("DefWindowProcW")
	procGetMessage       = user32.NewProc("GetMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessage  = user32.NewProc("DispatchMessageW")
	procPostMessage      = user32.NewProc("PostMessageW")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	procShowWindow       = user32.NewProc("ShowWindow")
	procUpdateWindow     = user32.NewProc("UpdateWindow")
	procBeginPaint       = user32.NewProc("BeginPaint")
	procEndPaint         = user32.NewProc("EndPaint")
	procFillRect         = user32.NewProc("FillRect")
	procGetClientRect    = user32.NewProc("GetClientRect")
	procGetWindowRect    = user32.NewProc("GetWindowRect")
	procCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")
	procGetModuleHandle  = kernel32.NewProc("GetModuleHandleW")
)

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

type rect struct{ Left, Top, Right, Bottom int32 }
type msg struct {
	Hwnd    windows.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

const (
	wsOverlappedWindow = 0x00CF0000
	wsPopup            = 0x80000000
	wsThickFrame       = 0x00040000
	wsCaption          = 0x00C00000
	wsMinimizeBox      = 0x00020000
	wsMaximizeBox      = 0x00010000
	wsSysMenu          = 0x00080000
	wsClipChildren     = 0x02000000
	swShow             = 5
	swRestore          = 9
	wmDestroy          = 0x0002
	wmPaint            = 0x000F
	wmClose            = 0x0010
	wmSize             = 0x0005
	wmKeyDown          = 0x0100
	wmChar             = 0x0102
	wmLButtonDown      = 0x0201
	wmLButtonUp        = 0x0202
	wmRButtonDown      = 0x0204
	wmSysCommand       = 0x0112
	scMinimize         = 0xF020
	scRestore          = 0xF120
	wmApp              = 0x8000
	csHRedraw          = 0x0001
	csVRedraw          = 0x0002
)

type hostWindow struct {
	mu         sync.Mutex
	hwnd       uintptr
	pid        uint32
	mode       string
	x, y, w, h int
	className  string
	title      string
	created    string
	brush      uintptr
	logPath    string
	status     string
	clicks     int
	lastX      int32
	lastY      int32
	closed     bool
	jobs       chan func()
	classOK    bool
	recreating bool
}

var host hostWindow

type statusFile struct {
	HWND      uint64 `json:"hwnd"`
	PID       uint32 `json:"pid"`
	Created   string `json:"created"`
	Mode      string `json:"mode"`
	Minimized bool   `json:"minimized"`
	Rect      rect   `json:"rect"`
	Client    rect   `json:"client"`
}

func main() {
	runtime.LockOSThread()
	var control int
	var duration int
	flag.StringVar(&host.mode, "mode", "window", "window or frameless")
	flag.IntVar(&host.x, "x", 120, "x")
	flag.IntVar(&host.y, "y", 120, "y")
	flag.IntVar(&host.w, "w", 900, "width")
	flag.IntVar(&host.h, "h", 640, "height")
	flag.StringVar(&host.status, "status", "", "status JSON path")
	flag.StringVar(&host.logPath, "log", "", "event JSONL path")
	flag.IntVar(&control, "control", 0, "control TCP port on 127.0.0.1")
	flag.IntVar(&duration, "duration", 0, "seconds before exiting, 0 = forever")
	flag.Parse()

	host.jobs = make(chan func(), 16)
	host.pid = uint32(os.Getpid())
	host.className = fmt.Sprintf("OddwardOverlayHost-%d", host.pid)
	host.title = "Oddward Overlay Host"
	self := windows.CurrentProcess()
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(self, &creation, &exit, &kernel, &user); err != nil {
		log.Fatalf("host process times: %v", err)
	}
	host.created = fmt.Sprintf("%d", uint64(creation.HighDateTime)<<32|uint64(creation.LowDateTime))

	if err := host.create(); err != nil {
		log.Fatal(err)
	}
	if control != 0 {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", control))
		if err != nil {
			log.Fatalf("control listen: %v", err)
		}
		go serveControl(ln)
	}
	if duration > 0 {
		go func() {
			time.Sleep(time.Duration(duration) * time.Second)
			procPostMessage.Call(host.currentHWND(), wmClose, 0, 0)
		}()
	}
	if err := host.pump(); err != nil {
		log.Fatal(err)
	}
}

func (h *hostWindow) currentHWND() uintptr {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hwnd
}

func (h *hostWindow) create() error {
	className, _ := windows.UTF16PtrFromString(h.className)
	title, _ := windows.UTF16PtrFromString(h.title)
	hInst, _, _ := procGetModuleHandle.Call(0)
	wc := wndClassEx{
		Size:      uint32(unsafe.Sizeof(wndClassEx{})),
		Style:     csHRedraw | csVRedraw,
		WndProc:   windows.NewCallback(hostProc),
		Instance:  windows.Handle(hInst),
		ClassName: className,
	}
	if !h.classOK {
		if r, _, err := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
			return fmt.Errorf("RegisterClassEx: %v", err)
		}
		h.classOK = true
	}
	style := uintptr(wsOverlappedWindow)
	if h.mode == "frameless" {
		style = wsPopup | wsThickFrame | wsClipChildren
	}
	hwnd, _, err := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(title)),
		style, uintptr(h.x), uintptr(h.y), uintptr(h.w), uintptr(h.h), 0, 0, hInst, 0)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowEx: %v", err)
	}
	if h.brush == 0 {
		h.brush, _, _ = procCreateSolidBrush.Call(0x00101820)
	}
	h.mu.Lock()
	h.hwnd = hwnd
	h.closed = false
	h.mu.Unlock()
	procShowWindow.Call(hwnd, swShow)
	procShowWindow.Call(hwnd, swRestore)
	procUpdateWindow.Call(hwnd)
	fmt.Printf("hwnd=%d pid=%d mode=%s\n", hwnd, h.pid, h.mode)
	_ = os.Stdout.Sync()
	h.writeStatus()
	h.logEvent("created", map[string]any{"hwnd": hwnd})
	return nil
}

func (h *hostWindow) pump() error {
	var m msg
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return nil
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func (h *hostWindow) writeStatus() {
	h.mu.Lock()
	hwnd := h.hwnd
	h.mu.Unlock()
	if h.status == "" || hwnd == 0 {
		return
	}
	var wr, cr rect
	procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&cr)))
	min := false
	if v, _, _ := user32.NewProc("IsIconic").Call(hwnd); v != 0 {
		min = true
	}
	st := statusFile{
		HWND: uint64(hwnd), PID: h.pid, Created: h.created, Mode: h.mode, Minimized: min,
		Rect: wr, Client: cr,
	}
	b, _ := json.Marshal(st)
	tmp := h.status + ".tmp"
	_ = os.WriteFile(tmp, append(b, '\n'), 0o644)
	_ = os.Rename(tmp, h.status)
}

func (h *hostWindow) logEvent(kind string, fields map[string]any) {
	if h.logPath == "" {
		return
	}
	entry := map[string]any{"event": kind, "time": time.Now().UnixMilli()}
	for k, v := range fields {
		entry[k] = v
	}
	b, _ := json.Marshal(entry)
	f, err := os.OpenFile(h.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

func serveControl(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go handleControl(conn)
	}
}

func handleControl(conn net.Conn) {
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		reply := runCommand(sc.Text())
		b, _ := json.Marshal(reply)
		_, _ = conn.Write(append(b, '\n'))
	}
}

func runCommand(line string) map[string]any {
	var cmd string
	var args []string
	fmt.Sscanf(line, "%s", &cmd)
	rest := line[len(cmd):]
	args = splitFields(rest)
	switch cmd {
	case "status":
		return map[string]any{"ok": true, "hwnd": host.currentHWND(), "pid": host.pid, "minimized": isMinimized(host.currentHWND())}
	case "minimize":
		procShowWindow.Call(host.currentHWND(), 6) // SW_MINIMIZE
		host.logEvent("minimize", nil)
		return map[string]any{"ok": true}
	case "restore":
		procShowWindow.Call(host.currentHWND(), swRestore)
		host.logEvent("restore", nil)
		return map[string]any{"ok": true}
	case "move":
		var x, y, w, h int
		if len(args) != 4 {
			return map[string]any{"ok": false, "error": "move x y w h"}
		}
		fmt.Sscanf(args[0], "%d", &x)
		fmt.Sscanf(args[1], "%d", &y)
		fmt.Sscanf(args[2], "%d", &w)
		fmt.Sscanf(args[3], "%d", &h)
		hwnd := host.currentHWND()
		user32.NewProc("SetWindowPos").Call(hwnd, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 0x0014) // NOZORDER|NOACTIVATE
		host.writeStatus()
		host.logEvent("move", map[string]any{"x": x, "y": y, "w": w, "h": h})
		return map[string]any{"ok": true}
	case "recreate":
		var err error
		host.onUI(func() { err = host.recreate() })
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}
		}
		return map[string]any{"ok": true, "hwnd": host.currentHWND()}
	case "destroy":
		host.onUI(func() { procDestroyWindow.Call(host.currentHWND()) })
		return map[string]any{"ok": true}
	case "quit":
		procPostMessage.Call(host.currentHWND(), wmClose, 0, 0)
		return map[string]any{"ok": true}
	case "log":
		return map[string]any{"ok": true}
	default:
		return map[string]any{"ok": false, "error": "unknown command"}
	}
}

func splitFields(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func isMinimized(hwnd uintptr) bool {
	v, _, _ := user32.NewProc("IsIconic").Call(hwnd)
	return v != 0
}

func (h *hostWindow) recreate() error {
	h.mu.Lock()
	h.recreating = true
	old := h.hwnd
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.recreating = false
		h.mu.Unlock()
	}()
	var wr rect
	procGetWindowRect.Call(old, uintptr(unsafe.Pointer(&wr)))
	h.x, h.y = int(wr.Left), int(wr.Top)
	h.w, h.h = int(wr.Right-wr.Left), int(wr.Bottom-wr.Top)
	procDestroyWindow.Call(old)
	if err := h.create(); err != nil {
		return err
	}
	h.logEvent("recreated", map[string]any{"hwnd": h.currentHWND()})
	return nil
}

// onUI runs fn on the window's own thread and waits for it. The control server
// goroutine must not call DestroyWindow or CreateWindow itself.
func (h *hostWindow) onUI(fn func()) {
	done := make(chan struct{})
	h.jobs <- func() {
		defer close(done)
		fn()
	}
	procPostMessage.Call(h.currentHWND(), wmApp, 0, 0)
	<-done
}

func hostProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case wmPaint:
		var ps struct {
			Hdc       windows.Handle
			Erase     int32
			Paint     rect
			Restore   int32
			IncUpdate int32
			Reserved  [32]byte
		}
		procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		var cr rect
		procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&cr)))
		procFillRect.Call(uintptr(ps.Hdc), uintptr(unsafe.Pointer(&cr)), host.brush)
		// Draw a crosshair at the last click so captures can prove input arrived.
		host.mu.Lock()
		cx, cy, clicks := host.lastX, host.lastY, host.clicks
		host.mu.Unlock()
		if clicks > 0 {
			pen, _, _ := gdi32.NewProc("CreatePen").Call(0, 3, 0x0000FFFF)
			old, _, _ := gdi32.NewProc("SelectObject").Call(uintptr(ps.Hdc), pen)
			procMoveToEx := gdi32.NewProc("MoveToEx")
			procLineTo := gdi32.NewProc("LineTo")
			procMoveToEx.Call(uintptr(ps.Hdc), uintptr(cx-20), uintptr(cy), 0)
			procLineTo.Call(uintptr(ps.Hdc), uintptr(cx+20), uintptr(cy))
			procMoveToEx.Call(uintptr(ps.Hdc), uintptr(cx), uintptr(cy-20), 0)
			procLineTo.Call(uintptr(ps.Hdc), uintptr(cx), uintptr(cy+20))
			gdi32.NewProc("SelectObject").Call(uintptr(ps.Hdc), old)
			gdi32.NewProc("DeleteObject").Call(pen)
		}
		procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		return 0
	case wmLButtonDown:
		x, y := int32(lParam&0xffff), int32((lParam>>16)&0xffff)
		host.mu.Lock()
		host.clicks++
		host.lastX, host.lastY = x, y
		host.mu.Unlock()
		host.logEvent("click", map[string]any{"x": x, "y": y, "button": "left"})
		user32.NewProc("InvalidateRect").Call(hwnd, 0, 1)
		return 0
	case wmRButtonDown:
		x, y := int32(lParam&0xffff), int32((lParam>>16)&0xffff)
		host.logEvent("click", map[string]any{"x": x, "y": y, "button": "right"})
		return 0
	case wmKeyDown, wmChar:
		host.logEvent("key", map[string]any{"message": message, "key": wParam})
		return 0
	case wmSize:
		var cr rect
		procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&cr)))
		host.writeStatus()
		host.logEvent("size", map[string]any{"w": cr.Right, "h": cr.Bottom})
		return 0
	case wmSysCommand:
		cmd := wParam & 0xFFF0
		if cmd == scMinimize {
			host.logEvent("minimize", nil)
		} else if cmd == scRestore {
			host.logEvent("restore", nil)
		}
	case wmClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		host.mu.Lock()
		host.hwnd = 0
		host.closed = true
		quitting := !host.recreating
		host.mu.Unlock()
		host.logEvent("destroyed", nil)
		if quitting {
			procPostQuitMessage.Call(0)
		}
		return 0
	case wmApp:
		for {
			select {
			case fn := <-host.jobs:
				fn()
			default:
				return 0
			}
		}
	}
	r, _, _ := procDefWindowProc.Call(hwnd, uintptr(message), wParam, lParam)
	return r
}
