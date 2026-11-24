// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package broker

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nerv-base/Oddward/internal/cnlogin/elevatedipc"
	"github.com/nerv-base/Oddward/internal/cnlogin/launch"
	"github.com/nerv-base/Oddward/internal/cnlogin/sdkhost"
	"github.com/nerv-base/Oddward/internal/process"
)

// Start authenticates a same-executable elevated host and asks it to validate
// root and start its fixed game image. ctx only bounds initialization. If a
// start request was sent without an observed result, ErrUnobserved means the
// game may have started; the caller must not claim that it did not.
func Start(ctx context.Context, root string) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validRoot(root) {
		return nil, errProtocol
	}
	conn, err := elevatedipc.Start(ctx)
	if err != nil {
		return nil, err
	}
	return startClient(ctx, conn, root)
}
func validRoot(root string) bool {
	return filepath.IsAbs(root) && len(root) <= 32767 && !strings.ContainsRune(root, 0)
}
func startClient(ctx context.Context, conn io.ReadWriteCloser, root string) (*Client, error) {
	c := newClient(conn)
	if !validRoot(root) {
		_ = c.Close()
		return nil, errProtocol
	}
	if err := c.call(ctx, "start", startPayload{Root: root}); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// Run is called only by cmd/launcher's internal elevatedipc.Mode branch, before
// UI initialization. args are the arguments after Mode. It owns no accounts,
// HTTP client, login UI process, or cross-process store. Once a game exists, Run
// retains the SDK host and game resources until its real exit even after EOF.
func Run(ctx context.Context, args []string) error {
	conn, err := elevatedipc.Accept(ctx, args)
	if err != nil {
		return err
	}
	// Accept has verified these exact arguments against the pipe peer's OS
	// identity. Parsing argv alone never authorizes an overlay request.
	parent, err := acceptedParent(args)
	if err != nil {
		_ = conn.Close()
		return err
	}
	return serve(ctx, conn, func(ctx context.Context, root string) (*owned, error) {
		o, err := launchOwned(ctx, root)
		if o != nil && o.game != nil {
			game := o.game.Identity()
			o.bindOverlay = func(ctx context.Context, p overlayPayload) error {
				return bindOverlay(ctx, parent, game, p, nativeOverlaySystem{})
			}
		}
		return o, err
	})
}
func acceptedParent(args []string) (process.Identity, error) {
	if len(args) != 3 {
		return process.Identity{}, errProtocol
	}
	pid, err := strconv.ParseUint(args[1], 10, 32)
	if err != nil || pid == 0 {
		return process.Identity{}, errProtocol
	}
	creation, err := strconv.ParseUint(args[2], 10, 64)
	if err != nil || creation == 0 {
		return process.Identity{}, errProtocol
	}
	return process.Identity{PID: uint32(pid), CreationTime: creation}, nil
}
func launchOwned(ctx context.Context, root string) (*owned, error) {
	if !validRoot(root) {
		return nil, errProtocol
	}
	prepared, err := launch.Prepare(ctx, root)
	if err != nil {
		return nil, err
	}
	defer prepared.Close()
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	info := prepared.Info()
	host, err := sdkhost.New(ctx, sdkhost.Options{Profile: info.Profile, HostExecutable: executable})
	if err != nil {
		return nil, err
	}
	game, err := prepared.Start(ctx, launch.Options{IncludeImage: true, PipeName: host.PipeName(), ConnectedClientPID: host.ConnectedClientPID})
	if game == nil {
		_ = host.Close()
		if err == nil {
			err = errRemote
		}
		return nil, err
	}
	o := &owned{host: host, game: game, installedVersion: prepared.Info().InstalledVersion}
	if err == nil {
		id := game.Identity()
		err = host.BindGame(id.PID, id.CreationTime)
	}
	return o, err
}
