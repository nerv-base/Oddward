// SPDX-License-Identifier: GPL-3.0-or-later

package state

import (
	"path/filepath"
	"strings"
)

// ManagedState is the ordinary user's workflow. It is separate from the explicit
// demonstration clock and never infers successful game login from a host PID.
type ManagedPhase uint8

const (
	ManagedUnlocated ManagedPhase = iota
	ManagedLoading
	ManagedLocating
	ManagedReady
	ManagedPreparing
	ManagedStarting
	ManagedRunning
	ManagedExited
	ManagedFailed
	ManagedUnobserved
)

type ManagedState struct {
	Phase                ManagedPhase
	Root, Message, Error string
	Request              uint64
	RiskAccepted         bool
	Downloaded, Total    int64
	// restoredConsentRoot binds a saved confirmation to its unadopted path until
	// inspection completes. Root remains empty while that saved path is untrusted.
	restoredConsentRoot string
}

const (
	ManagedLoad EventKind = 190 + iota
	ManagedLoaded
	ManagedLoadFailed
	ManagedBrowse
	ManagedSelectPath
	ManagedLocated
	ManagedLocateFailed
	ManagedAcceptLaunch
	ManagedProgress
	ManagedPrepared
	ManagedPrepareFailed
	ManagedHostStarted
	ManagedHostExited
	ManagedHostFailed
	ManagedHostUnobserved
	ManagedCancel
	ManagedResume
	ManagedPickerCancelled
	ManagedSaveFailed
)

const (
	LoadManagedSettings EffectKind = 190 + iota
	PickManagedDirectory
	InspectManagedDirectory
	PrepareManagedLogin
	LaunchManagedHost
	CancelManagedWork
	SaveManagedSettings
)

// ManagedConsentVersion identifies the launch risk notice the user accepted.
// Bump it whenever the notice text changes so the risk is shown again.
const ManagedConsentVersion = "cn-native-login-notice-v1"

func InitialNormal() Model {
	m := Initial()
	m.NormalMode, m.Flow = true, Ready
	m.Managed.Phase = ManagedLoading
	return m
}

func reduceManaged(m Model, e Event) (Model, []Effect, bool) {
	recognized := e.Kind >= ManagedLoad && e.Kind <= ManagedSaveFailed
	if e.Kind == ManagedResume {
		if ManagedActive(m.Managed.Phase) {
			return m, nil, true
		}
		m.NormalMode, m.Page, m.Overlay, m.Flow = true, Home, NoOverlay, Ready
		m.Token++
		return m, []Effect{{Kind: CancelOperation}}, true
	}
	if !m.NormalMode {
		return m, nil, recognized
	}
	p := &m.Managed
	activeHost := ManagedActive(p.Phase)
	if e.Kind == SelectScenario && activeHost {
		return m, nil, true
	}
	prepare := func() (Model, []Effect, bool) {
		p.Request++
		p.Phase = ManagedPreparing
		p.Error = ""
		p.Message = "正在准备登录组件"
		p.Downloaded, p.Total = 0, 0
		m.Progress = 0
		m.Overlay = NoOverlay
		// The user asked to start the game, so the daily check-in may run now
		// (when its switch is on). It is one more effect: the launch path never
		// waits on it and a failed launch does not cancel it.
		return m, []Effect{{Kind: PrepareManagedLogin, Token: p.Request}, {Kind: TajiduoCheckInLaunch}}, true
	}
	if e.Kind == Primary {
		if m.Page != Home || m.Overlay != NoOverlay || activeHost || ManagedBusy(p.Phase) {
			return m, nil, true
		}
		if p.Root == "" {
			m.Overlay = LocateGame
			return m, nil, true
		}
		if !p.RiskAccepted {
			m.Overlay = LaunchNotice
			return m, nil, true
		}
		return prepare()
	}
	if !recognized {
		return m, nil, false
	}
	current := e.Token == p.Request
	switch e.Kind {
	case ManagedSaveFailed:
		p.Error = e.Message
	case ManagedLoad:
		if p.Phase != ManagedLoading || p.Request != 0 {
			break
		}
		p.Request++
		return m, []Effect{{Kind: LoadManagedSettings, Token: p.Request}}, true
	case ManagedLoaded:
		if !current || p.Phase != ManagedLoading {
			break
		}
		p.RiskAccepted = e.Enabled && e.Path != ""
		p.restoredConsentRoot = ""
		if p.RiskAccepted {
			p.restoredConsentRoot = e.Path
		}
		p.Root = ""
		p.Error = ""
		if e.Path == "" {
			p.Phase = ManagedUnlocated
			break
		}
		p.Request++
		p.Phase = ManagedLocating
		return m, []Effect{{Kind: InspectManagedDirectory, Token: p.Request, Path: e.Path}}, true
	case ManagedLoadFailed:
		if !current || p.Phase != ManagedLoading {
			break
		}
		p.Phase = ManagedUnlocated
		p.Error = e.Message
	case ManagedBrowse, ManagedSelectPath:
		if m.Overlay != LocateGame || activeHost || ManagedBusy(p.Phase) {
			break
		}
		if e.Kind == ManagedSelectPath && e.Path == "" {
			break
		}
		p.Request++
		p.Phase = ManagedLocating
		p.Error = ""
		kind := InspectManagedDirectory
		if e.Kind == ManagedBrowse {
			kind = PickManagedDirectory
		}
		return m, []Effect{{Kind: kind, Token: p.Request, Path: e.Path}}, true
	case ManagedLocated:
		if !current || p.Phase != ManagedLocating || e.Path == "" {
			break
		}
		consentRoot := p.Root
		if p.restoredConsentRoot != "" {
			consentRoot = p.restoredConsentRoot
		}
		if !SameManagedRoot(consentRoot, e.Path) {
			p.RiskAccepted = false
		}
		p.restoredConsentRoot = ""
		p.Root, p.Message, p.Error = e.Path, e.Message, ""
		p.Phase = ManagedReady
		m.Overlay = NoOverlay
		return m, []Effect{{Kind: SaveManagedSettings, Token: p.Request}}, true
	case ManagedLocateFailed:
		if !current || p.Phase != ManagedLocating {
			break
		}
		p.Phase = ManagedFailed
		p.Error = e.Message
	case ManagedPickerCancelled:
		if !current || p.Phase != ManagedLocating {
			break
		}
		p.Phase = ManagedUnlocated
		if p.Root != "" {
			p.Phase = ManagedReady
		}
	case ManagedAcceptLaunch:
		if m.Overlay != LaunchNotice || activeHost || ManagedBusy(p.Phase) || p.Root == "" {
			break
		}
		p.RiskAccepted = true
		next, fx, ok := prepare()
		fx = append(fx, Effect{Kind: SaveManagedSettings, Token: next.Managed.Request})
		return next, fx, ok
	case ManagedProgress:
		if !current || p.Phase != ManagedPreparing {
			break
		}
		if e.Downloaded < 0 || e.Total < 0 || e.Total > 0 && e.Downloaded > e.Total {
			break
		}
		p.Downloaded, p.Total, p.Message = e.Downloaded, e.Total, e.Message
		m.Progress = 0
		if e.Total > 0 {
			m.Progress = float64(e.Downloaded) / float64(e.Total)
		}
	case ManagedPrepared:
		if !current || p.Phase != ManagedPreparing || !p.RiskAccepted {
			break
		}
		p.Phase = ManagedStarting
		p.Message = "正在启动登录组件"
		m.Progress = 0
		return m, []Effect{{Kind: LaunchManagedHost, Token: p.Request}}, true
	case ManagedPrepareFailed:
		if !current || p.Phase != ManagedPreparing {
			break
		}
		p.Phase = ManagedFailed
		p.Error = e.Message
		m.Progress = 0
	case ManagedHostStarted:
		if !current || p.Phase != ManagedStarting {
			break
		}
		p.Phase = ManagedRunning
		p.Message = "登录组件运行中；请在组件窗口完成登录。游戏状态尚未确认。"
	case ManagedHostExited, ManagedHostFailed:
		if !current || (p.Phase != ManagedStarting && p.Phase != ManagedRunning) {
			break
		}
		p.Phase = ManagedExited
		if e.Kind == ManagedHostFailed {
			p.Phase = ManagedFailed
			p.Error = e.Message
		}
		p.Message = "登录组件已退出，尚未确认游戏登录状态。"
	case ManagedHostUnobserved:
		if !current || (p.Phase != ManagedStarting && p.Phase != ManagedRunning) {
			break
		}
		p.Phase = ManagedUnobserved
		p.Error = e.Message
		p.Message = "组件状态无法确认；保持启动锁，避免重复启动。"
	case ManagedCancel:
		if p.Phase != ManagedPreparing && p.Phase != ManagedLocating && p.Phase != ManagedLoading {
			break
		}
		p.Request++
		p.Phase = ManagedUnlocated
		if p.Root != "" {
			p.Phase = ManagedReady
		}
		p.Message = "已取消"
		m.Progress = 0
		return m, []Effect{{Kind: CancelManagedWork, Token: p.Request}}, true
	}
	return m, nil, true
}

func managedPresentation(m Model) (Presentation, bool) {
	if !m.NormalMode {
		return Presentation{}, false
	}
	p := m.Managed
	detail := p.Message
	if p.Error != "" {
		detail = p.Error
	}
	out := Presentation{Primary: "定位游戏", Status: "尚未定位", Detail: detail, Enabled: true}
	switch p.Phase {
	case ManagedLoading:
		out.Primary, out.Status, out.Enabled = "读取设置", "正在准备", false
	case ManagedLocating:
		out.Primary, out.Status, out.Enabled = "正在定位", "只读检查游戏目录", false
	case ManagedPreparing:
		out.Primary, out.Status, out.Enabled, out.Progress = "准备登录组件", p.Message, false, true
	case ManagedStarting:
		out.Primary, out.Status, out.Enabled = "正在启动", "正在启动登录组件", false
	case ManagedRunning:
		out.Primary, out.Status, out.Enabled = "登录组件运行中", "游戏状态尚未确认", false
	case ManagedUnobserved:
		out.Primary, out.Status, out.Enabled = "状态待确认", "已保留启动锁", false
	default:
		if p.Root != "" {
			out.Primary, out.Status = "启动游戏", "已定位游戏"
			if p.Phase == ManagedFailed {
				out.Primary = "重试启动"
			}
		}
	}
	return out, true
}

// sameManagedRoot compares only already selected/inspected path spellings. It
// neither follows links nor grants content trust or permission to launch.
func SameManagedRoot(a, b string) bool {
	return a != "" && b != "" && strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func ManagedActive(p ManagedPhase) bool {
	return p == ManagedStarting || p == ManagedRunning || p == ManagedUnobserved
}
func ManagedBusy(p ManagedPhase) bool {
	return p == ManagedLoading || p == ManagedLocating || p == ManagedPreparing || p == ManagedStarting
}
