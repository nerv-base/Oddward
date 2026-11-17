// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package sdkhost

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/nerv-base/Oddward/internal/cnlogin/cnproto"
	"golang.org/x/sys/windows"
)

type helperCommand struct {
	Action, Pipe string
	Data         []byte
	Count        int
}
type helperResult struct {
	PID      uint32
	Creation uint64
	Window   uintptr
	Data     []byte
	Frames   [][]byte
	Err      string
}
type helperProcess struct {
	t        *testing.T
	cmd      *exec.Cmd
	input    io.WriteCloser
	enc      *json.Encoder
	dec      *json.Decoder
	identity helperResult
}

func startHelper(t *testing.T) *helperProcess {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSDKHostHelper$")
	cmd.Env = append(os.Environ(), "ODDWARD_SDKHOST_HELPER=1")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &helperProcess{t: t, cmd: cmd, input: in, enc: json.NewEncoder(in), dec: json.NewDecoder(out)}
	t.Cleanup(func() { in.Close(); cancel(); _ = cmd.Wait() })
	if err = p.dec.Decode(&p.identity); err != nil {
		t.Fatal(err)
	}
	if p.identity.Err != "" {
		t.Fatal(p.identity.Err)
	}
	return p
}
func (p *helperProcess) request(c helperCommand) helperResult {
	p.t.Helper()
	if err := p.enc.Encode(c); err != nil {
		p.t.Fatal(err)
	}
	var r helperResult
	if err := p.dec.Decode(&r); err != nil {
		p.t.Fatal(err)
	}
	return r
}
func (p *helperProcess) ok(c helperCommand) helperResult {
	p.t.Helper()
	r := p.request(c)
	if r.Err != "" {
		p.t.Fatal(r.Err)
	}
	return r
}
func awaitEvent(t *testing.T, h *Host, kind EventKind) Event {
	t.Helper()
	select {
	case e := <-h.Events():
		if e.Kind != kind {
			t.Fatalf("event kind %d want %d (%v)", e.Kind, kind, e.Err)
		}
		return e
	case <-time.After(4 * time.Second):
		t.Fatalf("missing event %d", kind)
		return Event{}
	}
}

func fixtureFrame(op uint32, size int) []byte {
	b := make([]byte, size)
	binary.LittleEndian.PutUint32(b, cnproto.Magic)
	binary.LittleEndian.PutUint32(b[4:], op)
	binary.LittleEndian.PutUint32(b[8:], uint32(size))
	return b
}
func envRequest(pid uint32) []byte {
	b := fixtureFrame(cnproto.OpEnvInit, 796)
	binary.LittleEndian.PutUint32(b[12:], pid)
	writeWide(b[18:], "1289")
	return b
}
func registerRequest(pid uint32, hwnd uintptr) []byte {
	b := fixtureFrame(0, 1244)
	body := b[12:]
	binary.LittleEndian.PutUint32(body, uint32(hwnd))
	writeWide(body[4:], "1289")
	writeWide(body[0x104:], "0123456789abcdefghijklmnopqrstuv")
	writeWide(body[0x204:], "oneSdkConfig.config")
	writeWide(body[0x40c:], "2.0.8.0")
	for i := 12; i < len(b); i++ {
		b[i] ^= byte(pid)
	}
	return b
}
func queryRequest(pid uint32, kind, contextID int32) []byte {
	b := fixtureFrame(40, 532)
	binary.LittleEndian.PutUint32(b[12:], uint32(kind))
	copy(b[16:], "{}\x00")
	binary.LittleEndian.PutUint32(b[12+0x204:], uint32(contextID))
	for i := 12; i < len(b); i++ {
		b[i] ^= byte(pid)
	}
	return b
}

func TestPipeAuthenticatesBoundCreationAndIndependentClient(t *testing.T) {
	h := testHost(t)
	client := startHelper(t)
	other := startHelper(t)
	if err := h.BindGame(client.identity.PID, client.identity.Creation+1); err != ErrIdentity {
		t.Fatalf("creation mismatch accepted: %v", err)
	}
	// Connection metadata is observable before binding; it does not authorize
	// reading frames, and must still identify a foreign client as foreign.
	if pid := h.ConnectedClientPID(); pid != 0 {
		t.Fatalf("unconnected pipe reported pid %d", pid)
	}
	// An unbound connection can queue, but no SDK frame may be read yet.
	other.ok(helperCommand{Action: "connect", Pipe: h.PipeName()})
	if pid := h.ConnectedClientPID(); pid != other.identity.PID {
		t.Fatalf("queued client pid=%d want %d", pid, other.identity.PID)
	}
	other.ok(helperCommand{Action: "write", Data: envRequest(other.identity.PID)})
	select {
	case e := <-h.Events():
		t.Fatalf("unbound event %v", e.Kind)
	default:
	}
	if err := h.BindGame(client.identity.PID, client.identity.Creation); err != nil {
		t.Fatal(err)
	}
	if r := other.request(helperCommand{Action: "read"}); r.Err == "" || len(r.Data) > 0 {
		t.Fatal("wrong process received environment")
	}
	other.ok(helperCommand{Action: "disconnect"})
	client.ok(helperCommand{Action: "connect", Pipe: h.PipeName()})
	client.ok(helperCommand{Action: "write", Data: envRequest(client.identity.PID)})
	env := client.ok(helperCommand{Action: "read"}).Data
	if len(env) != 5668 || binary.LittleEndian.Uint32(env[4:]) != 1001 || !strings.Contains(string(env[12:]), "ArcMoblieNotify|=|") {
		t.Fatal("environment reply mismatch")
	}
	if readWide(env[0x100c:0x1214]) != h.opts.HostExecutable {
		t.Fatal("environment image mismatch")
	}
	client.ok(helperCommand{Action: "write", Data: registerRequest(client.identity.PID, client.identity.Window)})
	registration := awaitEvent(t, h, EventRegistration).Registration
	tail, err := registration.AppKeyTail()
	if err != nil || string(tail[:]) != "ghijklmnopqrstuv" {
		t.Fatal("registration derived tail mismatch")
	}
	clear(tail[:])
	registration.Zero()
	client.ok(helperCommand{Action: "write", Data: queryRequest(client.identity.PID, 1, 77)})
	if q := awaitEvent(t, h, EventQuery).Query; q.Type != 1 || q.ContextID != 77 {
		t.Fatalf("query %+v", q)
	}
	// Reconnecting the same owned SDK process remains supported.
	client.ok(helperCommand{Action: "disconnect"})
	client.ok(helperCommand{Action: "connect", Pipe: h.PipeName()})
	client.ok(helperCommand{Action: "write", Data: queryRequest(client.identity.PID, 2, -1)})
	if q := awaitEvent(t, h, EventQuery).Query; q.Type != 2 || q.ContextID != -1 {
		t.Fatalf("subscription %+v", q)
	}
}

func TestCallbacksReachOwnedWindowInOrder(t *testing.T) {
	h := testHost(t)
	client := startHelper(t)
	if err := h.BindGame(client.identity.PID, client.identity.Creation); err != nil {
		t.Fatal(err)
	}
	client.ok(helperCommand{Action: "connect", Pipe: h.PipeName()})
	client.ok(helperCommand{Action: "write", Data: envRequest(client.identity.PID)})
	client.ok(helperCommand{Action: "read"})
	client.ok(helperCommand{Action: "write", Data: registerRequest(client.identity.PID, client.identity.Window)})
	awaitEvent(t, h, EventRegistration).Registration.Zero()
	id := cnproto.LoginIdentity{GameUID: []byte("game-user"), GameToken: []byte("game-token"), ChannelUID: []byte("channel-user"), ChannelToken: []byte("channel-token"), Info: []byte(`{"synthetic":true}`)}
	defer id.Zero()
	if err := h.SendLogin(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if err := h.Reply(context.Background(), cnproto.PreLoginReply{Code: 0, ContextID: 77, Body: []byte(`{"code":0}`)}); err != nil {
		t.Fatal(err)
	}
	frames := client.ok(helperCommand{Action: "callbacks", Count: 3}).Frames
	for i, op := range []uint32{0x40d, 2, 49} {
		if binary.LittleEndian.Uint32(frames[i][4:]) != op {
			t.Errorf("callback %d order", i)
		}
	}
	for _, b := range frames {
		for i := 12; i < len(b); i++ {
			b[i] ^= byte(os.Getpid())
		}
	}
	if string(frames[1][12:21]) != "game-user" || binary.LittleEndian.Uint32(frames[2][24:]) != 77 {
		t.Fatal("callback payload mismatch")
	}
	client.ok(helperCommand{Action: "destroy-window"})
	if err := h.SendLogin(context.Background(), id); err != ErrIdentity {
		t.Fatalf("destroyed HWND accepted: %v", err)
	}
}

func TestCloseCancelsPendingPipeRead(t *testing.T) {
	h := testHost(t)
	client := startHelper(t)
	if err := h.BindGame(client.identity.PID, client.identity.Creation); err != nil {
		t.Fatal(err)
	}
	client.ok(helperCommand{Action: "connect", Pipe: h.PipeName()})
	client.ok(helperCommand{Action: "write", Data: envRequest(client.identity.PID)})
	client.ok(helperCommand{Action: "read"})
	done := make(chan error, 1)
	go func() { done <- h.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pending overlapped read prevented Close")
	}
	if r := client.request(helperCommand{Action: "read"}); r.Err == "" {
		t.Fatal("client pipe survived Close")
	}
}

func TestPipeRejectsOversizedMessageAndReportsFailure(t *testing.T) {
	h := testHost(t)
	client := startHelper(t)
	if err := h.BindGame(client.identity.PID, client.identity.Creation); err != nil {
		t.Fatal(err)
	}
	client.ok(helperCommand{Action: "connect", Pipe: h.PipeName()})
	_ = client.request(helperCommand{Action: "write", Data: fixtureFrame(999, 65537)})
	e := awaitEvent(t, h, EventFailure)
	if e.Err == nil {
		t.Fatal("missing protocol failure")
	}
	if windowPID(h.hwnd) != uint32(os.Getpid()) {
		t.Fatal("protocol failure destroyed active host")
	}
}

func TestSDKHostHelper(t *testing.T) {
	if os.Getenv("ODDWARD_SDKHOST_HELPER") != "1" {
		return
	}
	runSDKHelper()
	os.Exit(0)
}

var helperFrames = make(chan []byte, 16)
var helperProc = windows.NewCallback(func(hwnd, msg, wparam, lparam uintptr) uintptr {
	if msg == wmCopyData {
		data := (*copyData)(unsafe.Add(nil, lparam))
		if data.Tag != cnproto.CopyDataTag || data.Size < 12 || data.Size > 65536 || windowPID(wparam) == 0 {
			return 0
		}
		packet := append([]byte(nil), unsafe.Slice(data.Data, int(data.Size))...)
		helperFrames <- packet
		return 1
	}
	if msg == wmClose {
		winDestroyWindow.Call(hwnd)
		return 0
	}
	if msg == wmDestroy {
		winPostQuit.Call(0)
		return 0
	}
	r, _, _ := winDefWindowProc.Call(hwnd, msg, wparam, lparam)
	return r
})

func runSDKHelper() {
	enc, dec := json.NewEncoder(os.Stdout), json.NewDecoder(os.Stdin)
	windowReady := make(chan uintptr, 1)
	windowDone := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(windowDone)
		class, _ := windows.UTF16PtrFromString(fmt.Sprintf("Oddward.SDK.Test.%d", os.Getpid()))
		module, _, _ := winModuleHandle.Call(0)
		wc := windowClass{Proc: helperProc, Instance: module, Name: class}
		wc.Size = uint32(unsafe.Sizeof(wc))
		winRegisterClass.Call(uintptr(unsafe.Pointer(&wc)))
		defer winUnregisterClass.Call(uintptr(unsafe.Pointer(class)), module)
		hwnd, _, _ := winCreateWindow.Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(class)), 0, 0, 0, 0, 0, 0, 0, module, 0)
		windowReady <- hwnd
		if hwnd == 0 {
			return
		}
		var m windowMessage
		for {
			r, _, _ := winGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(r) <= 0 {
				return
			}
			winDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
		}
	}()
	hwnd := <-windowReady
	created, err := processCreation(uint32(os.Getpid()))
	first := helperResult{PID: uint32(os.Getpid()), Creation: created, Window: hwnd}
	if err != nil || hwnd == 0 {
		first.Err = "helper initialization failed"
	}
	enc.Encode(first)
	var pipe windows.Handle
	defer func() {
		if pipe != 0 {
			windows.CloseHandle(pipe)
		}
		if hwnd != 0 {
			winPostMessage.Call(hwnd, wmClose, 0, 0)
		}
		<-windowDone
	}()
	for {
		var c helperCommand
		if err := dec.Decode(&c); err != nil {
			return
		}
		r := helperResult{}
		var err error
		switch c.Action {
		case "connect":
			name, _ := windows.UTF16PtrFromString(`\\.\pipe\` + c.Pipe)
			deadline := time.Now().Add(2 * time.Second)
			for {
				pipe, err = windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
				if err != windows.ERROR_PIPE_BUSY || time.Now().After(deadline) {
					break
				}
				runtime.Gosched()
			}
			if err == nil {
				mode := uint32(windows.PIPE_READMODE_MESSAGE)
				err = windows.SetNamedPipeHandleState(pipe, &mode, nil, nil)
			}
		case "write":
			var n uint32
			err = windows.WriteFile(pipe, c.Data, &n, nil)
		case "read":
			b := make([]byte, 65536)
			var n uint32
			err = windows.ReadFile(pipe, b, &n, nil)
			r.Data = b[:n]
		case "disconnect":
			if pipe != 0 {
				err = windows.CloseHandle(pipe)
				pipe = 0
			}
		case "callbacks":
			for i := 0; i < c.Count; i++ {
				select {
				case b := <-helperFrames:
					r.Frames = append(r.Frames, b)
				case <-time.After(5 * time.Second):
					err = fmt.Errorf("callback timeout")
				}
			}
		case "destroy-window":
			winPostMessage.Call(hwnd, wmClose, 0, 0)
			<-windowDone
			hwnd = 0
		default:
			err = fmt.Errorf("unknown helper action")
		}
		if err != nil {
			r.Err = err.Error()
		}
		enc.Encode(r)
	}
}
