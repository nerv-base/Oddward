// SPDX-License-Identifier: GPL-3.0-or-later
package launcher

import (
	"context"
	"errors"
	"fmt"
	"github.com/nerv-base/Oddward/internal/gacha"
	"github.com/nerv-base/Oddward/internal/launcher/state"
	"sort"
)

type wishLinkMedia struct {
	phone, code            string
	roles                  []WishLinkRole
	out                    chan wishLinkResult
	cancel                 context.CancelFunc
	statusPending, wasWish bool
	status                 string
}
type wishLinkResult struct {
	effect state.Effect
	status *WishLinkStatus
	roles  []WishLinkRole
	report WishImportReport
	err    error
}

func (s *Shell) applyWishLinkEffect(f state.Effect) bool {
	if f.Kind < state.TajiduoSendSMS || f.Kind > state.WishFetch {
		return false
	}
	if s.wishLink.cancel != nil {
		s.wishLink.cancel()
		s.wishLink.cancel = nil
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.wishLink.cancel = cancel
	backend, out, wake := s.WishLinkBackend, s.wishLink.out, s.RequestFrame
	phone, code := s.wishLink.phone, s.wishLink.code
	if f.Kind == state.TajiduoLogin {
		s.wishLink.code = ""
	}
	if f.Kind == state.TajiduoCancel {
		s.wishLink.phone, s.wishLink.code = "", ""
		s.wishLink.roles = nil
	}
	s.wishLink.status = ""
	go func() {
		defer cancel()
		r := wishLinkResult{effect: f}
		if backend == nil {
			r.err = WishLinkError{Kind: state.WishLinkStorage}
		} else {
			switch f.Kind {
			case state.TajiduoSendSMS:
				r.err = backend.SendSMS(ctx, f.Path, phone)
			case state.TajiduoLogin:
				r.err = backend.Login(ctx, f.Path, phone, code)
			case state.TajiduoLoginSaved:
				r.err = backend.LoginSaved(ctx, f.Path)
			case state.TajiduoRoles:
				r.roles, r.err = backend.Roles(ctx, f.Path)
			case state.TajiduoBindRole:
				r.err = backend.Bind(ctx, f.Path, f.RoleID)
			case state.TajiduoComplete:
				r.err = backend.Complete(ctx, f.Path, f.RoleID)
			case state.TajiduoCancel:
				r.err = backend.Cancel(ctx, f.Path)
			case state.TajiduoUnlink:
				r.err = backend.Unlink(ctx, f.Path)
			case state.WishFetch:
				r.report, r.err = backend.Fetch(ctx, f.Path, f.RoleID)
			}
		}
		if ctx.Err() != nil || errors.Is(r.err, context.Canceled) {
			return
		}
		select {
		case out <- r:
			if wake != nil && ctx.Err() == nil {
				wake()
			}
		case <-ctx.Done():
		}
	}()
	return true
}
func (s *Shell) beginWishLinkStatus() {
	if s.WishLinkBackend == nil || s.wishLink.statusPending {
		return
	}
	s.wishLink.statusPending = true
	backend, ctx, out, wake, token := s.WishLinkBackend, s.ctx, s.wishLink.out, s.RequestFrame, s.Model.Wish.Link.Token
	go func() {
		status, err := backend.Status(ctx)
		if ctx.Err() != nil {
			return
		}
		r := wishLinkResult{effect: state.Effect{Token: token}, status: &status, err: err}
		select {
		case out <- r:
			if wake != nil && ctx.Err() == nil {
				wake()
			}
		case <-ctx.Done():
		}
	}()
}
func (s *Shell) syncWishLink() {
	for {
		select {
		case r := <-s.wishLink.out:
			if r.status != nil {
				s.wishLink.statusPending = false
				if r.err == nil && r.effect.Token == s.Model.Wish.Link.Token {
					s.Dispatch(state.Event{Kind: state.WishLinkStatus, WishAccountID: r.status.AccountID, WishRoleID: r.status.RoleID, WishLinked: r.status.Linked, WishRelink: r.status.NeedsRelink})
				}
				continue
			}
			if s.Model.Closed || r.effect.Token != s.Model.Wish.Link.Token {
				continue
			}
			if r.effect.Kind == state.TajiduoCancel {
				continue
			}
			e := state.Event{Token: r.effect.Token, WishRoleID: r.effect.RoleID, Now: s.now}
			if r.err != nil {
				e.Kind, e.WishLinkFailure = state.WishLinkFailed, wishLinkErrorKind(r.err)
				if e.WishLinkFailure == state.WishLinkAuth {
					s.wishLink.phone, s.wishLink.code = "", ""
					s.wishLink.roles = nil
				}
			} else {
				switch r.effect.Kind {
				case state.TajiduoSendSMS:
					e.Kind = state.WishLinkSMSDone
				case state.TajiduoLogin, state.TajiduoLoginSaved:
					e.Kind = state.WishLinkLoginDone
				case state.TajiduoRoles:
					s.wishLink.roles = r.roles
					e.Kind = state.WishLinkRolesDone
				case state.TajiduoBindRole:
					e.Kind = state.WishLinkBound
				case state.TajiduoComplete:
					e.Kind = state.WishLinkCompleted
					s.wishLink.phone, s.wishLink.code = "", ""
					s.wishLink.roles = nil
					s.wishLink.status = "已关联塔吉多，点击更新记录获取最近约 6 个月的 S 记录"
				case state.TajiduoUnlink:
					e.Kind = state.WishLinkUnlinked
					s.wishLink.status = "已解除关联，本地档案已保留"
				case state.WishFetch:
					e.Kind = state.WishFetchDone
					e.WishRoleID = r.report.Archive.RoleID
					e.WishNewCount, e.WishOutside, e.WishConflicts = r.report.Report.Added, r.report.Report.Missing, r.report.Report.Conflicts
					s.wish.archive, s.wish.stats = r.report.Archive, gacha.Statistics(r.report.Archive)
					if !containsWishRole(s.wish.roles, e.WishRoleID) {
						s.wish.roles = append(s.wish.roles, e.WishRoleID)
						sort.Strings(s.wish.roles)
					}
					s.wishLink.status = fmt.Sprintf("更新完成 · 新增 %d 条", r.report.Report.Added)
				}
			}
			s.Dispatch(e)
		default:
			onWish := s.Model.Page == state.WishAnalysis
			if onWish && !s.wishLink.wasWish {
				s.beginWishLinkStatus()
			}
			s.wishLink.wasWish = onWish
			return
		}
	}
}
func (s *Shell) wishTajiduoProjection() wishTajiduo {
	l := s.Model.Wish.Link
	status := s.wishLink.status
	if l.Failure != state.WishLinkFailureNone {
		status = wishLinkFailureText(l.Failure)
	}
	mismatch := l.RoleID != "" && s.wish.archive.RoleID != "" && l.RoleID != s.wish.archive.RoleID
	if mismatch {
		status = "当前档案不是塔吉多绑定的角色，请先选择绑定角色"
	}
	return wishTajiduo{
		Linkable: l.AccountID != "", Linked: l.Phase == state.WishLinked, NeedsRelink: l.Phase == state.WishNeedsRelink,
		Fetching: l.Fetch == state.WishFetching, Cooldown: state.WishLinkCooldown(l.CooldownUntil, s.now), RoleID: l.RoleID, RoleMismatch: mismatch,
		Status: status, StatusWarn: l.Failure != state.WishLinkFailureNone || l.Phase == state.WishNeedsRelink || mismatch,
		OnUpdate: func() {
			kind := state.WishFetchRequest
			if l.Phase != state.WishLinked {
				kind = state.WishLinkStart
				s.wishLink.phone, s.wishLink.code = "", ""
				s.wishLink.roles = nil
			}
			s.Dispatch(state.Event{Kind: kind, Now: s.now})
		},
		OnUnlink: func() { s.Dispatch(state.Event{Kind: state.WishLinkUnlink}) },
	}
}
