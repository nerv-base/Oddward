// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
	"github.com/nerv-base/Oddward/internal/news"
)

// Source reads the official public announcements. The launcher only draws
// what it returns: a nil Source leaves the board in its reading state and
// touches no network at all, which is what the unit tests use.
type Source interface {
	Feed(ctx context.Context) (news.Feed, error)
	Detail(ctx context.Context, url string) (news.Detail, error)
}

// A finished background read, handed to the UI thread through a buffered
// channel of capacity one so that no goroutine ever touches the Model.
type newsResult struct {
	token   uint64
	rows    state.NewsRow
	banners state.BannerRow
	err     error
}

type detailResult struct {
	token uint64
	body  string
	err   error
}

// Shell owns UI-thread state. No element or build context escapes a frame.
// The mock clock is advanced by the UI, not goroutines or wall-clock tests.
type Shell struct {
	Model              state.Model
	WishLinkBackend    WishLinkBackend
	wishLink           wishLinkMedia
	WishBackend        WishArchiveBackend
	WishImages         wishImageSource
	wishIcons          wishIconMedia
	WishLocation       *time.Location
	PickWishImport     func(context.Context) (string, error)
	PickWishExport     func(context.Context, string, state.WishFormat) (string, error)
	wish               wishArchives
	FixedWindow        bool
	ManagedBackend     ManagedBackend
	ManagedStore       ManagedStore
	PickGameDirectory  func(context.Context) (string, error)
	ManagedPathInput   string
	InitialManagedRoot string
	managedOut         chan managedResult
	managedProgress    chan state.Event
	managedCancel      context.CancelFunc
	managedPlan        GamePlan
	gameSession        GameSession
	managedPlanRoot    string
	managedSave        chan ManagedPreferences
	managedSaveStarted bool
	Player             *ui.VideoPlayer
	Slogan             *ui.Bitmap
	Logo               *ui.Bitmap
	News               *ui.Bitmap
	Poster             *ui.Bitmap
	// Accent is the launch tone derived from the poster (PosterAccent); the
	// zero value keeps the default tone.
	Accent          ui.Color
	bannerMedia     bannerMedia
	mediaFocus      ui.Handle
	navigationFocus ui.Handle
	Minimize        func()
	Maximize        func()
	CloseWindow     func()
	theme           *ui.Theme
	clockPaused     bool
	now             time.Time
	pending         *demoOperation
	ctx             context.Context
	cancel          context.CancelFunc
	Source          Source
	// RequestFrame must be safe to call from a worker. Set it before Source
	// begins work; production binds the native window's Invalidate method.
	RequestFrame  func()
	newsOut       chan newsResult
	detailOut     chan detailResult
	newsPending   bool
	detailPending bool
	// GameDownloads performs native work through the injected system boundary.
	GameDownloads        GameDownloads
	download             downloadWork
	DownloadForm         DownloadForm
	DownloadSettingsOpen bool
	UpdateBackend        UpdateBackend
	NetworkBackend       NetworkBackend
	NetworkForm          NetworkForm
	workOut              chan state.Event
	updateProgress       chan state.Event
	updateAutoChecked    bool
	// CheckInBackend signs and reads the saved Tajiduo link. A nil backend
	// leaves the page in its unlinked state and touches no network.
	CheckInBackend CheckInBackend
	checkinOut     chan state.Event
	checkinStarted bool
	checkinVisit   bool
	// checkinAutoBusy serializes automatic-switch writes; checkinLaunch asks
	// for one automatic attempt after the user starts the game.
	checkinAutoBusy bool
	checkinLaunch   bool
	// Shown runs once, off the UI thread, after the first frame is built; the
	// installed build marks its version healthy there.
	Shown     func()
	shownDone bool
	// LoginBackend logs in for the game that is waiting; nil when none is.
	LoginBackend LoginBackend
	LoginForm    LoginForm
	// ShowLogin shows or hides the separate login window.
	ShowLogin  func(open bool)
	loginShown bool
}

type demoOperation struct {
	effect   state.Effect
	start    time.Time
	base     float64
	duration time.Duration
}

func NewShell(m state.Model) *Shell {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Shell{
		Model: m, ctx: ctx, cancel: cancel, theme: launcherTheme(), clockPaused: m.DemoPaused, FixedWindow: m.NormalMode,
		wishLink: wishLinkMedia{out: make(chan wishLinkResult, 4)},
		wish:     wishArchives{out: make(chan wishArchiveResult, 4)},
		newsOut:  make(chan newsResult, 1), detailOut: make(chan detailResult, 1),
		managedOut: make(chan managedResult, 8), managedProgress: make(chan state.Event, 1),
		managedSave: make(chan ManagedPreferences, 1),
		workOut:     make(chan state.Event, 8), updateProgress: make(chan state.Event, 1),
		checkinOut: make(chan state.Event, 8),
	}
	s.NetworkForm = networkForm(m.Network)
	if state.Active(m.Flow) {
		s.apply([]state.Effect{{Kind: state.Simulate, Flow: m.Flow, Token: m.Token}})
	}
	return s
}

func (s *Shell) Dispatch(e state.Event) {
	var fx []state.Effect
	s.Model, fx = state.Reduce(s.Model, e)
	s.apply(fx)
	s.syncLoginWindow()
}

func (s *Shell) apply(fx []state.Effect) {
	for _, f := range fx {
		if s.applyDownloadEffect(f) || s.applyWishLinkEffect(f) || s.applyWishEffect(f) || s.applyCheckInEffect(f) || s.applyManagedEffect(f) || s.applyUpdateEffect(f) || s.applyLoginEffect(f) {
			continue
		}
		switch f.Kind {
		case state.FocusMediaConsole:
			s.mediaFocus.Focus()
		case state.CancelOperation:
			s.pending = nil
		case state.Simulate:
			d := time.Second
			switch f.Flow {
			case state.Downloading:
				d = 10 * time.Second
			case state.Verifying:
				d = 2400 * time.Millisecond
			case state.Launching:
				d = 1400 * time.Millisecond
			}
			s.pending = &demoOperation{effect: f, start: s.now, base: s.Model.Progress, duration: d}
		case state.CloseWindow:
			s.closeAfterDownloads()
		case state.FetchNews:
			s.beginNews(f.Token)
		case state.FetchDetail:
			s.beginDetail(f.Token)
		}
	}
}

func (s *Shell) Dispose() {
	s.cancel()
	s.cancelManagedWork()
	s.pending = nil
	s.newsPending, s.detailPending = false, false
	if s.Player != nil {
		s.Player.Close()
		s.Player = nil
	}
}

// beginNews reads the official feed off the UI thread. The result is handed
// back through a channel that only the UI thread drains, so a slow or dead
// network can never stall a frame.
func (s *Shell) beginNews(token uint64) {
	if s.Source == nil {
		return
	}
	src, ctx, wake := s.Source, s.ctx, s.RequestFrame
	s.newsPending = true
	go func() {
		feed, err := src.Feed(ctx)
		result := newsResult{token: token}
		if err != nil {
			result.err = err
		} else {
			result.rows, result.banners = state.ProjectNews(feed), state.ProjectBanners(feed)
		}
		select {
		case s.newsOut <- result:
			if wake != nil && ctx.Err() == nil {
				wake()
			}
		case <-ctx.Done():
		}
	}()
}

// beginDetail reads one official announcement page off the UI thread. The URL
// comes from the rows the feed returned: nothing else is ever fetched.
func (s *Shell) beginDetail(token uint64) {
	if s.Source == nil {
		return
	}
	url := s.Model.DetailURL
	if url == "" {
		s.Dispatch(state.Event{Kind: state.DetailFailed, Token: token, Message: "公告地址缺失，无法读取正文"})
		return
	}
	src, ctx, wake := s.Source, s.ctx, s.RequestFrame
	s.detailPending = true
	go func() {
		detail, err := src.Detail(ctx, url)
		result := detailResult{token: token}
		if err != nil {
			result.err = err
		} else {
			result.body = strings.Join(detail.Paragraphs, "\n\n")
		}
		select {
		case s.detailOut <- result:
			if wake != nil && ctx.Err() == nil {
				wake()
			}
		case <-ctx.Done():
		}
	}()
}

// syncNews runs on the UI thread, from tick: it delivers whatever came back,
// then starts the first read of the board if nobody has started one yet.
func (s *Shell) syncNews() {
	for {
		select {
		case result := <-s.newsOut:
			s.newsPending = false
			if result.err != nil {
				s.Dispatch(state.Event{Kind: state.NewsFailed, Token: result.token, Message: state.BriefError(result.err)})
			} else {
				s.Dispatch(state.Event{Kind: state.NewsLoaded, Token: result.token, Rows: result.rows, Banners: result.banners})
			}
			continue
		case result := <-s.detailOut:
			s.detailPending = false
			if result.err != nil {
				s.Dispatch(state.Event{Kind: state.DetailFailed, Token: result.token, Message: state.BriefError(result.err)})
			} else {
				s.Dispatch(state.Event{Kind: state.DetailLoaded, Token: result.token, Body: result.body})
			}
			continue
		default:
		}
		break
	}
	if s.Source != nil && s.Model.News == state.NewsPending && !s.newsPending {
		s.Dispatch(state.Event{Kind: state.NewsRequested})
	}
}

func (s *Shell) tick(c *ui.Context) {
	previous := s.now
	s.now = c.Now()
	if s.ctx.Err() != nil {
		return
	}
	if reduced := c.Preferences().ReduceMotion; reduced != s.Model.SystemReducedMotion {
		s.Dispatch(state.Event{Kind: state.SystemMotionChanged, Enabled: reduced})
	}
	if op := s.pending; op != nil {
		if op.start.IsZero() {
			op.start = s.now
		} else if (s.Model.DemoPaused || s.clockPaused) && !previous.IsZero() {
			// Include the last frozen frame: an idle native window may have no
			// scheduled frames until the resume input arrives much later.
			op.start = op.start.Add(s.now.Sub(previous))
		}
		if !s.Model.DemoPaused {
			p := op.base + float64(s.now.Sub(op.start))/float64(op.duration)
			if p >= 1 {
				s.Dispatch(state.Event{Kind: state.Completed, Token: op.effect.Token})
			} else {
				s.Dispatch(state.Event{Kind: state.Progressed, Token: op.effect.Token, Progress: p})
			}
			if s.pending != nil {
				c.After(40 * time.Millisecond)
			}
		}
	}
	s.clockPaused = s.Model.DemoPaused
	if !s.Model.MediaPreview {
		if s.Player == nil && s.Model.Media == state.MediaLoading {
			s.Dispatch(state.Event{Kind: state.MediaFailed})
		} else if s.Player != nil {
			if s.Player.Err() != nil && s.Model.Media != state.MediaUnavailable {
				s.Dispatch(state.Event{Kind: state.MediaFailed})
			}
			if s.Player.Ready() && s.Model.Media == state.MediaLoading {
				s.Dispatch(state.Event{Kind: state.MediaReady})
			}
		}
	}
	if s.Player != nil && !s.Model.MediaPreview {
		play := s.Model.AnimateBackground()
		if play && !s.Player.Playing() {
			s.Player.Play()
		} else if !play && s.Player.Playing() {
			s.Player.Pause()
		}
	}
	s.syncNews()
	s.syncWish()
	s.syncWishImages()
	s.syncBannerMedia()
	s.syncManaged()
	s.syncDownloads()
	s.syncDownloadProcesses(c)
	s.syncDownloadOffers(c)
	s.syncUpdate()
	s.syncCheckIn(c)
}

func (s *Shell) View(c *ui.Context) {
	tone := s.tone()
	s.theme.Accent, s.theme.Focus, s.theme.InactiveAccent = tone, tone, tone
	c.SetTheme(s.theme)
	s.tick(c)
	if c.Shortcut(0, ui.KeyF8) && !c.ShortcutRepeated() {
		if s.Model.Overlay == state.Scenarios {
			s.Dispatch(state.Event{Kind: state.Dismiss})
		} else if s.Model.Overlay == state.NoOverlay {
			s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.Scenarios})
		}
	}
	root := ui.Box(c.Key("launcher")).Fill().Clip().Background(bezel).Font("Microsoft YaHei UI").TextColor(paper)
	s.updateMediaBar(c, root)
	root.Children(func() {
		s.stage(c)
		if s.Model.Page == state.Home {
			s.hero(c)
			s.action(c)
		} else {
			if s.Model.Page == state.WishAnalysis {
				s.wishPage(c, s.wishProjection())
			} else {
				s.checkInPage(c, s.checkInProjection())
			}
			s.footer(c)
		}
		s.topbar(c)
		s.mediaBar(c)
		// Fixed side chrome must remain clickable above the revealed fullwidth
		// media lane. Modal isolation is applied last by overlay below.
		s.navigation(c)
	})
	s.overlay(c)
	if !s.shownDone {
		s.shownDone = true
		if s.Shown != nil {
			go s.Shown()
		}
	}
}
