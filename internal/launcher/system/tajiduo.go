// SPDX-License-Identifier: GPL-3.0-or-later
package system

import (
	"context"
	"errors"
	"github.com/nerv-base/Oddward/internal/checkin"
	"github.com/nerv-base/Oddward/internal/cnlogin/accounts"
	"github.com/nerv-base/Oddward/internal/gacha"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
	"github.com/nerv-base/Oddward/internal/tajiduo"
	"sync"
)

// TajiduoBackend serializes staged links and local attachment commits. Secrets
// stay here and inside Session; no raw error is returned across the UI boundary.
type TajiduoBackend struct {
	mu       sync.Mutex
	accounts *accounts.Store
	client   *tajiduo.Client
	archives *WishArchives
	checkin  *checkin.Store
	sessions map[string]*tajiduoLink
	devices  map[string]string
}
type tajiduoLink struct {
	session   *tajiduo.Session
	committed bool
	roles     []launcher.WishLinkRole
}

var _ launcher.WishLinkBackend = (*TajiduoBackend)(nil)

func NewTajiduoBackend(store *accounts.Store, client *tajiduo.Client, archives *WishArchives, records *checkin.Store) *TajiduoBackend {
	return &TajiduoBackend{accounts: store, client: client, archives: archives, checkin: records, sessions: map[string]*tajiduoLink{}, devices: map[string]string{}}
}
func tajiduoFailure(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	kind := state.WishLinkStorage
	switch {
	case errors.Is(err, tajiduo.ErrAuthExpired), errors.Is(err, tajiduo.ErrNeedsRelink):
		kind = state.WishLinkAuth
	case errors.Is(err, tajiduo.ErrProtocolChanged):
		kind = state.WishLinkProtocol
	case errors.Is(err, tajiduo.ErrChallengeRequired):
		kind = state.WishLinkChallenge
	case errors.Is(err, tajiduo.ErrNetwork):
		kind = state.WishLinkNetwork
	case errors.Is(err, tajiduo.ErrIdentityMismatch), errors.Is(err, accounts.ErrTajiduoMismatch):
		kind = state.WishLinkMismatch
	case errors.Is(err, tajiduo.ErrNoBoundRole):
		kind = state.WishLinkNoRole
	case errors.Is(err, tajiduo.ErrInvalidInput):
		kind = state.WishLinkInvalid
	}
	return launcher.WishLinkError{Kind: kind}
}
func (b *TajiduoBackend) current(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.accounts == nil || b.client == nil {
		return tajiduo.ErrInvalidInput
	}
	last, err := b.accounts.Last()
	if err != nil {
		return err
	}
	if id == "" || last != id {
		return tajiduo.ErrIdentityMismatch
	}
	return nil
}
func toAccountGrant(g tajiduo.Grant) accounts.TajiduoGrant {
	return accounts.TajiduoGrant{DeviceID: g.DeviceID, CenterUID: g.CenterUID, RefreshToken: g.RefreshToken, LaohuUserID: g.LaohuUserID, RoleID: g.RoleID}
}
func fromAccountGrant(g accounts.TajiduoGrant) tajiduo.Grant {
	return tajiduo.Grant{DeviceID: g.DeviceID, CenterUID: g.CenterUID, RefreshToken: g.RefreshToken, LaohuUserID: g.LaohuUserID, RoleID: g.RoleID}
}
func (b *TajiduoBackend) newLink(id string, g tajiduo.Grant, committed bool) *tajiduoLink {
	link := &tajiduoLink{committed: committed}
	link.session = tajiduo.NewSession(b.client, g, func(next tajiduo.Grant) error {
		if !link.committed {
			return nil
		}
		return b.accounts.UpdateTajiduo(id, toAccountGrant(next))
	})
	return link
}
func (b *TajiduoBackend) savedLink(id string) (*tajiduoLink, error) {
	if link := b.sessions[id]; link != nil {
		return link, nil
	}
	g, err := b.accounts.UnlockTajiduo(id)
	if err != nil {
		return nil, err
	}
	defer g.Wipe()
	link := b.newLink(id, fromAccountGrant(g), true)
	b.sessions[id] = link
	return link, nil
}
func (b *TajiduoBackend) Status(ctx context.Context) (launcher.WishLinkStatus, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out launcher.WishLinkStatus
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if b.accounts == nil {
		return out, nil
	}
	id, err := b.accounts.Last()
	if err != nil {
		return out, tajiduoFailure(err)
	}
	out.AccountID = id
	if id == "" {
		return out, nil
	}
	g, err := b.accounts.UnlockTajiduo(id)
	if errors.Is(err, accounts.ErrNoTajiduo) {
		return out, nil
	}
	if errors.Is(err, accounts.ErrCorrupt) {
		// A damaged attachment stays recoverable: offer a relink instead of
		// failing the whole status query, which would hide the entry point.
		out.NeedsRelink = true
		return out, nil
	}
	if err != nil {
		return out, tajiduoFailure(err)
	}
	defer g.Wipe()
	out.Linked, out.RoleID = true, g.RoleID
	if link := b.sessions[id]; link != nil {
		out.NeedsRelink = link.session.NeedsRelink()
	}
	return out, nil
}
func (b *TajiduoBackend) SendSMS(ctx context.Context, id, phone string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.current(ctx, id); err != nil {
		return tajiduoFailure(err)
	}
	return tajiduoFailure(b.client.SendSMS(ctx, phone))
}
func (b *TajiduoBackend) Login(ctx context.Context, id, phone, code string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.current(ctx, id); err != nil {
		return tajiduoFailure(err)
	}
	device := b.devices[id]
	old, err := b.accounts.UnlockTajiduo(id)
	if err == nil {
		device = old.DeviceID
		old.Wipe()
	} else if !errors.Is(err, accounts.ErrNoTajiduo) && !errors.Is(err, accounts.ErrCorrupt) {
		// A damaged attachment cannot supply a device id; relinking starts a
		// fresh one and replaces the attachment on completion.
		return tajiduoFailure(err)
	}
	if device == "" {
		device, err = accounts.NewTajiduoDeviceID()
		if err != nil {
			return tajiduoFailure(err)
		}
		b.devices[id] = device
	}
	g, err := b.client.LoginSMS(ctx, phone, code, device)
	if err != nil {
		return tajiduoFailure(err)
	}
	secret, err := b.accounts.Unlock(id)
	if err != nil {
		return tajiduoFailure(err)
	}
	defer secret.Wipe()
	if g.LaohuUserID != secret.ChannelUID {
		return tajiduoFailure(tajiduo.ErrIdentityMismatch)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	b.sessions[id] = b.newLink(id, g, false)
	return nil
}
func (b *TajiduoBackend) Roles(ctx context.Context, id string) ([]launcher.WishLinkRole, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.current(ctx, id); err != nil {
		return nil, tajiduoFailure(err)
	}
	link, err := b.savedLink(id)
	if err != nil {
		return nil, tajiduoFailure(err)
	}
	a, err := link.session.Access(ctx)
	if err != nil {
		return nil, tajiduoFailure(err)
	}
	list, err := b.client.Roles(ctx, a)
	if err != nil {
		return nil, tajiduoFailure(err)
	}
	roles := make([]launcher.WishLinkRole, 0, len(list.Roles))
	for _, role := range list.Roles {
		roles = append(roles, launcher.WishLinkRole{ID: role.ID, Name: role.Name, Bound: role.ID == list.BoundRoleID})
	}
	link.roles = roles
	return append([]launcher.WishLinkRole(nil), roles...), nil
}
func (b *TajiduoBackend) Bind(ctx context.Context, id, role string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.current(ctx, id); err != nil {
		return tajiduoFailure(err)
	}
	link := b.sessions[id]
	if link == nil || link.committed {
		return tajiduoFailure(tajiduo.ErrInvalidInput)
	}
	found := false
	for _, r := range link.roles {
		if r.ID == role {
			found = true
		}
	}
	if !found {
		return tajiduoFailure(tajiduo.ErrInvalidInput)
	}
	a, err := link.session.Access(ctx)
	if err != nil {
		return tajiduoFailure(err)
	}
	if err = b.client.BindRole(ctx, a, role); err != nil {
		return tajiduoFailure(err)
	}
	for i := range link.roles {
		link.roles[i].Bound = link.roles[i].ID == role
	}
	return nil
}
func (b *TajiduoBackend) Complete(ctx context.Context, id, role string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.current(ctx, id); err != nil {
		return tajiduoFailure(err)
	}
	link := b.sessions[id]
	if link == nil || link.committed {
		return tajiduoFailure(tajiduo.ErrInvalidInput)
	}
	bound := false
	for _, r := range link.roles {
		if r.ID == role && r.Bound {
			bound = true
		}
	}
	if !bound {
		return tajiduoFailure(tajiduo.ErrNoBoundRole)
	}
	g := link.session.Grant()
	g.RoleID = role
	if err := b.accounts.LinkTajiduo(id, toAccountGrant(g)); err != nil {
		return tajiduoFailure(err)
	}
	b.sessions[id] = b.newLink(id, g, true)
	return nil
}
func (b *TajiduoBackend) Cancel(ctx context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if link := b.sessions[id]; link != nil && !link.committed {
		delete(b.sessions, id)
	}
	return nil
}
func (b *TajiduoBackend) Unlink(ctx context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.current(ctx, id); err != nil {
		return tajiduoFailure(err)
	}
	if err := b.accounts.UnlinkTajiduo(id); err != nil {
		return tajiduoFailure(err)
	}
	delete(b.sessions, id)
	return nil
}
func (b *TajiduoBackend) Fetch(ctx context.Context, id, role string) (launcher.WishImportReport, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.current(ctx, id); err != nil {
		return launcher.WishImportReport{}, tajiduoFailure(err)
	}
	link, err := b.savedLink(id)
	if err != nil {
		return launcher.WishImportReport{}, tajiduoFailure(err)
	}
	if !link.committed {
		return launcher.WishImportReport{}, tajiduoFailure(tajiduo.ErrInvalidInput)
	}
	if role == "" || role != link.session.Grant().RoleID {
		return launcher.WishImportReport{}, launcher.WishLinkError{Kind: state.WishLinkRoleMismatch}
	}
	a, err := link.session.Access(ctx)
	if err != nil {
		return launcher.WishImportReport{}, tajiduoFailure(err)
	}
	snapshot, err := b.client.Gacha(ctx, a)
	if err != nil {
		return launcher.WishImportReport{}, tajiduoFailure(err)
	}
	if b.archives == nil {
		return launcher.WishImportReport{}, tajiduoFailure(tajiduo.ErrInvalidInput)
	}
	report, err := b.archives.MergeSnapshot(ctx, snapshot)
	if err != nil {
		return report, tajiduoFailure(err)
	}
	// Official names are a display supplement fetched after the records already
	// merged. A failure here must not fail the fetch or hide merged records.
	if catalog, catalogErr := b.client.Characters(ctx, a); catalogErr == nil && len(catalog.Items) > 0 {
		_ = b.archives.SaveCatalog(ctx, gacha.Catalog{FetchedAt: catalog.FetchedAt, Items: catalog.Items})
	}
	return report, nil
}
