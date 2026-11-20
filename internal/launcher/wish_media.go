// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/nerv-base/Oddward/internal/gacha"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// wishArchives and all its contained slices/maps belong only to the UI thread.
// Workers transfer ownership through out and never read this projection.
type wishArchives struct {
	started bool
	roles   []string
	archive gacha.Archive
	stats   gacha.Stats
	catalog gacha.Catalog
	out     chan wishArchiveResult
}

type wishArchiveResult struct {
	effect  state.Effect
	roles   []string
	archive gacha.Archive
	catalog gacha.Catalog
	report  WishImportReport
	path    string
	err     error
}

func wishFailure(err error) state.WishFailureKind {
	switch {
	case errors.Is(err, gacha.ErrCorrupt):
		return state.WishFailureCorrupt
	case errors.Is(err, gacha.ErrRoleMismatch):
		return state.WishFailureRoleMismatch
	case errors.Is(err, gacha.ErrUnknownFormat):
		return state.WishFailureFormat
	default:
		return state.WishFailureIO
	}
}

func (s *Shell) applyWishEffect(f state.Effect) bool {
	switch f.Kind {
	case state.WishList, state.WishLoad, state.WishDelete, state.WishPickImport, state.WishImport, state.WishPickExport, state.WishExport:
	default:
		return false
	}
	backend, ctx, out, wake := s.WishBackend, s.ctx, s.wish.out, s.RequestFrame
	pickImport, pickExport := s.PickWishImport, s.PickWishExport
	go func() {
		result := wishArchiveResult{effect: f}
		if err := ctx.Err(); err != nil {
			return
		}
		switch f.Kind {
		case state.WishPickImport:
			if pickImport != nil {
				result.path, result.err = pickImport(ctx)
			}
		case state.WishPickExport:
			if pickExport != nil {
				result.path, result.err = pickExport(ctx, wishSuggestedFilename(f.RoleID, f.WishFormat), f.WishFormat)
			}
		default:
			if backend == nil {
				result.err = errors.New("local wish archive backend unavailable")
				break
			}
			switch f.Kind {
			case state.WishList:
				result.roles, result.err = backend.List(ctx)
			case state.WishLoad:
				result.archive, result.err = backend.Load(ctx, f.RoleID)
				if result.err == nil {
					// Names are optional; a missing or corrupt catalog only falls
					// back to built-in names and raw ids.
					result.catalog, _ = backend.Catalog(ctx)
				}
			case state.WishDelete:
				result.err = backend.Delete(ctx, f.RoleID)
			case state.WishImport:
				result.report, result.err = backend.Import(ctx, f.Path, f.RoleID, f.Confirmed)
			case state.WishExport:
				result.err = backend.Export(ctx, f.Path, f.RoleID, f.WishFormat)
			}
		}
		if ctx.Err() != nil || errors.Is(result.err, context.Canceled) {
			return
		}
		select {
		case out <- result:
			if wake != nil && ctx.Err() == nil {
				wake()
			}
		case <-ctx.Done():
		}
	}()
	return true
}

func wishSuggestedFilename(roleID string, format state.WishFormat) string {
	prefix := "oddward-gacha"
	if format == state.WishNTEUID {
		prefix = "nteuid-gacha"
	}
	return prefix + "-" + roleID + ".json"
}

func (s *Shell) syncWish() {
	s.syncWishLink()
	for {
		select {
		case r := <-s.wish.out:
			if s.Model.Closed || r.effect.Token != s.Model.Wish.Token {
				continue
			}
			e := state.Event{Token: r.effect.Token, Path: r.path, WishRoleID: r.effect.RoleID, WishFormat: r.effect.WishFormat}
			if r.err != nil {
				e.Kind, e.WishFailure, e.Message = state.WishOperationFailed, wishFailure(r.err), state.BriefError(r.err)
			} else {
				switch r.effect.Kind {
				case state.WishList:
					s.wish.roles = r.roles
					e.Kind, e.WishCount = state.WishListed, len(r.roles)
					e.WishRoleID = ""
					if len(r.roles) > 0 {
						e.WishRoleID = r.roles[0]
						for _, role := range r.roles {
							if role == s.Model.Wish.RoleID {
								e.WishRoleID = role
								break
							}
						}
					} else {
						s.wish.archive, s.wish.stats = gacha.Archive{}, gacha.Stats{}
					}
				case state.WishLoad:
					s.wish.archive = r.archive
					s.wish.stats = gacha.Statistics(r.archive)
					s.wish.catalog = r.catalog
					e.Kind, e.WishRoleID, e.WishCount = state.WishLoaded, r.archive.RoleID, len(r.archive.Records)
				case state.WishDelete:
					s.wish.archive, s.wish.stats = gacha.Archive{}, gacha.Stats{}
					e.Kind = state.WishDeleted
				case state.WishPickImport:
					e.Kind = state.WishImportPicked
				case state.WishPickExport:
					e.Kind = state.WishExportPicked
				case state.WishExport:
					e.Kind = state.WishExported
				case state.WishImport:
					report := r.report
					e.Path = r.effect.Path
					e.WishRoleID, e.WishCount = report.Archive.RoleID, len(report.Archive.Records)
					e.WishNewCount, e.WishOutside, e.WishConflicts = report.Report.Added, report.Report.Missing, report.Report.Conflicts
					if report.NeedsConfirmation {
						e.Kind = state.WishImportNeedsConfirmation
					} else {
						e.Kind = state.WishImported
						s.wish.archive, s.wish.stats = report.Archive, gacha.Statistics(report.Archive)
						if !containsWishRole(s.wish.roles, report.Archive.RoleID) {
							s.wish.roles = append(s.wish.roles, report.Archive.RoleID)
							sort.Strings(s.wish.roles)
						}
					}
				}
			}
			s.Dispatch(e)
			if e.Kind == state.WishLoaded || e.Kind == state.WishImported {
				// The archive changed the visible column order: drop what the
				// previous one queued, then warm the new first screen before
				// the paint asks for it.
				s.resetWishIcons()
				s.prefetchWishIcons(s.wish.archive)
			}
		default:
			if !s.wish.started && s.Model.Page == state.WishAnalysis && s.Model.Overlay == state.NoOverlay && s.Model.Wish.Phase == state.WishIdle {
				s.wish.started = true
				if s.WishBackend != nil {
					s.Dispatch(state.Event{Kind: state.WishRequestList})
				}
			}
			return
		}
	}
}

func containsWishRole(roles []string, roleID string) bool {
	for _, role := range roles {
		if role == roleID {
			return true
		}
	}
	return false
}

func (s *Shell) wishProjection() wishPageData {
	w := s.Model.Wish
	data := wishPageData{
		Tajiduo: s.wishTajiduoProjection(),
		Roles:   s.wish.roles, RoleID: w.RoleID, Archive: s.wish.archive, Stats: s.wish.stats, Catalog: s.wish.catalog, Location: s.WishLocation, Now: s.now,
		Loading: w.Phase == state.WishLoading, Busy: w.Busy, Failed: w.Phase == state.WishFailed, Corrupt: w.Failure == state.WishFailureCorrupt,
		NewCount: w.NewCount, Outside: w.Outside, Conflicts: w.Conflicts, ImportReported: w.ImportReported,
		Image:    s.wishIcon,
		OnRole:   func(role string) { s.Dispatch(state.Event{Kind: state.WishSelectRole, WishRoleID: role}) },
		OnImport: func() { s.Dispatch(state.Event{Kind: state.WishRequestImport}) },
		OnExport: func(nteuid bool) {
			format := state.WishOddward
			if nteuid {
				format = state.WishNTEUID
			}
			s.Dispatch(state.Event{Kind: state.WishRequestExport, WishFormat: format})
		},
		OnDelete: func() { s.Dispatch(state.Event{Kind: state.WishRequestDelete, WishCount: len(s.wish.archive.Records)}) },
		OnRetry: func() {
			if w.RoleID != "" {
				s.Dispatch(state.Event{Kind: state.WishSelectRole, WishRoleID: w.RoleID})
			} else {
				s.Dispatch(state.Event{Kind: state.WishRequestList})
			}
		},
	}
	if data.Archive.RoleID != w.RoleID {
		data.Archive, data.Stats = gacha.Archive{}, gacha.Stats{}
	}
	switch w.Failure {
	case state.WishFailureRoleMismatch:
		data.FailureTitle, data.FailureBody = "角色档案不一致", "导入文件的角色与目标档案不一致。原档案已保留，请选择对应角色的 JSON。"
	case state.WishFailureFormat:
		data.FailureTitle, data.FailureBody = "无法识别导入格式", "请选择 Oddward 或 NTEUID 格式的 JSON。原档案已保留。"
	}
	if w.Confirm.Kind != state.WishConfirmNone {
		confirm := &wishPageConfirmation{
			OnAccept: func() { s.Dispatch(state.Event{Kind: state.WishAcceptConfirm}) },
			OnCancel: func() { s.Dispatch(state.Event{Kind: state.WishCancelConfirm}) },
		}
		switch w.Confirm.Kind {
		case state.WishConfirmDelete:
			confirm.Title, confirm.Accept = "删除本地记录", "删除记录"
			confirm.Body = fmt.Sprintf("将删除角色 %s 的 %d 条本地记录。此操作只删除启动器中的抽卡档案。", w.Confirm.RoleID, w.Confirm.Count)
			if w.Failure == state.WishFailureCorrupt {
				confirm.Body = fmt.Sprintf("角色 %s 的本地档案已损坏，记录条数无法读取。确认删除原文件？", w.Confirm.RoleID)
			}
		case state.WishConfirmImport:
			confirm.Title, confirm.Accept = "新建角色档案", "新建并导入"
			confirm.Body = fmt.Sprintf("导入文件包含角色 %s。将为该角色新建本地档案，导入 %d 条记录。", w.Confirm.RoleID, w.Confirm.Count)
		case state.WishConfirmUnlink:
			confirm.Title, confirm.Accept = "解除塔吉多关联", "解除关联"
			confirm.Body = "解除后将清除塔吉多关联，本地抽卡档案会保留。"
		case state.WishConfirmLossy:
			confirm.Title, confirm.Accept = "导出 NTEUID 互通格式", "继续导出"
			confirm.Body = "这个格式不含 luck 字段，导出时将丢失该字段及档案快照。Oddward 完整格式可保留全部信息。"
		}
		data.Confirm = confirm
	}
	return data
}
