// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"time"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

type ManagedTransfer struct {
	Stage             string
	Downloaded, Total int64
}
type ManagedPreferences struct{ GameRoot, RiskVersion string }

type ManagedBackend interface {
	Locate(context.Context, string) (string, error)
	Prepare(context.Context, string, func(ManagedTransfer)) (GamePlan, error)
}
type ManagedStore interface {
	Load(context.Context) (ManagedPreferences, error)
	Save(context.Context, ManagedPreferences) error
}
type managedResult struct {
	event   state.Event
	plan    GamePlan
	session GameSession
}

func (s *Shell) applyManagedEffect(f state.Effect) bool {
	switch f.Kind {
	case state.LoadManagedSettings:
		s.beginManagedLoad(f.Token)
	case state.PickManagedDirectory:
		s.beginManagedLocate(f.Token, f.Path, true)
	case state.InspectManagedDirectory:
		s.beginManagedLocate(f.Token, f.Path, false)
	case state.PrepareManagedLogin:
		s.beginManagedPrepare(f.Token)
	case state.LaunchManagedHost:
		s.beginManagedStart(f.Token)
	case state.CancelManagedWork:
		s.cancelManagedWork()
	case state.SaveManagedSettings:
		s.saveManagedSettings()
	default:
		return false
	}
	return true
}

func postManaged(ctx context.Context, out chan<- managedResult, wake func(), result managedResult) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case out <- result:
		if ctx.Err() == nil && wake != nil {
			wake()
		}
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Shell) cancelManagedWork() {
	if s.managedCancel != nil {
		s.managedCancel()
		s.managedCancel = nil
	}
	closeGamePlan(s.managedPlan)
	s.managedPlan = nil
	s.managedPlanRoot = ""
}

// closeGamePlan releases a prepared plan that will never be started. Plans
// that hold OS resources (the native login install lock) implement Close.
func closeGamePlan(plan GamePlan) {
	if plan == nil {
		return
	}
	if closer, ok := plan.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}

func (s *Shell) beginManagedLoad(token uint64) {
	s.cancelManagedWork()
	ctx, cancel := context.WithCancel(s.ctx)
	s.managedCancel = cancel
	store, override, out, wake := s.ManagedStore, s.InitialManagedRoot, s.managedOut, s.RequestFrame
	go func() {
		defer cancel()
		var value ManagedPreferences
		var err error
		if store != nil {
			value, err = store.Load(ctx)
		}
		e := state.Event{Kind: state.ManagedLoaded, Token: token, Path: value.GameRoot, Enabled: value.RiskVersion == state.ManagedConsentVersion}
		if override != "" {
			if !state.SameManagedRoot(value.GameRoot, override) {
				e.Enabled = false
			}
			e.Path = override
		}
		if err != nil {
			e.Kind = state.ManagedLoadFailed
			e.Message = "保存的游戏设置不可用，请重新定位。"
		}
		postManaged(ctx, out, wake, managedResult{event: e})
	}()
}

func (s *Shell) beginManagedLocate(token uint64, path string, pick bool) {
	s.cancelManagedWork()
	ctx, cancel := context.WithCancel(s.ctx)
	s.managedCancel = cancel
	backend, picker, out, wake := s.ManagedBackend, s.PickGameDirectory, s.managedOut, s.RequestFrame
	go func() {
		defer cancel()
		e := state.Event{Kind: state.ManagedLocated, Token: token}
		if pick {
			if picker == nil {
				e.Kind = state.ManagedLocateFailed
				e.Message = "系统目录选择器不可用，可粘贴游戏目录。"
				postManaged(ctx, out, wake, managedResult{event: e})
				return
			}
			selected, err := picker(ctx)
			if err != nil {
				e.Kind = state.ManagedLocateFailed
				e.Message = state.BriefError(err)
				postManaged(ctx, out, wake, managedResult{event: e})
				return
			}
			if selected == "" {
				e.Kind = state.ManagedPickerCancelled
				postManaged(ctx, out, wake, managedResult{event: e})
				return
			}
			path = selected
		}
		if backend == nil {
			e.Kind = state.ManagedLocateFailed
			e.Message = "游戏目录识别服务不可用。"
		} else {
			inspectCtx, stop := context.WithTimeout(ctx, 15*time.Second)
			root, err := backend.Locate(inspectCtx, path)
			stop()
			if err != nil {
				e.Kind = state.ManagedLocateFailed
				e.Message = state.BriefError(err)
			} else {
				e.Path = root
			}
		}
		postManaged(ctx, out, wake, managedResult{event: e})
	}()
}

func (s *Shell) beginManagedPrepare(token uint64) {
	s.beginManagedPrepareWithin(token, 10*time.Minute)
}

// beginManagedPrepareWithin keeps the production deadline explicit while allowing
// the same worker path to be exercised with a bounded local fixture deadline.
func (s *Shell) beginManagedPrepareWithin(token uint64, timeout time.Duration) {
	s.cancelManagedWork()
	// User cancellation/disposal also invalidates delivery. The worker deadline
	// does not: its failure still has to reach the UI and release Preparing.
	deliveryCtx, cancel := context.WithCancel(s.ctx)
	workerCtx, stop := context.WithTimeout(deliveryCtx, timeout)
	s.managedCancel = cancel
	backend, root, out, wake, progress := s.ManagedBackend, s.Model.Managed.Root, s.managedOut, s.RequestFrame, s.managedProgress
	go func() {
		defer cancel()
		defer stop()
		e := state.Event{Kind: state.ManagedPrepared, Token: token, Path: root}
		if backend == nil {
			e.Kind = state.ManagedPrepareFailed
			e.Message = "游戏启动服务不可用。"
			postManaged(deliveryCtx, out, wake, managedResult{event: e})
			return
		}
		report := func(p ManagedTransfer) {
			if workerCtx.Err() != nil {
				return
			}
			event := state.Event{Kind: state.ManagedProgress, Token: token, Downloaded: p.Downloaded, Total: p.Total, Message: managedStage(p.Stage)}
			select {
			case progress <- event:
			default:
				select {
				case <-progress:
				default:
				}
				select {
				case progress <- event:
				default:
				}
			}
			if wake != nil {
				wake()
			}
		}
		plan, err := backend.Prepare(workerCtx, root, report)
		if workerCtx.Err() != nil {
			// A backend returning a plan after expiry must not revive the operation.
			// Close it here: the UI will never see this plan, so it could never
			// release the native login install lock otherwise.
			closeGamePlan(plan)
			plan = nil
			err = workerCtx.Err()
		}
		if err != nil {
			closeGamePlan(plan)
			plan = nil
			e.Kind = state.ManagedPrepareFailed
			e.Message = "无法启动游戏：" + state.BriefError(err)
		} else if plan == nil {
			e.Kind = state.ManagedPrepareFailed
			e.Message = "游戏检查未返回启动计划。"
		}
		if !postManaged(deliveryCtx, out, wake, managedResult{event: e, plan: plan}) {
			closeGamePlan(plan)
		}
	}()
}

func managedStage(stage string) string {
	switch stage {
	case "checking":
		return "检查游戏文件"
	case "preparing":
		return "准备本地登录窗口"
	case "verifying":
		return "校验文件"
	case "config":
		return "读取登录配置"
	case "ready":
		return "准备就绪"
	}
	return "检查游戏文件"
}

func (s *Shell) saveManagedSettings() {
	if s.ManagedStore == nil {
		return
	}
	if !s.managedSaveStarted {
		s.managedSaveStarted = true
		ctx, store, queue, out, wake := s.ctx, s.ManagedStore, s.managedSave, s.managedOut, s.RequestFrame
		go func() {
			for {
				select {
				case value := <-queue:
					if err := store.Save(ctx, value); err != nil && ctx.Err() == nil {
						postManaged(ctx, out, wake, managedResult{event: state.Event{Kind: state.ManagedSaveFailed, Message: "未能保存游戏设置：" + state.BriefError(err)}})
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	value := ManagedPreferences{GameRoot: s.Model.Managed.Root}
	if s.Model.Managed.RiskAccepted {
		value.RiskVersion = state.ManagedConsentVersion
	}
	select {
	case s.managedSave <- value:
	default:
		select {
		case <-s.managedSave:
		default:
		}
		s.managedSave <- value
	}
}

func (s *Shell) syncManaged() {
	for {
		select {
		case e := <-s.managedProgress:
			s.Dispatch(e)
		case result := <-s.managedOut:
			e := result.event
			if e.Kind == state.ManagedPrepared && e.Token == s.Model.Managed.Request && s.Model.Managed.Phase == state.ManagedPreparing && s.Model.NormalMode {
				s.managedPlan, s.managedPlanRoot = result.plan, e.Path
			} else if e.Kind == state.ManagedPrepared {
				closeGamePlan(result.plan)
			}
			if e.Kind == state.ManagedLocated {
				s.ManagedPathInput = e.Path
			}
			if (e.Kind == state.LoginRequested || e.Kind == state.LoginEnded) && result.session != s.gameSession {
				continue // a request from a game this launcher no longer hosts
			}
			s.adoptGameResult(result)
			s.Dispatch(e)
		default:
			if s.Model.NormalMode && s.Model.Managed.Phase == state.ManagedLoading && s.Model.Managed.Request == 0 && s.RequestFrame != nil {
				s.Dispatch(state.Event{Kind: state.ManagedLoad})
			}
			return
		}
	}
}
