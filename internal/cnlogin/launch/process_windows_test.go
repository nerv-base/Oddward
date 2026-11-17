//go:build windows

// SPDX-License-Identifier: GPL-3.0-or-later
package launch

import (
	"context"
	"encoding/json"
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"
)

type helperReport struct {
	CommandLine, Directory, Environment string
	Args                                []string
	StdTypes                            [3]uint32
	SentinelSignaled                    bool
}

func TestMain(m *testing.M) {
	if os.Getenv("ODDWARD_LAUNCH_HELPER") == "1" {
		if bootstrap := os.Getenv("SYNTHETIC_BOOTSTRAP_REPORT"); bootstrap != "" {
			exe, err := os.Executable()
			if err != nil || os.WriteFile(bootstrap, []byte(strconv.Itoa(os.Getpid())), 0600) != nil {
				os.Exit(94)
			}
			child := exec.Command(exe)
			child.Env = append(os.Environ(), "SYNTHETIC_BOOTSTRAP_REPORT=")
			child.Stdout, child.Stderr = os.Stdout, os.Stderr
			if err := child.Start(); err != nil {
				os.Exit(95)
			}
			_ = child.Process.Release()
			os.Exit(24)
		}
		if pipe := os.Getenv("SYNTHETIC_PIPE"); pipe != "" {
			h, err := windows.CreateFile(windows.StringToUTF16Ptr(pipe), windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
			if err != nil {
				os.Exit(93)
			}
			defer windows.CloseHandle(h)
		}
		report := helperReport{CommandLine: windows.UTF16PtrToString(windows.GetCommandLine()), Args: os.Args, Environment: os.Getenv("SYNTHETIC_UNICODE")}
		report.Directory, _ = os.Getwd()
		for i, std := range []uint32{windows.STD_INPUT_HANDLE, windows.STD_OUTPUT_HANDLE, windows.STD_ERROR_HANDLE} {
			h, _ := windows.GetStdHandle(std)
			report.StdTypes[i], _ = windows.GetFileType(h)
		}
		sentinel, _ := strconv.ParseUint(os.Getenv("SYNTHETIC_HANDLE"), 10, 64)
		report.SentinelSignaled = windows.SetEvent(windows.Handle(sentinel)) == nil
		_, _ = os.Stdout.Write(make([]byte, 256<<10))
		_, _ = os.Stderr.Write(make([]byte, 256<<10))
		b, _ := json.Marshal(report)
		if os.WriteFile(os.Getenv("SYNTHETIC_REPORT"), b, 0600) != nil {
			os.Exit(91)
		}
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(os.Getenv("SYNTHETIC_RELEASE")); err == nil {
				os.Exit(23)
			}
			time.Sleep(10 * time.Millisecond)
		}
		os.Exit(92)
	}
	os.Exit(m.Run())
}

func TestCreateProcessFramingIdentityAndLifetime(t *testing.T) {
	for _, prefixImage := range []bool{false, true} {
		t.Run(strconv.FormatBool(prefixImage), func(t *testing.T) {
			dir := t.TempDir()
			reportFile := filepath.Join(dir, "report.json")
			release := filepath.Join(dir, "release")
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
			sentinel, err := windows.CreateEvent(&sa, 1, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(sentinel)
			env := append(os.Environ(), "ODDWARD_LAUNCH_HELPER=1", "SYNTHETIC_UNICODE=测试环境", "SYNTHETIC_REPORT="+reportFile, "SYNTHETIC_RELEASE="+release, "SYNTHETIC_HANDLE="+strconv.FormatUint(uint64(sentinel), 10))
			command := RequiredArguments + ` -name="two words"`
			game, err := startProcess(context.Background(), LaunchPlan{Image: exe, CommandLine: command, Directory: dir, Environment: env}, Options{IncludeImage: prefixImage})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = os.WriteFile(release, nil, 0600)
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				_, _ = game.Wait(ctx)
				_ = game.Close()
			}()
			logs, err := createPrivateLogs(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			game.logs = logs
			gateName := `Local\Oddward.Launch.Lifetime.` + filepath.Base(dir) + strconv.FormatBool(prefixImage)
			game.gate, err = acquireInstance(gateName)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(logs.path, "sdk.log"), "synthetic raw log")
			id := game.Identity()
			if id.PID == 0 || id.CreationTime == 0 {
				t.Fatalf("identity %+v", id)
			}
			var created, end, kernel, user windows.Filetime
			if err := windows.GetProcessTimes(game.handle, &created, &end, &kernel, &user); err != nil {
				t.Fatal(err)
			}
			if id.CreationTime != uint64(created.HighDateTime)<<32|uint64(created.LowDateTime) {
				t.Fatal("creation time mismatch")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := game.Wait(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel: %v", err)
			}
			if err := game.Close(); !errors.Is(err, ErrGameRunning) {
				t.Fatalf("early close: %v", err)
			}
			if second, err := acquireInstance(gateName); !errors.Is(err, ErrHostRunning) {
				if second != nil {
					_ = second.Close()
				}
				t.Fatalf("early Close released mutex: %v", err)
			}
			if _, err := os.Stat(logs.path); err != nil {
				t.Fatalf("early Close removed logs: %v", err)
			}
			var report helperReport
			deadline := time.Now().Add(10 * time.Second)
			for {
				b, err := os.ReadFile(reportFile)
				if err == nil && json.Unmarshal(b, &report) == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("helper report timeout")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if prefixImage {
				command = `"` + exe + `" ` + command
				if len(report.Args) != 4 || report.Args[0] != exe || report.Args[1] != strings.Fields(RequiredArguments)[0] {
					t.Fatalf("standard framing lost game map argument: %q", report.Args)
				}
			}
			if report.CommandLine != command || report.Directory != dir || report.Environment != "测试环境" {
				t.Fatalf("helper inputs %+v", report)
			}
			for _, kind := range report.StdTypes {
				if kind != windows.FILE_TYPE_CHAR {
					t.Fatalf("stdio not NUL: %+v", report)
				}
			}
			status, err := windows.WaitForSingleObject(sentinel, 0)
			if err != nil || status != uint32(windows.WAIT_TIMEOUT) || report.SentinelSignaled {
				t.Fatalf("unrelated handle inherited: %d %v %+v", status, err, report)
			}
			if err := os.WriteFile(release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			code, err := game.Wait(ctx)
			if err != nil || code != 23 {
				t.Fatalf("exit: %d %v", code, err)
			}
			if err := game.Close(); err != nil {
				t.Fatal(err)
			}
			if err := game.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(logs.path); !os.IsNotExist(err) {
				t.Fatalf("raw logs survive exit: %v", err)
			}
			again, err := acquireInstance(gateName)
			if err != nil {
				t.Fatal(err)
			}
			_ = again.Close()
		})
	}
}

func TestPrepareGateAndFailedStartCleanup(t *testing.T) {
	f := newFakeInstall(t)
	ctx := context.Background()
	name := `Local\Oddward.Launch.Prepare.` + filepath.Base(t.TempDir())
	p, err := prepare(ctx, f.root, syntheticProfile, func(context.Context) error { return nil }, name)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.Info().Profile.Name != "synthetic" {
		t.Fatal("prepared info missing")
	}
	if _, err := prepare(ctx, f.root, syntheticProfile, func(context.Context) error { return nil }, name); !errors.Is(err, ErrHostRunning) {
		t.Fatalf("mutex: %v", err)
	}
	// Reject invalid inputs before making log directories or invoking CreateProcess.
	if game, err := p.Start(ctx, Options{PipeName: "invalid\\name"}); err == nil || game != nil {
		t.Fatalf("bad options: %v %v", game, err)
	}
	// Modifying an approved DLL after preparation must invalidate the authorization.
	writeFile(t, f.bridge, "changed")
	if game, err := p.Start(ctx, Options{PipeName: "synthetic"}); !errors.Is(err, ErrUnsupportedSDK) || game != nil {
		t.Fatalf("stale gate: %v %v", game, err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Start(ctx, Options{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed preparation: %v", err)
	}
	if _, err := prepare(ctx, f.root, syntheticProfile, func(context.Context) error { return ErrClientRunning }, name); !errors.Is(err, ErrClientRunning) {
		t.Fatalf("running client: %v", err)
	}
}

func TestStartFailureRemovesPrivateLogs(t *testing.T) {
	f := newFakeInstall(t)
	p, err := prepare(context.Background(), f.root, syntheticProfile, func(context.Context) error { return nil }, `Local\Oddward.Launch.Failure.`+filepath.Base(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	parent := t.TempDir()
	p.logParent = parent
	// The synthetic placeholder is deliberately not a valid executable.
	if game, err := p.Start(context.Background(), Options{PipeName: "synthetic"}); err == nil || game != nil {
		t.Fatalf("expected CreateProcess failure %v %v", game, err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed start retained %d log directories", len(entries))
	}
}

var _ = strings.EqualFold
