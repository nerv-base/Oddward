// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package elevatedipc

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func testListener(t *testing.T) (*conn, []string, identity) {
	t.Helper()
	local, err := identify(windows.CurrentProcess())
	if err != nil {
		t.Fatal(err)
	}
	// Each caller has its own test-specific pipe. The public Start path uses
	// crypto/rand and is exercised separately by syntheticLaunch.
	name := pipePrefix + strings.Repeat("0", 32)
	c, err := listen(name, local.sid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c, []string{name, strconv.FormatUint(uint64(local.pid), 10), strconv.FormatUint(local.creation, 10)}, local
}

func TestRejectsSameUserSameImageWrongClient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var rogue *conn
	c, err := start(ctx, func(exe string, args []string) (windows.Handle, error) {
		var err error
		rogue, err = dial(ctx, args[1]) // This PID is not the retained child's PID.
		if err != nil {
			return 0, err
		}
		return childLaunch(t, "reject")(exe, args)
	}, false)
	if c != nil {
		c.Close()
		t.Fatal("wrong peer accepted")
	}
	if !errors.Is(err, errIdentity) {
		t.Fatalf("wrong client: %v", err)
	}
	if rogue == nil {
		t.Fatal("rogue did not connect")
	}
	defer rogue.Close()
	var b [1]byte
	if n, err := rogue.Read(b[:]); n != 0 || err == nil {
		t.Fatalf("unverified client received readiness: %d, %v", n, err)
	}
}

func TestAcceptRejectsWrongServerPIDAndCreation(t *testing.T) {
	for _, field := range []int{1, 2} {
		t.Run(strconv.Itoa(field), func(t *testing.T) {
			_, args, _ := testListener(t)
			n, _ := strconv.ParseUint(args[field], 10, 64)
			args[field] = strconv.FormatUint(n+1, 10)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			c, err := accept(ctx, args, false)
			if c != nil {
				c.Close()
				t.Fatal("wrong server accepted")
			}
			if !errors.Is(err, errIdentity) {
				t.Fatalf("wrong server: %v", err)
			}
		})
	}
}

func TestPeerPrincipalRequiresSameUserSessionAndExecutable(t *testing.T) {
	local, err := identify(windows.CurrentProcess())
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if local.sid != user.User.Sid.String() || !samePrincipal(local, local) {
		t.Fatal("current user's real token did not match")
	}
	for _, change := range []func(*identity){
		func(id *identity) {
			id.sid = "S-1-5-18"
			if id.sid == local.sid {
				id.sid = "S-1-5-19"
			}
		},
		func(id *identity) { id.session++ },
		func(id *identity) { id.image += ".foreign" },
	} {
		other := local
		change(&other)
		if samePrincipal(other, local) {
			t.Fatal("different principal accepted")
		}
	}
}

func TestListenerIsPrivateAndFirstInstance(t *testing.T) {
	c, args, local := testListener(t)
	other, err := listen(args[0], local.sid)
	if other != nil {
		other.Close()
		t.Fatal("second server instance created")
	}
	if err == nil {
		t.Fatal("squatted pipe accepted")
	}
	sd, err := windows.GetSecurityInfo(c.h, windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("DACL is not protected: %v", err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount != 1 {
		t.Fatalf("private DACL: %v", err)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(acl, 0, &ace); err != nil {
		t.Fatal(err)
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if sid.String() != local.sid {
		t.Fatal("DACL permits another principal")
	}
}

func TestAcceptRejectsUntrustedArgumentsWithoutEcho(t *testing.T) {
	for _, args := range [][]string{nil, {"secret-argument", "1", "1"}, {pipePrefix + strings.Repeat("0", 32), "secret-argument", "1"}, {pipePrefix + strings.Repeat("0", 32), "1", "secret-argument"}} {
		c, err := Accept(context.Background(), args)
		if c != nil {
			c.Close()
			t.Fatal("invalid arguments accepted")
		}
		if err == nil || strings.Contains(err.Error(), "secret-argument") {
			t.Fatal("unsafe diagnostic")
		}
	}
}

func TestAcceptRequiresElevatedToken(t *testing.T) {
	if windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("runner already elevated; no token lowering or UAC in tests")
	}
	_, args, _ := testListener(t)
	c, err := Accept(context.Background(), args)
	if c != nil {
		c.Close()
		t.Fatal("unelevated host accepted")
	}
	if !errors.Is(err, errElevation) {
		t.Fatalf("elevation check: %v", err)
	}
}

func TestNoApplicationBytesBeforeBothPeersVerify(t *testing.T) {
	server, args, _ := testListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		c, err := accept(ctx, args, false)
		if c != nil {
			c.Close()
		}
		result <- err
	}()
	if err := server.connect(); err != nil {
		t.Fatal(err)
	}
	// Cancel while the authenticated client waits for server readiness.
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting client: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not interrupt handshake")
	}
	var b [1]byte
	if n, err := server.Read(b[:]); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("client emitted data before readiness: %d, %v", n, err)
	}
}
