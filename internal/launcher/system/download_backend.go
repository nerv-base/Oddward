// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameinstall"
	"github.com/nerv-base/Oddward/internal/gamelocate"
	"github.com/nerv-base/Oddward/internal/gameres"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
	"github.com/nerv-base/Oddward/internal/launchsettings"
)

// GameDownloadBackend connects the native launcher to verified game-resource storage.
type GameDownloadBackend struct {
	DB             *database.DB
	StateRoot      string
	Settings       *launchsettings.Store
	Client         *http.Client
	Version        string
	roots          []string
	processes      func(context.Context) (downloadProcesses, error)
	free           func(string) (int64, error)
	downloadVolume func(string) (string, error)
	preloadQuery   func(context.Context, string, string) (gameDelivery, error)
	mu             sync.Mutex
	plans          map[string]*downloadPlan
	availability   cachedDownloadAvailability
}

// NewGameDownloadBackend creates the production backend and wires the official
// pre-release delivery query. The database is authoritative for download tasks;
// the state root still holds their compatibility mirrors and the lease. Tests
// that build a backend literal keep the query nil and therefore fail closed
// until they inject a verified route.
func NewGameDownloadBackend(db *database.DB, stateRoot string, settings *launchsettings.Store, client *http.Client, version string) *GameDownloadBackend {
	backend := &GameDownloadBackend{DB: db, StateRoot: stateRoot, Settings: settings, Client: client, Version: version}
	backend.preloadQuery = func(ctx context.Context, branch, version string) (gameDelivery, error) {
		return backend.metadata().delivery(ctx, branch, version)
	}
	return backend
}

type downloadPlan struct {
	root, key, id      string
	kind               state.DownloadKind
	delivery           gameDelivery
	plan               gameres.Plan
	base               gameinstall.InstallRecord
	need, free, margin int64
	preload            *gameinstall.PreloadRecord
	workRoot           string
	externalCache      string
	cacheBudget        bool
	resumeTask         *gamedl.Task
	supersedes         string
}

type downloadProcesses struct{ official, game bool }

func (b *GameDownloadBackend) metadata() remoteDownloads {
	roots := b.roots
	if len(roots) == 0 {
		roots = officialDownloadRoots[:]
	}
	client := b.Client
	if client == nil {
		client = http.DefaultClient
	}
	return remoteDownloads{client: client, roots: roots}
}
func (b *GameDownloadBackend) getPlan(root string) *downloadPlan {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.plans[strings.ToUpper(root)]
}
func (b *GameDownloadBackend) putPlan(root string, plan *downloadPlan) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.plans == nil {
		b.plans = make(map[string]*downloadPlan)
	}
	if plan == nil {
		delete(b.plans, strings.ToUpper(root))
		return
	}
	b.plans[strings.ToUpper(root)] = plan
}
func (b *GameDownloadBackend) space(path string) (int64, error) {
	if b.free != nil {
		return b.free(path)
	}
	return downloadFree(path)
}
func (b *GameDownloadBackend) observe(ctx context.Context) (downloadProcesses, error) {
	if b.processes != nil {
		return b.processes(ctx)
	}
	return observeDownloadProcesses(ctx)
}

// ObserveDownloadProcesses returns a fresh snapshot for the native download page.
func (b *GameDownloadBackend) ObserveDownloadProcesses(ctx context.Context) (official, game bool, err error) {
	seen, err := b.observe(ctx)
	return seen.official, seen.game, err
}

func (b *GameDownloadBackend) guard(ctx context.Context, write bool) error {
	p, err := b.observe(ctx)
	if err != nil {
		return errDownloadProcessCheck
	}
	if p.official {
		return errDownloadOfficial
	}
	if p.game && write {
		return errDownloadGame
	}
	return nil
}

var (
	errDownloadOfficial     = errors.New("official-running")
	errDownloadGame         = errors.New("game-running")
	errDownloadProcessCheck = errors.New("process check failed")
	errDownloadPlanMissing  = errors.New("下载计划已失效，请重新检查")
)

func (b *GameDownloadBackend) HandleDownload(ctx context.Context, req launcher.DownloadRequest, report func(state.Event)) state.Event {
	fx := req.Effect
	event := state.Event{Token: fx.Token, Path: req.Root}
	if report == nil {
		report = func(state.Event) {}
	}
	emit := func(e state.Event) { e.Token = fx.Token; report(e) }
	var err error
	switch fx.Kind {
	case state.EffectInstallLocate:
		event.Kind = state.InstallLocated
		var selected string
		if fx.Enabled {
			selected, err = freshDownloadRoot(req.Root)
		} else {
			var found gamelocate.Installation
			found, err = gamelocate.Inspect(ctx, req.Root)
			selected = found.Root
		}
		event.Path = selected
		if err != nil {
			event.Kind = state.InstallLocateFailed
		}
	case state.EffectInstallCacheLocate:
		event.Kind, event.Path = state.DownloadCacheLocateFailed, ""
		if err = ctx.Err(); err != nil {
			break
		}
		var key string
		key, err = gameinstall.InstallKey(req.Root)
		if err == nil {
			event.Path, _, err = b.externalDownloadCache(req.Root, req.CacheRoot, key)
		}
		if err == nil {
			event.Kind = state.DownloadCacheLocated
		}
	case state.EffectSaveDownloads:
		event.Kind = state.DownloadSettingsFailed
		if b.Settings == nil {
			err = errors.New("下载设置存储不可用")
			break
		}
		err = b.Settings.Update(ctx, func(current *launchsettings.Settings) {
			current.Downloads = launchsettings.Downloads{RateLimitKiB: req.Settings.RateLimitKiB}
		})
		if err == nil {
			event.Kind = state.DownloadSettingsSaved
		}
	case state.EffectDownloadPause:
		event.Kind = state.DownloadPaused
	case state.EffectDownloadCancel:
		event.Kind = state.DownloadCancelled
		err = b.cancelPlan(ctx, req.Root)
		if err != nil {
			event.Kind = state.DownloadFailed
		}
	default:
		event, err = b.handleDownloadOperation(ctx, req, event, emit)
	}
	if err != nil {
		event.Message = downloadMessage(err)
	}
	return event
}

func (b *GameDownloadBackend) handleDownloadOperation(ctx context.Context, req launcher.DownloadRequest, event state.Event, report func(state.Event)) (state.Event, error) {
	root := req.Root
	switch req.Effect.Kind {
	case state.EffectDownloadPlan:
		return b.planDownload(ctx, req, event, report)
	case state.EffectDownloadRun, state.EffectPreloadRun:
		return b.runDownload(ctx, req, event, report)
	case state.EffectDownloadOfferPoll:
		latest, preload, offered, err := b.preloadOfferSnapshot(ctx, root)
		if err != nil {
			event.Kind = state.DownloadOfferPollFailed
			return event, err
		}
		event.Kind, event.Message, event.Body, event.Enabled = state.DownloadOfferPolled, latest, preload, offered
		return event, nil
	case state.EffectDownloadVerify:
		return b.verifyPlan(ctx, req, event, report)
	case state.EffectDownloadCommit, state.EffectPreloadApply:
		return b.commitDownload(ctx, req, event, report)
	case state.EffectRecoverScan:
		return b.recoverDownload(ctx, req, event)
	case state.EffectPreloadCheck, state.EffectPreloadDiscard:
		return b.preloadCommand(ctx, req, event, report)
	}
	store, err := gameinstall.NewStore(b.DB, filepath.Join(b.StateRoot, "installs"))
	switch req.Effect.Kind {
	case state.EffectInstallAdopt:
		event.Kind = state.InstallAdoptFailed
		if err != nil {
			return event, err
		}
		if err = b.guard(ctx, false); err != nil {
			return event, err
		}
		found, err := gamelocate.Inspect(ctx, root)
		if err != nil {
			return event, err
		}
		lease, err := acquireDownloadWork(found.Root)
		if err != nil {
			return event, err
		}
		defer lease.Close()
		status, err := store.TransactionStatus(ctx, found.Root, downloadWork(found.Root))
		if err != nil {
			return event, err
		}
		if status.LaunchBlocked {
			return event, gameinstall.ErrRecoveryRequired
		}
		result, err := store.AdoptQuick(ctx, found.Root, nil)
		if err != nil {
			return event, err
		}
		if result.State == gameinstall.Detected && len(result.Record.Targets) == 0 {
			delivery, fetchErr := b.metadata().delivery(ctx, found.Branch, "")
			if fetchErr == nil {
				result, err = store.AdoptQuick(ctx, found.Root, &gameinstall.AdoptMetadata{ConfigXML: delivery.ConfigResponse.Data, Manifest: delivery.XML.ResList, Branch: found.Branch})
				if err != nil {
					return event, err
				}
			} else if ctx.Err() != nil {
				return event, ctx.Err()
			}
		}
		event.Kind, event.Message = state.InstallAdopted, result.Record.Version
		event.Enabled = result.Record.VerifyCoverage == "full"
		return event, nil
	case state.EffectInstallVerify:
		event.Kind = state.InstallVerifyFailed
		if err != nil {
			return event, err
		}
		if err = b.guard(ctx, false); err != nil {
			return event, err
		}
		lease, err := acquireDownloadWork(root)
		if err != nil {
			return event, err
		}
		defer lease.Close()
		status, err := store.TransactionStatus(ctx, root, downloadWork(root))
		if err != nil {
			return event, err
		}
		if status.LaunchBlocked {
			return event, gameinstall.ErrRecoveryRequired
		}
		result, err := store.VerifyInstallation(ctx, root, func(p gameinstall.VerifyReport) {
			report(state.Event{Kind: state.InstallVerifyProgress, Stage: state.StageVerify, Downloaded: p.BytesDone, Total: p.BytesTotal, FilesDone: uint16(p.FilesDone), FilesTotal: uint16(p.FilesTotal)})
		})
		if err != nil {
			return event, err
		}
		if len(result.Mismatches) > 0 {
			return event, errors.New("校验发现不一致的游戏文件，可重新检查并修复")
		}
		event.Kind, event.Message = state.InstallVerified, result.Record.Version
		return event, nil
	default:
		event.Kind = state.DownloadFailed
		return event, errors.New("下载操作不可用")
	}
}

func downloadMessage(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "操作已暂停"
	case errors.Is(err, errDownloadOfficial):
		return "官方启动器正在运行，请退出后重试"
	case errors.Is(err, errDownloadGame):
		return "请退出游戏后提交更新"
	case errors.Is(err, errDownloadProcessCheck):
		return "无法确认游戏进程状态，请稍后重试"
	case errors.Is(err, errDownloadMetadata):
		return "无法验证官方资源清单，请稍后重试"
	case errors.Is(err, gameinstall.ErrRecoveryRequired):
		return "安装需要恢复，请处理后再启动游戏"
	case errors.Is(err, gameinstall.ErrStaleState):
		return "安装资源状态已变化，请重新接管并校验"
	case errors.Is(err, gameinstall.ErrNoSpace), errors.Is(err, gameres.ErrInsufficientSpace):
		return "磁盘空间不足，请释放空间后重试"
	case errors.Is(err, gameres.ErrPathUnsafe), errors.Is(err, gameinstall.ErrUnsafeTarget):
		return "安装路径无效或包含不受支持的链接"
	case errors.Is(err, os.ErrNotExist):
		return "找不到安装目录或所需文件"
	default:
		return err.Error()
	}
}
