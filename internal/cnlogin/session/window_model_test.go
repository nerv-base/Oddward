// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package session

import (
	"strconv"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/cnlogin/loginui"
)

func TestEveryLoginScreenKeepsBoundGameIdentity(t *testing.T) {
	rig := newRig(t, newFakeAccounts(), testPreLoginOK)
	rig.register(t)
	ui := rig.openLogin(t)
	check := func(m loginui.Model) {
		t.Helper()
		identity := rig.game.Identity()
		if m.GameProcessID != identity.PID || m.GameCreationTime != strconv.FormatUint(identity.CreationTime, 10) {
			t.Fatalf("login window %q lost bound game identity: pid=%d creation=%q", m.Screen, m.GameProcessID, m.GameCreationTime)
		}
	}
	check(ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "notice" }))
	ui.events <- loginui.Event{Kind: "acknowledge"}
	check(ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" }))
	ui.events <- loginui.Event{Kind: "help"}
	check(ui.waitFor(t, func(m loginui.Model) bool { return m.Status != "" }))
	ui.events <- loginui.Event{Kind: "cancel"}
	select {
	case <-ui.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("login window did not close")
	}
	reopened := rig.openLogin(t)
	check(reopened.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" }))
}
