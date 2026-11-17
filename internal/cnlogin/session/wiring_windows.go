// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package session

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/nerv-base/Oddward/internal/cnlogin/bootcfg"
	"github.com/nerv-base/Oddward/internal/cnlogin/cnproto"
	"github.com/nerv-base/Oddward/internal/cnlogin/laohu"
	"github.com/nerv-base/Oddward/internal/cnlogin/launch"
	"github.com/nerv-base/Oddward/internal/cnlogin/sdkhost"
)

const (
	// bootConfigRelativePath is the Qt resource holding the CN login
	// configuration. It is named after the application id (1289).
	bootConfigRelativePath = "NTELauncher/ResData/1289.dat"
	maxBootConfigSize      = 64 << 20
)

// loadBootConfig reads and decrypts the boot configuration of root with the
// app-key tail the game sent during SDK registration.
func loadBootConfig(ctx context.Context, root string, key [16]byte) (laohu.Config, error) {
	if err := ctx.Err(); err != nil {
		return laohu.Config{}, err
	}
	path := filepath.Join(root, filepath.FromSlash(bootConfigRelativePath))
	file, err := os.Open(path)
	if err != nil {
		return laohu.Config{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return laohu.Config{}, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxBootConfigSize {
		return laohu.Config{}, fmt.Errorf("cnlogin/session: boot configuration has an unexpected size")
	}
	resource := make([]byte, info.Size())
	if _, err := io.ReadFull(file, resource); err != nil {
		return laohu.Config{}, err
	}
	defer clear(resource)
	plain, err := bootcfg.Decode(resource, key)
	if err != nil {
		return laohu.Config{}, err
	}
	defer clear(plain)
	return laohu.ParseConfig(plain)
}

// installedVersion reports the game version read from the install metadata, or
// the launcher version when that optional metadata is absent. The login
// service only accepts two-to-four-part numeric versions.
func (p *Plan) installedVersion() string {
	if version := p.info.InstalledVersion; version != "" {
		return version
	}
	version, err := launch.NormalizeVersion(p.cfg.LauncherVersion)
	if err != nil {
		return "0.0.0.0"
	}
	return version
}

// osVersion reports the operating system to the login service.
func (p *Plan) osVersion() string {
	if p.cfg.OSVersion != "" {
		return p.cfg.OSVersion
	}
	return "Microsoft Windows NT 10.0"
}

// hostAdapter adapts *sdkhost.Host to the Host seam and converts its events.
type hostAdapter struct {
	host   *sdkhost.Host
	events chan HostEvent
	done   chan struct{}
	once   sync.Once
}

func newHostAdapter(host *sdkhost.Host) *hostAdapter {
	a := &hostAdapter{host: host, events: make(chan HostEvent), done: make(chan struct{})}
	go a.forward()
	return a
}

func (a *hostAdapter) forward() {
	defer close(a.events)
	for event := range a.host.Events() {
		out := HostEvent{Kind: hostKind(event.Kind), Query: event.Query, Err: event.Err}
		if event.Registration != nil {
			tail, err := event.Registration.AppKeyTail()
			var preLogin [16]byte
			if err == nil {
				preLogin, err = event.Registration.PreLoginKey()
			}
			event.Registration.Zero()
			if err != nil {
				out = HostEvent{Kind: HostFailure, Err: err}
			} else {
				out.Key = laohu.NewDerivedAppKey(tail, preLogin)
				out.ConfigKey = tail
			}
		}
		select {
		case a.events <- out:
		case <-a.done:
			return
		}
	}
}

func hostKind(kind sdkhost.EventKind) HostEventKind {
	switch kind {
	case sdkhost.EventRegistration:
		return HostRegistered
	case sdkhost.EventQuery:
		return HostQuery
	case sdkhost.EventLoginRequested:
		return HostLoginRequested
	case sdkhost.EventLogout:
		return HostLogout
	default:
		return HostFailure
	}
}

func (a *hostAdapter) PipeName() string { return a.host.PipeName() }

func (a *hostAdapter) BindGame(pid uint32, createdAt uint64) error {
	return a.host.BindGame(pid, createdAt)
}

func (a *hostAdapter) Events() <-chan HostEvent { return a.events }

func (a *hostAdapter) SendLogin(ctx context.Context, id cnproto.LoginIdentity) error {
	return a.host.SendLogin(ctx, id)
}

func (a *hostAdapter) Reply(ctx context.Context, reply cnproto.PreLoginReply) error {
	return a.host.Reply(ctx, reply)
}

func (a *hostAdapter) CompleteLoginRequest() { a.host.CompleteLoginRequest() }

func (a *hostAdapter) Close() error {
	a.once.Do(func() { close(a.done) })
	return a.host.Close()
}
