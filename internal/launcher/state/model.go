// SPDX-License-Identifier: GPL-3.0-or-later

package state

import (
	"math"
	"time"
)

// MediaBar is independent of playback and game tasks.
type MediaBar uint8

const (
	MediaBarHidden MediaBar = iota
	MediaBarVisible
	MediaBarLeaving
	MediaBarDismissed
)

// Flow is the game region of the UI, not a claim about a real installation.
type Flow uint8

const (
	Checking Flow = iota
	NotInstalled
	Ready
	UpdateAvailable
	Downloading
	Paused
	Verifying
	Launching
	Running
	Failed
)

// Page is functional navigation within one game, never a game selector.
type Page uint8

const (
	Home Page = iota
	WishAnalysis
	CheckIn
	GameData
	LauncherSettings
)

type Overlay uint8

const (
	NoOverlay Overlay = iota
	Scenarios
	ConfirmQuit
	NewsDetails
	LocateGame
	LaunchNotice
	GameSettings
	ConfirmDownloadCancel
)

type Media uint8

const (
	MediaLoading Media = iota
	MediaPlaying
	MediaPaused
	MediaUnavailable
)

type EventKind uint16

const (
	Primary EventKind = iota
	OpenOverlay
	Dismiss
	SelectScenario
	Progressed
	Completed
	OperationFailed
	RequestQuit
	ConfirmExit
	ToggleMotion
	ToggleReducedMotion
	ToggleCloseToTray
	ToggleAccentFollow
	// ToggleCheckUpdates turns the startup update check on or off.
	ToggleCheckUpdates
	// UIPreferencesLoaded restores the saved launcher UI choices at startup.
	UIPreferencesLoaded
	MediaReady
	MediaFailed
	// MediaRetry returns a failed video to loading when a newer file is opened.
	MediaRetry
	SystemMotionChanged
	Navigate
	OpenOfficial
	MediaBarActivity
	RevealMediaBar
	HideMediaBar
	ToggleDemoClock
	StepDemo
	FailDemo
	SelectNewsTab
	SelectNewsStory
	SelectMediaState
	ResetMediaPreview
	// The official announcement board: the shell fetches, Reduce only maps.
	NewsRequested
	NewsLoaded
	NewsFailed
	DetailRequested
	DetailLoaded
	DetailFailed
	WishRequestList
	WishSelectRole
	WishRequestDelete
	WishRequestImport
	WishRequestExport
	WishAcceptConfirm
	WishCancelConfirm
	WishListed
	WishLoaded
	WishDeleted
	WishImportPicked
	WishImported
	WishExportPicked
	WishExported
	WishOperationFailed
	WishImportNeedsConfirmation
	// Check-in: account reads, two independent writes, the automatic
	// switches and the game status panel.
	CheckInStateRequested
	CheckInStateLoaded
	CheckInStateFailed
	CheckInBegin
	CheckInSigned
	CheckInConfirm
	CheckInAutoToggle
	CheckInAutoSaved
	CheckInAutoTick
	CheckInStatusRequested
	CheckInStatusLoaded
	CheckInStatusFailed
	SettingsBack
	SelectLauncherSettingsSection
)

type Event struct {
	Kind                    EventKind
	Index                   uint8
	Media                   Media
	Page                    Page
	Overlay                 Overlay
	Flow                    Flow
	Token                   uint64
	Progress                float64
	Message                 string
	Path                    string
	Downloaded, Total       int64
	CurrentVersion          string
	DownloadSettings        DownloadPreferences
	Stage                   DownloadStage
	FilesDone, FilesTotal   uint16
	Reused, PatchBytes      int64
	ETASecs                 int32
	Needed, Free, Suggested int64
	// Rate is the measured transfer rate in bytes per second; 0 is unknown.
	Rate    int64
	Enabled bool
	Focused bool
	// Since is when the backend first observed the game running.
	Since     time.Time
	FromMedia bool
	Now       time.Time
	// Rows carries a loaded official feed; Body carries the text of one
	// official announcement. Both are fenced by Token.
	Rows    NewsRow
	Banners BannerRow
	Body    string
	// Logins lists the saved accounts a login request offers.
	Logins                                   []SavedLogin
	WishLinkFailure                          WishLinkFailure
	WishAccountID                            string
	WishLinked, WishRelink                   bool
	WishRoleID                               string
	WishCount                                int
	WishNewCount, WishOutside, WishConflicts int
	WishFailure                              WishFailureKind
	WishFormat                               WishFormat
	// Check carries the check-in facts a shell response reports.
	Check CheckInPayload
	// CloseToTray, AccentFollow and CheckUpdates carry the saved launcher UI choices.
	CloseToTray, AccentFollow, CheckUpdates bool
	// Accounts carries the home account row's loaded list.
	Accounts []SavedAccount
}

type EffectKind uint16

const (
	Simulate EffectKind = iota
	CancelOperation
	CloseWindow
	VisitOfficial
	FocusMediaConsole
	// FetchNews and FetchDetail ask the shell to read the official public
	// pages off the UI thread. Reduce never performs I/O itself.
	FetchNews
	FetchDetail
	WishList
	WishLoad
	WishDelete
	WishPickImport
	WishImport
	WishPickExport
	WishExport
	// Check-in effects read the account's state, run one write, refresh the
	// status panel and persist one automatic switch.
	TajiduoCheckInState
	TajiduoCheckInSign
	TajiduoCheckInStatus
	TajiduoCheckInAuto
	TajiduoCheckInLaunch
	// PersistUIPreferences saves the launcher window and theming choices.
	PersistUIPreferences
	// LoadHomeAccounts and RememberHomeAccount bridge the compact account row.
	LoadHomeAccounts
	RememberHomeAccount
)

type Effect struct {
	Path       string
	Kind       EffectKind
	Flow       Flow
	Token      uint64
	RoleID     string
	WishFormat WishFormat
	// Confirmed marks a step the user explicitly approved: a lossy export or
	// a retry of a check-in whose last answer never arrived.
	Confirmed bool
	// CheckInKind and Enabled address one daily task or one switch.
	CheckInKind CheckInKind
	Enabled     bool
	// CloseToTray, AccentFollow and CheckUpdates carry the persisted launcher
	// UI choices for PersistUIPreferences.
	CloseToTray, AccentFollow, CheckUpdates bool
}

// NewsStatus is the state of the official announcement feed. There is no
// mock state: the board is either reading the public site, showing what the
// site returned, or saying that it could not.
type NewsStatus uint8

const (
	NewsPending NewsStatus = iota
	NewsPresent
	NewsUnavailable
)

// NewsRow is the projection of the official feed the connected module shows:
// the site's three channels, up to three stories each, newest first. Arrays
// of values keep Model comparable, which the tests rely on.
type NewsRow struct {
	Counts [3]uint8
	Titles [3][3]string
	Dates  [3][3]string
	URLs   [3][3]string
}

// At returns the story at index of one channel.
func (r NewsRow) At(tab, index uint8) (title, date, url string, ok bool) {
	if tab > 2 || index > 2 || r.Counts[tab] <= index {
		return "", "", "", false
	}
	return r.Titles[tab][index], r.Dates[tab][index], r.URLs[tab][index], true
}

// Transfer holds the figures the last progress report carried. Zero values
// mean "not reported": the view never estimates what was not measured.
type Transfer struct {
	Downloaded, Total int64
	// Rate is bytes per second while the transfer is live.
	Rate int64
}

// Empty reports whether the official feed listed nothing at all.
func (r NewsRow) Empty() bool { return r.Counts == [3]uint8{} }

type Model struct {
	NormalMode          bool
	Managed             ManagedState
	Page                Page
	Wish                WishState
	CheckIn             CheckInState
	DemoPaused          bool
	NewsTab             uint8
	NewsIndex           uint8
	NewsItem            uint8
	Bar                 MediaBar
	BarDeadline         time.Time
	Flow                Flow
	Overlay             Overlay
	SettingsSection     LauncherSettingsSection
	settingsReturnPage  Page
	settingsFromMedia   bool
	locateFromSettings  bool
	Media               Media
	MediaPreview        bool
	Token               uint64
	Progress            float64
	Transfer            Transfer
	Error               string
	Motion              bool
	ReducedMotion       bool
	SystemReducedMotion bool
	// CloseToTray hides the window instead of asking to quit when the user
	// closes it. AccentFollow keeps the launch tone from the current key art.
	CloseToTray  bool
	AccentFollow bool
	// CheckUpdates asks the update service once at startup.
	CheckUpdates bool
	Closed       bool
	Download     DownloadState
	Update       UpdateState
	Network      NetworkState
	Login        LoginState
	HomeAccounts HomeAccountsState

	// The official announcement board.
	News           NewsStatus
	NewsRows       NewsRow
	NewsBanners    BannerRow
	NewsError      string
	NewsRequest    uint64
	NewsFromCache  bool
	NewsRefreshing bool

	// The opened announcement.
	Detail        NewsStatus
	DetailBody    string
	DetailError   string
	DetailItem    uint8
	DetailTitle   string
	DetailDate    string
	DetailURL     string
	DetailRequest uint64
}

func Initial() Model {
	return Model{Flow: Checking, Media: MediaLoading, Motion: true, CloseToTray: true, AccentFollow: true, CheckUpdates: true, Token: 1, News: NewsPending}
}

// Reduce is the only authority for transitions. It never performs I/O.
func Reduce(m Model, e Event) (Model, []Effect) {
	if m.Closed {
		return m, nil
	}
	if next, effects, handled := reduceDownload(m, e); handled {
		return next, effects
	}
	if next, effects, handled := reduceUpdate(m, e); handled {
		return next, effects
	}
	if next, effects, handled := reduceLogin(m, e); handled {
		return next, effects
	}
	if next, effects, handled := reduceHomeAccounts(m, e); handled {
		return next, effects
	}
	if next, effects, handled := reduceNetwork(m, e); handled {
		return next, effects
	}
	if next, effects, handled := reduceManaged(m, e); handled {
		return next, effects
	}
	if next, effects, handled := ReduceWish(m, e); handled {
		return next, effects
	}
	if next, effects, handled := ReduceCheckIn(m, e); handled {
		return next, effects
	}
	start := func(flow Flow, reset bool) (Model, []Effect) {
		m.Token++
		m.Flow = flow
		m.Error = ""
		m.Transfer.Rate = 0
		if reset {
			m.Progress = 0
			m.Transfer = Transfer{}
		}
		return m, []Effect{{Kind: CancelOperation}, {Kind: Simulate, Flow: flow, Token: m.Token}}
	}
	closeNow := func() (Model, []Effect) {
		m.Closed = true
		m.Token++
		return m, []Effect{{Kind: CancelOperation}, {Kind: CloseWindow}}
	}
	syncMedia := func() {
		if m.Media == MediaPlaying || m.Media == MediaPaused {
			if m.Motion && !m.ReducedMotion && !m.SystemReducedMotion {
				m.Media = MediaPlaying
			} else {
				m.Media = MediaPaused
			}
		}
	}
	switch e.Kind {
	case RevealMediaBar:
		if m.Overlay == NoOverlay && m.Page != LauncherSettings {
			m.Bar = MediaBarVisible
			m.BarDeadline = time.Time{}
		}
	case HideMediaBar:
		if m.Overlay == NoOverlay && m.Page != LauncherSettings {
			m.Bar = MediaBarDismissed
			m.BarDeadline = time.Time{}
		}
	case MediaBarActivity:
		if m.Overlay != NoOverlay || m.Page == LauncherSettings {
			break
		}
		occupied := e.Enabled || e.Focused
		if m.Bar == MediaBarDismissed {
			if !occupied {
				m.Bar = MediaBarHidden
			}
		} else if occupied {
			m.Bar = MediaBarVisible
			m.BarDeadline = time.Time{}
		} else if m.Bar == MediaBarVisible {
			m.Bar = MediaBarLeaving
			m.BarDeadline = e.Now.Add(600 * time.Millisecond)
		} else if m.Bar == MediaBarLeaving && !e.Now.Before(m.BarDeadline) {
			m.Bar = MediaBarHidden
			m.BarDeadline = time.Time{}
		}
	case ToggleDemoClock:
		if m.Overlay == Scenarios {
			m.DemoPaused = !m.DemoPaused
		}
	case StepDemo:
		if m.Overlay == Scenarios && Active(m.Flow) {
			// Use the ordinary token-fenced transition, one stage at a time.
			return Reduce(m, Event{Kind: Completed, Token: m.Token})
		}
	case FailDemo:
		if m.Overlay == Scenarios && Active(m.Flow) {
			return Reduce(m, Event{Kind: OperationFailed, Token: m.Token,
				Message: "演示失败：模拟连接中断；未发起网络请求或修改游戏文件。"})
		}
	case SelectMediaState:
		if m.Overlay == Scenarios && e.Media <= MediaUnavailable {
			m.Media, m.MediaPreview = e.Media, true
		}
	case ResetMediaPreview:
		if m.Overlay == Scenarios && m.MediaPreview {
			m.Media, m.MediaPreview = MediaLoading, false
		}
	case SelectNewsTab:
		if m.Overlay == NoOverlay && m.Page == Home && e.Index <= 2 {
			m.NewsTab, m.NewsItem = e.Index, 0
		}
	case SelectNewsStory:
		if m.Overlay == NoOverlay && m.Page == Home && e.Index < m.NewsBanners.Count && e.Index < 8 {
			m.NewsIndex, m.NewsItem = e.Index, 0
		}
	case AdvanceNewsBanner:
		if m.Page == Home && m.Overlay == NoOverlay && m.News == NewsPresent && !m.ReducedMotion && !m.SystemReducedMotion && m.NewsBanners.Count > 1 {
			m.NewsIndex = (m.NewsIndex + 1) % min(m.NewsBanners.Count, uint8(len(m.NewsBanners.Titles)))
		}
	case OpenOfficial:
		if m.Overlay == NoOverlay && m.Page == Home && e.Index <= 2 {
			m.NewsItem = e.Index
			m.Overlay = NewsDetails
			return openDetail(m, e.Index)
		}
	case OpenNewsBanner:
		if m.Overlay != NoOverlay || m.Page != Home || m.News != NewsPresent {
			break
		}
		title, url, _, ok := m.NewsBanners.At(m.NewsIndex)
		if !ok {
			break
		}
		m.Overlay, m.DetailTitle, m.DetailDate, m.DetailURL = NewsDetails, title, "", url
		return requestDetail(m)
	case DetailRequested:
		// A retry preserves the original article target, including a banner
		// whose URL need not occur in the selected category's visible rows.
		if m.Overlay != NewsDetails {
			break
		}
		return requestDetail(m)
	case NewsRequested:
		// Only the connected home board reads the public site, and only when
		// no modal panel is in front of it.
		if m.Page != Home || m.Overlay != NoOverlay || m.NewsRefreshing {
			break
		}
		m.NewsRequest++
		m.NewsRefreshing, m.NewsError = true, ""
		if !m.NewsFromCache {
			m.News = NewsPending
		}
		return m, []Effect{{Kind: FetchNews, Token: m.NewsRequest}}
	case NewsCacheLoaded:
		if e.Token != m.NewsRequest || !m.NewsRefreshing || m.News == NewsPresent || (e.Rows.Empty() && e.Banners.Count == 0) {
			break
		}
		m.News, m.NewsFromCache, m.NewsRows, m.NewsBanners = NewsPresent, true, e.Rows, e.Banners
		m.NewsIndex, m.NewsItem = 0, 0
	case NewsLoaded:
		if e.Token != m.NewsRequest {
			break
		}
		m.NewsRefreshing = false
		if m.NewsFromCache && e.Rows.Empty() && e.Banners.Count == 0 {
			break
		}
		if !m.NewsFromCache || m.NewsBanners != e.Banners {
			m.NewsIndex = 0
		}
		m.News, m.NewsError, m.NewsRows, m.NewsBanners = NewsPresent, "", e.Rows, e.Banners
		m.NewsFromCache, m.NewsItem = false, 0
	case NewsFailed:
		if e.Token != m.NewsRequest {
			break
		}
		m.NewsRefreshing, m.NewsError = false, e.Message
		if !m.NewsFromCache {
			m.News, m.NewsRows, m.NewsBanners = NewsUnavailable, NewsRow{}, BannerRow{}
			m.NewsIndex, m.NewsItem = 0, 0
		}
	case DetailLoaded:
		if e.Token != m.DetailRequest || m.Overlay != NewsDetails {
			break
		}
		m.Detail, m.DetailError, m.DetailBody = NewsPresent, "", e.Body
	case DetailFailed:
		if e.Token != m.DetailRequest || m.Overlay != NewsDetails {
			break
		}
		m.Detail, m.DetailError, m.DetailBody = NewsUnavailable, e.Message, ""
	case Navigate:
		if m.Overlay == NoOverlay && e.Page <= LauncherSettings && e.Page != m.Page {
			if e.Page == LauncherSettings {
				m.settingsReturnPage, m.settingsFromMedia = m.Page, e.FromMedia
				m.SettingsSection = SettingsGeneral
				m.Bar, m.BarDeadline = MediaBarHidden, time.Time{}
				if !m.Network.Saving {
					m.Network.Saved, m.Network.Error = false, ""
				}
			}
			m.Page = e.Page
		}
	case OpenOverlay:
		gameSettings := e.Overlay == GameSettings && m.NormalMode && m.Page == Home && m.Overlay == NoOverlay
		if (e.Overlay == Scenarios || e.Overlay == LocateGame || gameSettings) && m.Overlay != ConfirmQuit {
			m.locateFromSettings = e.Overlay == LocateGame && m.Overlay == GameSettings
			m.Overlay = e.Overlay
		}
	case Dismiss, SettingsBack:
		if e.Kind == SettingsBack {
			if m.Overlay == LocateGame && m.locateFromSettings {
				m.Overlay = GameSettings
				m.locateFromSettings = false
				break
			}
			if m.Overlay != LocateGame {
				break
			}
		}
		if m.Overlay == NoOverlay && m.Page == LauncherSettings {
			m.Page = m.settingsReturnPage
			if m.settingsFromMedia {
				m.settingsFromMedia = false
				m.Bar, m.BarDeadline = MediaBarVisible, time.Time{}
				return m, []Effect{{Kind: FocusMediaConsole}}
			}
		}
		m.Overlay = NoOverlay
		m.locateFromSettings = false
	case Primary:
		if m.Overlay != NoOverlay || m.Page != Home {
			break
		}
		switch m.Flow {
		case NotInstalled, UpdateAvailable:
			return start(Downloading, true)
		case Ready:
			return start(Launching, true)
		case Paused:
			return start(Downloading, false)
		case Downloading:
			m.Flow = Paused
			m.Transfer.Rate = 0
			m.Token++
			return m, []Effect{{Kind: CancelOperation}}
		case Failed:
			return start(Checking, true)
		}
	case SelectScenario:
		if m.Overlay != Scenarios || e.Flow > Failed {
			break
		}
		m.Overlay = NoOverlay
		wasNormal := m.NormalMode
		if m.NormalMode {
			m.Managed.Request++
			if ManagedBusy(m.Managed.Phase) {
				m.Managed.Phase = ManagedUnlocated
				if m.Managed.Root != "" {
					m.Managed.Phase = ManagedReady
				}
			}
		}
		m.NormalMode = false
		m.Page = Home
		m.Progress = 0
		m.Error = ""
		m.Token++
		m.Flow = e.Flow
		if e.Flow == Failed {
			m.Error = "演示：连接中断。现有文件保持不变，请重试。"
		}
		if e.Flow == Paused {
			m.Progress = .38
		}
		fx := []Effect{{Kind: CancelOperation}}
		if wasNormal {
			fx = append(fx, Effect{Kind: CancelManagedWork})
		}
		if Active(m.Flow) {
			fx = append(fx, Effect{Kind: Simulate, Flow: m.Flow, Token: m.Token})
		}
		return m, fx
	case Progressed, Completed, OperationFailed:
		if e.Token != m.Token || !Active(m.Flow) {
			break
		}
		switch e.Kind {
		case Progressed:
			if (m.Flow == Downloading || m.Flow == Verifying) && !math.IsNaN(e.Progress) && !math.IsInf(e.Progress, 0) {
				m.Progress = math.Max(m.Progress, math.Min(1, math.Max(0, e.Progress)))
				if e.Total > 0 && e.Downloaded >= 0 && e.Downloaded <= e.Total && e.Rate >= 0 {
					m.Transfer = Transfer{Downloaded: e.Downloaded, Total: e.Total, Rate: e.Rate}
				}
			}
		case OperationFailed:
			m.Flow = Failed
			m.Error = e.Message
			if m.Error == "" {
				m.Error = "操作未完成，请重试。"
			}
			m.Token++
			return m, []Effect{{Kind: CancelOperation}}
		case Completed:
			switch m.Flow {
			case Checking:
				m.Flow = Ready
				m.Progress = 0
			case Downloading:
				return start(Verifying, true)
			case Verifying:
				m.Flow = Ready
				m.Progress = 0
			case Launching:
				m.Flow = Running
				m.Progress = 0
			}
			m.Token++
			return m, []Effect{{Kind: CancelOperation}}
		}
	case RequestQuit:
		if Active(m.Flow) && m.Flow != Checking || m.Flow == Paused ||
			m.NormalMode && (ManagedBusy(m.Managed.Phase) || ManagedActive(m.Managed.Phase)) {
			m.Overlay = ConfirmQuit
		} else {
			return closeNow()
		}
	case ConfirmExit:
		if m.Overlay == ConfirmQuit {
			return closeNow()
		}
	case ToggleMotion:
		if m.Overlay == NoOverlay {
			m.Motion = !m.Motion
			syncMedia()
		}
	case ToggleReducedMotion:
		if m.Page == LauncherSettings && m.Overlay == NoOverlay {
			m.ReducedMotion = !m.ReducedMotion
			syncMedia()
		}
	case ToggleCloseToTray:
		if m.Page == LauncherSettings && m.Overlay == NoOverlay {
			m.CloseToTray = !m.CloseToTray
			return m, []Effect{{Kind: PersistUIPreferences, CloseToTray: m.CloseToTray, AccentFollow: m.AccentFollow, CheckUpdates: m.CheckUpdates}}
		}
	case ToggleAccentFollow:
		if m.Page == LauncherSettings && m.Overlay == NoOverlay {
			m.AccentFollow = !m.AccentFollow
			return m, []Effect{{Kind: PersistUIPreferences, CloseToTray: m.CloseToTray, AccentFollow: m.AccentFollow, CheckUpdates: m.CheckUpdates}}
		}
	case ToggleCheckUpdates:
		if m.Page == LauncherSettings && m.Overlay == NoOverlay {
			m.CheckUpdates = !m.CheckUpdates
			return m, []Effect{{Kind: PersistUIPreferences, CloseToTray: m.CloseToTray, AccentFollow: m.AccentFollow, CheckUpdates: m.CheckUpdates}}
		}
	case UIPreferencesLoaded:
		m.CloseToTray, m.AccentFollow, m.CheckUpdates = e.CloseToTray, e.AccentFollow, e.CheckUpdates
	case SystemMotionChanged:
		m.SystemReducedMotion = e.Enabled
		syncMedia()
	case MediaReady:
		if !m.MediaPreview {
			m.Media = MediaPlaying
			syncMedia()
		}
	case MediaFailed:
		if !m.MediaPreview {
			m.Media = MediaUnavailable
		}
	case MediaRetry:
		if !m.MediaPreview {
			m.Media = MediaLoading
		}
	}
	if m.Overlay != NoOverlay {
		m.Bar = MediaBarHidden
		m.BarDeadline = time.Time{}
	}
	return m, nil
}

// openDetail starts reading the text of one announcement. The panel opens at
// once and shows its own reading state until the shell delivers the text.
func openDetail(m Model, index uint8) (Model, []Effect) {
	m.DetailItem = index
	m.DetailTitle, m.DetailDate, m.DetailURL, _ = m.NewsRows.At(m.NewsTab, index)
	return requestDetail(m)
}

func requestDetail(m Model) (Model, []Effect) {
	m.Detail, m.DetailBody, m.DetailError = NewsPending, "", ""
	m.DetailRequest++
	return m, []Effect{{Kind: FetchDetail, Token: m.DetailRequest}}
}

func Active(f Flow) bool {
	return f == Checking || f == Downloading || f == Verifying || f == Launching
}

type Presentation struct {
	Primary        string
	Status         string
	Detail         string
	Enabled        bool
	Progress       bool
	Task           bool
	LocateExisting bool
}

func Present(m Model) Presentation {
	if m.Download.Phase == DownloadRecoveryRequired {
		return Presentation{Primary: "完成上次更新", Status: "上次更新未完成", Detail: DownloadNoteText("recovery-required"), Enabled: true}
	}
	if downloadMutation(m.Download) {
		status := "正在提交更新"
		if m.Download.Stage == StageApply || m.Download.Phase == DownloadRebuilding {
			status = "正在重建文件"
		}
		return Presentation{Primary: "正在更新", Status: status, Detail: "请等待更新完成后启动游戏"}
	}
	if m.Download.GameRunning {
		return Presentation{Primary: "游戏运行中", Status: "游戏正在运行", Detail: "退出游戏后可再次启动"}
	}
	if m.Download.OfficialRunning {
		return Presentation{Primary: "等待官方启动器退出", Status: "安装目录正在使用", Detail: DownloadNoteText("official-running")}
	}
	if p, _, ok := homeDownloadPrimary(m); ok {
		return p
	}
	if p, ok := managedPresentation(m); ok {
		return p
	}
	var p Presentation
	switch m.Flow {
	case Checking:
		p = Presentation{Primary: "正在检查", Status: "正在准备", Detail: "读取演示环境…"}
	case NotInstalled:
		p = Presentation{Primary: "下载游戏", Status: "尚未安装", Detail: "选择下载后体验完整交互流程", Enabled: true}
	case Ready:
		p = Presentation{Primary: "启动游戏", Status: "准备就绪", Detail: "下一站，海特洛。", Enabled: true}
	case UpdateAvailable:
		p = Presentation{Primary: "更新游戏", Status: "发现新版本", Detail: "演示更新包 · 不会下载真实文件", Enabled: true}
	case Downloading:
		p = Presentation{Primary: "暂停下载", Status: "正在下载", Detail: "演示传输 · 不写入游戏目录", Enabled: true, Progress: true}
	case Paused:
		p = Presentation{Primary: "继续下载", Status: "下载已暂停", Detail: "进度已保留，随时继续", Enabled: true, Progress: true}
	case Verifying:
		p = Presentation{Primary: "正在校验", Status: "检查文件完整性", Detail: "演示校验 · 请稍候", Progress: true}
	case Launching:
		p = Presentation{Primary: "正在启动", Status: "正在连接城市", Detail: "演示启动 · 不会运行游戏进程"}
	case Running:
		p = Presentation{Primary: "游戏运行中", Status: "旅程已开始", Detail: "演示状态 · 可在状态预览中返回"}
	case Failed:
		p = Presentation{Primary: "重试", Status: "暂时无法继续", Detail: m.Error, Enabled: true}
	default:
		p = Presentation{Primary: "不可用", Status: "未知状态", Detail: "请重新打开启动器"}
	}
	return p
}
func (m Model) AnimateBackground() bool {
	return m.Motion && !m.ReducedMotion && !m.SystemReducedMotion && m.Media == MediaPlaying
}
