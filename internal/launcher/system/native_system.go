// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/nerv-base/Oddward/internal/cnlogin/accounts"
	"github.com/nerv-base/Oddward/internal/cnlogin/broker"
	"github.com/nerv-base/Oddward/internal/cnlogin/elevatedipc"
	"github.com/nerv-base/Oddward/internal/cnlogin/launch"
	loginuinative "github.com/nerv-base/Oddward/internal/cnlogin/loginui/native"
	"github.com/nerv-base/Oddward/internal/cnlogin/session"
	"github.com/nerv-base/Oddward/internal/gamelocate"
	"github.com/nerv-base/Oddward/internal/launcher"
)

// RunNativeHost dispatches the restricted SDK host before cmd/launcher opens
// account stores or initializes MyGo. It performs no work for ordinary UI args.
func RunNativeHost(ctx context.Context, args []string) (bool, error) {
	if len(args) == 0 || args[0] != elevatedipc.Mode {
		return false, nil
	}
	return true, broker.Run(ctx, args[1:])
}

// RunLoginUI dispatches the isolated MyGo Native UI login window / welcome
// banner helper before cmd/launcher opens databases, account stores or the main
// launcher window. It performs no work for ordinary launcher args.
func RunLoginUI(args []string) (bool, int) {
	return loginuinative.Run(args)
}

// NativeManagedBackend starts the game through the native CN login flow. The
// login window runs in an isolated child process (by default the same launcher
// image invoked with loginui.Mode); the main launcher window never draws a
// login form for this path.
type NativeManagedBackend struct {
	// ResolveLoginUI selects the helper and its fixed role arguments when the
	// user starts the game. A missing optional helper is a preparation error.
	ResolveLoginUI func() (string, []string, error)
	// Accounts is the saved-account store. It is required.
	Accounts *accounts.Store
	// HTTPClient is the launcher's shared transport.
	HTTPClient *http.Client
	// LauncherVersion is reported to the login service.
	LauncherVersion string
	Downloads       *GameDownloadBackend
}

func (b NativeManagedBackend) Locate(ctx context.Context, path string) (string, error) {
	found, err := gamelocate.Inspect(ctx, path)
	if err != nil {
		return "", err
	}
	return found.Root, nil
}

func (b NativeManagedBackend) Prepare(ctx context.Context, root string, progress func(launcher.ManagedTransfer)) (launcher.GamePlan, error) {
	if b.Accounts == nil {
		return nil, errors.New("账号存储不可用，请检查应用数据目录权限")
	}
	var (
		loginUI     string
		loginUIArgs []string
		err         error
	)
	if b.ResolveLoginUI != nil {
		loginUI, loginUIArgs, err = b.ResolveLoginUI()
		if err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(loginUI) == "" {
		return nil, errors.New("登录窗口组件缺失，请重新安装启动器")
	}
	if info, err := os.Stat(loginUI); err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("登录窗口组件缺失，请重新安装启动器")
	}
	found, err := gamelocate.Inspect(ctx, root)
	if err != nil {
		return nil, err
	}
	if b.Downloads != nil {
		if err := b.Downloads.checkLaunch(ctx, found.Root); err != nil {
			return nil, errors.New(downloadMessage(err))
		}
	}
	plan, err := session.Prepare(ctx, session.Config{
		Root:            found.Root,
		ElevatedHost:    true,
		LoginUI:         loginUI,
		LoginUIArgs:     loginUIArgs,
		Accounts:        b.Accounts,
		HTTPClient:      b.HTTPClient,
		LauncherVersion: b.LauncherVersion,
	})
	if err != nil {
		return nil, nativePrepareError(err)
	}
	return nativePlan{plan: plan, downloads: b.Downloads, root: found.Root}, nil
}

// nativePrepareError turns launch preconditions into the short sentences the
// launcher can show, without exposing internal sentinel values.
func nativePrepareError(err error) error {
	switch {
	case errors.Is(err, broker.ErrUnobserved):
		return launcher.ErrGameUnobserved
	case errors.Is(err, launch.ErrFilesMissing):
		return errors.New("游戏文件不完整，请先通过官方启动器修复游戏")
	case errors.Is(err, launch.ErrClientRunning), errors.Is(err, launch.ErrHostRunning):
		return errors.New("游戏或登录组件仍在运行，请先退出")
	case errors.Is(err, launch.ErrUnsupportedSDK):
		return errors.New("当前游戏版本暂不支持原生登录，请使用官方启动器")
	default:
		return err
	}
}

type nativePlan struct {
	plan      *session.Plan
	downloads *GameDownloadBackend
	root      string
}

func (p nativePlan) Summary() string { return p.plan.Summary() }

func (p nativePlan) Close() error { return p.plan.Close() }

func (p nativePlan) Start(ctx context.Context) (launcher.GameSession, error) {
	if p.downloads != nil {
		lock, err := acquireDownloadMutation(p.root)
		if err != nil {
			return nil, err
		}
		defer lock.Close()
		if err := p.downloads.checkLaunch(ctx, p.root); err != nil {
			return nil, errors.New(downloadMessage(err))
		}
	}
	started, err := p.plan.Start(ctx)
	if err != nil {
		return nil, nativePrepareError(err)
	}
	if started == nil {
		return nil, launcher.ErrGameUnobserved
	}
	return nativeSession{session: started}, nil
}

// nativeSession adapts the native login session to the game seam. The login
// window belongs to the session, so the launcher's own login panel stays off.
type nativeSession struct{ session nativeSessionAPI }

// nativeSessionAPI is the part of session.Session the game seam needs.
type nativeSessionAPI interface {
	Wait(context.Context) (uint32, error)
	Close() error
}

var errNativeLogin = errors.New("登录由原生登录窗口处理")

func (s nativeSession) Notices() <-chan launcher.GameNotice { return nil }

func (s nativeSession) Wait(ctx context.Context) error {
	code, err := s.session.Wait(ctx)
	if err != nil {
		return err
	}
	if code != 0 {
		return &launcher.GameExitError{Code: code}
	}
	return nil
}

func (s nativeSession) Close() error { return s.session.Close() }

func (nativeSession) SendCode(context.Context, string) error { return errNativeLogin }
func (nativeSession) LoginPhone(context.Context, string, string, bool) error {
	return errNativeLogin
}
func (nativeSession) LoginSaved(context.Context, string) error { return errNativeLogin }
func (nativeSession) Forget(context.Context, string) error     { return errNativeLogin }
