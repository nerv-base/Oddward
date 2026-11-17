// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nerv-base/Oddward/internal/cnlogin/accounts"
	"github.com/nerv-base/Oddward/internal/launcher"
)

type nativeSessionFake struct {
	code    uint32
	err     error
	closed  int
	waiting bool
}

func (f *nativeSessionFake) Wait(ctx context.Context) (uint32, error) {
	f.waiting = true
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return f.code, f.err
}

func (f *nativeSessionFake) Close() error {
	f.closed++
	return nil
}

func TestNativeBackendRejectsAMissingLoginWindowBeforeInspectingTheGame(t *testing.T) {
	backend := NativeManagedBackend{Accounts: &accounts.Store{}}
	_, err := backend.Prepare(context.Background(), `C:\games\NTE`, nil)
	if err == nil || !strings.Contains(err.Error(), "登录窗口组件缺失") {
		t.Fatalf("prepare error=%v want a missing login window", err)
	}
}

func TestNativeSessionMapsAGameExitCodeToTheLauncherError(t *testing.T) {
	fake := &nativeSessionFake{code: 3}
	session := nativeSession{session: fake}
	if session.Notices() != nil {
		t.Fatal("native path must not drive the launcher login panel")
	}
	err := session.Wait(context.Background())
	var exit *launcher.GameExitError
	if !errors.As(err, &exit) || exit.Code != 3 {
		t.Fatalf("wait error=%v want GameExitError code 3", err)
	}
	if !fake.waiting {
		t.Fatal("Wait was not forwarded")
	}
}

func TestNativeSessionReportsZeroExitAndClosesOnce(t *testing.T) {
	fake := &nativeSessionFake{}
	session := nativeSession{session: fake}
	if err := session.Wait(context.Background()); err != nil {
		t.Fatalf("zero exit: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if fake.closed != 1 {
		t.Fatalf("closes=%d want 1", fake.closed)
	}
}

func TestNativeSessionLoginPanelCallsAreRejected(t *testing.T) {
	session := nativeSession{session: &nativeSessionFake{}}
	if err := session.SendCode(context.Background(), "13800138000"); !errors.Is(err, errNativeLogin) {
		t.Fatalf("SendCode error=%v", err)
	}
	if err := session.LoginPhone(context.Background(), "13800138000", "123456", false); !errors.Is(err, errNativeLogin) {
		t.Fatalf("LoginPhone error=%v", err)
	}
	if err := session.LoginSaved(context.Background(), "abc"); !errors.Is(err, errNativeLogin) {
		t.Fatalf("LoginSaved error=%v", err)
	}
	if err := session.Forget(context.Background(), "abc"); !errors.Is(err, errNativeLogin) {
		t.Fatalf("Forget error=%v", err)
	}
}
