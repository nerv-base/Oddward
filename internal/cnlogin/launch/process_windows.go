//go:build windows

// SPDX-License-Identifier: GPL-3.0-or-later
package launch

import (
	"context"
	"errors"
	"fmt"
	"github.com/nerv-base/Oddward/internal/cnlogin/cnproto"
	"github.com/nerv-base/Oddward/internal/process"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"
)

// Options configures the native SDK pipe and optional game arguments. A nil
// ParentEnvironment inherits os.Environ; a non-nil empty slice starts empty.
// Priority settings are best-effort. No option overrides the compatibility gate.
type Options struct {
	PipeName, AdditionalArguments string
	ParentEnvironment             []string
	// IncludeImage uses standard Windows argv[0] framing when an elevated
	// host starts the game without its environment-losing elevation bootstrap.
	IncludeImage  bool
	PriorityClass uint32
	PriorityBoost *bool
	// ConnectedClientPID observes pipe connection metadata without reading SDK
	// frames. Zero means no connection. Binding still verifies PID + FILETIME.
	ConnectedClientPID func() uint32
}

// Prepared holds the single-instance gate before the caller creates its SDK host.
// On successful creation Start transfers the gate to Game. Close is then a no-op.
// The zero value is closed; obtain instances only through Prepare.
type Prepared struct {
	mu        sync.Mutex
	info      Info
	gate      *instanceGate
	logParent string
	resolve   func(string, string) (cnproto.Profile, error)
	check     func(context.Context) error
	closed    bool
	closeErr  error
}

func Prepare(ctx context.Context, root string) (*Prepared, error) {
	return prepare(ctx, root, cnproto.ResolveProfile, CheckProcesses, instanceName)
}
func prepare(ctx context.Context, root string, resolve func(string, string) (cnproto.Profile, error), check func(context.Context) error, name string) (*Prepared, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(root) {
		return nil, ErrFilesMissing
	}
	stat, err := os.Stat(filepath.Join(root, `Client\WindowsNoEditor\HT\Binaries\Win64\HTGame.exe`))
	if err != nil || !stat.Mode().IsRegular() {
		return nil, ErrFilesMissing
	}
	if err := check(ctx); err != nil {
		return nil, err
	}
	info, err := inspect(ctx, root, resolve)
	if err != nil {
		return nil, err
	}
	gate, err := acquireInstance(name)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = gate.Close()
		return nil, err
	}
	return &Prepared{info: info, gate: gate, logParent: os.TempDir(), resolve: resolve, check: check}, nil
}
func (p *Prepared) Info() Info { p.mu.Lock(); defer p.mu.Unlock(); return p.info }
func (p *Prepared) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		if p.gate != nil {
			p.closeErr = p.gate.Close()
			p.gate = nil
		}
	}
	return p.closeErr
}

// Start rechecks compatibility immediately before creating the process. If game
// is non-nil it was created, including when err reports a post-start identity
// query failure: callers must still retain the SDK host and Wait/Close the game.
func (p *Prepared) Start(ctx context.Context, o Options) (*Game, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.gate == nil {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := p.check(ctx); err != nil {
		return nil, err
	}
	info, err := inspect(ctx, p.info.Root, p.resolve)
	if err != nil {
		return nil, err
	}
	env := o.ParentEnvironment
	if env == nil {
		env = os.Environ()
	}
	build := BuildOptions{Root: info.Root, Bridge: info.Bridge, PipeName: o.PipeName, LogDirectory: filepath.Join(p.logParent, "pending"), AdditionalArguments: o.AdditionalArguments, ParentEnvironment: env, Patcher: info.Patcher}
	if _, err := BuildPlan(build); err != nil {
		return nil, err
	}
	logs, err := createPrivateLogs(ctx, p.logParent)
	if err != nil {
		return nil, err
	}
	build.LogDirectory = logs.path
	plan, err := BuildPlan(build)
	if err != nil {
		return nil, errors.Join(err, logs.Close())
	}
	game, err := startProcess(ctx, plan, o)
	if game == nil {
		return nil, errors.Join(err, logs.Close())
	}
	if err == nil {
		err = game.awaitSuccessor(ctx, info.GameExecutable, o.ConnectedClientPID)
	}
	game.logs = logs
	game.gate = p.gate
	p.gate = nil
	p.closed = true
	p.info = info
	return game, err
}

// Game owns the process handle returned directly by CreateProcess. Identity uses
// that handle's PID and raw FILETIME, never a fresh PID lookup. Wait cancellation
// stops only observation; Close refuses while the process still runs.
type Game struct {
	mu       sync.Mutex
	handle   windows.Handle
	identity process.Identity
	image    string
	logs     *privateLogs
	gate     *instanceGate
	closed   bool
	closeErr error
}

func (g *Game) Identity() process.Identity { return g.identity }
func (g *Game) Wait(ctx context.Context) (uint32, error) {
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		g.mu.Lock()
		if g.closed || g.handle == 0 {
			g.mu.Unlock()
			return 0, ErrClosed
		}
		status, err := windows.WaitForSingleObject(g.handle, 50)
		if err != nil {
			g.mu.Unlock()
			return 0, err
		}
		if status == windows.WAIT_OBJECT_0 {
			var code uint32
			err = windows.GetExitCodeProcess(g.handle, &code)
			g.mu.Unlock()
			return code, err
		}
		g.mu.Unlock()
		if status != uint32(windows.WAIT_TIMEOUT) {
			return 0, errors.New("launch: unexpected process wait result")
		}
	}
}

// Close never terminates the game. Once exit is confirmed, it closes the process
// handle, destroys private raw SDK logs, and releases the instance gate once.
func (g *Game) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return g.closeErr
	}
	if g.handle != 0 {
		status, err := windows.WaitForSingleObject(g.handle, 0)
		if err != nil {
			return err
		}
		if status == uint32(windows.WAIT_TIMEOUT) {
			return ErrGameRunning
		}
		if status != windows.WAIT_OBJECT_0 {
			return errors.New("launch: unexpected process wait result")
		}
		g.closeErr = windows.CloseHandle(g.handle)
		g.handle = 0
	}
	g.closed = true
	if g.logs != nil {
		g.closeErr = errors.Join(g.closeErr, g.logs.Close())
	}
	if g.gate != nil {
		g.closeErr = errors.Join(g.closeErr, g.gate.Close())
	}
	return g.closeErr
}

// awaitSuccessor handles the retail HTGame.exe bootstrap: it may create the
// real game process with the same image path and exit immediately. The session
// must then observe and bind the child, not the short-lived bootstrap.
func (g *Game) awaitSuccessor(ctx context.Context, image string, connectedClientPID func() uint32) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// A live, owned process that connected the SDK pipe is ready to bind.
		// Waiting the full bootstrap grace period here would consume the SDK's
		// entire five-second environment-handshake deadline. A foreign client
		// must not end the wait or become the process we track.
		if connectedClientPID != nil && connectedClientPID() == g.identity.PID {
			status, err := windows.WaitForSingleObject(g.handle, 0)
			if err != nil {
				return err
			}
			if status == uint32(windows.WAIT_TIMEOUT) {
				return nil
			}
		}
		handle, identity, ok, err := findGameProcess(g.identity.PID, g.identity.CreationTime, image)
		if err != nil {
			return err
		}
		if ok {
			_ = windows.CloseHandle(g.handle)
			g.handle = handle
			g.identity = identity
			g.image = image
			return nil
		}
		status, err := windows.WaitForSingleObject(g.handle, 100)
		if err != nil {
			return err
		}
		if status == uint32(windows.WAIT_TIMEOUT) {
			if time.Now().After(deadline) {
				return nil
			}
			continue
		}
		if status != windows.WAIT_OBJECT_0 {
			return errors.New("launch: unexpected process wait result")
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("launch: game launcher exited without starting %s", filepath.Base(image))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// findGameProcess looks for a newer process with the expected image path. The
// bootstrap is excluded by PID and by its recorded creation time. On success,
// the caller owns the same handle used to verify the returned identity.
func findGameProcess(bootstrapPID uint32, bootstrapCreation uint64, image string) (windows.Handle, process.Identity, bool, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, process.Identity{}, false, err
	}
	defer windows.CloseHandle(snap)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	err = windows.Process32First(snap, &entry)
	for err == nil {
		pid := entry.ProcessID
		if pid != 0 && pid != bootstrapPID && entry.ParentProcessID == bootstrapPID && strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), filepath.Base(image)) {
			handle, openErr := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
			if openErr == nil {
				buffer := make([]uint16, windows.MAX_PATH)
				size := uint32(len(buffer))
				if queryErr := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); queryErr == nil {
					path := windows.UTF16ToString(buffer[:size])
					var creation, exit, kernel, user windows.Filetime
					if timesErr := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); timesErr == nil {
						created := uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime)
						if created >= bootstrapCreation && strings.EqualFold(filepath.Clean(path), filepath.Clean(image)) {
							return handle, process.Identity{PID: pid, CreationTime: created}, true, nil
						}
					}
				}
				_ = windows.CloseHandle(handle)
			}
		}
		err = windows.Process32Next(snap, &entry)
	}
	if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return 0, process.Identity{}, false, nil
	}
	return 0, process.Identity{}, false, err
}

func startProcess(ctx context.Context, p LaunchPlan, o Options) (*Game, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	image, err := windows.UTF16PtrFromString(p.Image)
	if err != nil {
		return nil, err
	}
	commandLine := p.CommandLine
	if o.IncludeImage {
		// A Windows executable path cannot contain a double quote. Preserve its
		// spaces as argv[0], followed by the unchanged game arguments.
		if strings.ContainsRune(p.Image, '"') {
			return nil, errors.New("launch: invalid image path")
		}
		commandLine = `"` + p.Image + `" ` + commandLine
	}
	command, err := windows.UTF16FromString(commandLine)
	if err != nil {
		return nil, err
	}
	if len(command) > 32767 {
		return nil, errors.New("launch: command line too long")
	}
	directory, err := windows.UTF16PtrFromString(p.Directory)
	if err != nil {
		return nil, err
	}
	environment, err := EnvironmentBlock(p.Environment)
	if err != nil {
		return nil, err
	}
	nullName := windows.StringToUTF16Ptr("NUL")
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	null, err := windows.CreateFile(nullName, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, &sa, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(null)
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, err
	}
	defer attrs.Delete()
	handles := []windows.Handle{null}
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), unsafe.Sizeof(handles[0])); err != nil {
		return nil, err
	}
	startup := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{})), Flags: windows.STARTF_USESTDHANDLES, StdInput: null, StdOutput: null, StdErr: null}, ProcThreadAttributeList: attrs.List()}
	var pi windows.ProcessInformation
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	err = windows.CreateProcess(image, &command[0], nil, nil, true, windows.CREATE_UNICODE_ENVIRONMENT|windows.EXTENDED_STARTUPINFO_PRESENT, &environment[0], directory, &startup.StartupInfo, &pi)
	runtime.KeepAlive(handles)
	runtime.KeepAlive(attrs)
	if err != nil {
		return nil, err
	}
	_ = windows.CloseHandle(pi.Thread)
	game := &Game{handle: pi.Process, identity: process.Identity{PID: pi.ProcessId}, image: p.Image}
	var creation, exit, kernel, user windows.Filetime
	err = windows.GetProcessTimes(pi.Process, &creation, &exit, &kernel, &user)
	if err != nil {
		return game, err
	}
	game.identity.CreationTime = uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime)
	if game.identity.CreationTime == 0 {
		return game, errors.New("launch: created process has no creation time")
	}
	if o.PriorityClass != 0 {
		_ = windows.SetPriorityClass(pi.Process, o.PriorityClass)
	}
	if o.PriorityBoost != nil {
		_ = windows.SetProcessPriorityBoost(pi.Process, !*o.PriorityBoost)
	}
	return game, nil
}
