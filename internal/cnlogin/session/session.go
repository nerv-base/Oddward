// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package session

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/nerv-base/Oddward/internal/cnlogin/accounts"
	"github.com/nerv-base/Oddward/internal/cnlogin/broker"
	"github.com/nerv-base/Oddward/internal/cnlogin/cnproto"
	"github.com/nerv-base/Oddward/internal/cnlogin/laohu"
	"github.com/nerv-base/Oddward/internal/cnlogin/launch"
	"github.com/nerv-base/Oddward/internal/cnlogin/loginui"
	"github.com/nerv-base/Oddward/internal/cnlogin/sdkhost"
	"github.com/nerv-base/Oddward/internal/process"
)

var (
	// ErrStarted reports a second Start on one plan.
	ErrStarted = errors.New("cnlogin/session: plan already started")
	// ErrClosed reports use of a closed plan or session.
	ErrClosed = errors.New("cnlogin/session: closed")
)

// Accounts is the saved-account surface the login flow needs. *accounts.Store
// implements it.
type Accounts interface {
	List() ([]accounts.Account, int, error)
	Unlock(id string) (accounts.Secret, error)
	Last() (string, error)
	SetLast(id string) error
	Rename(id, label string) error
	Remove(id string) error
	Prepare(a accounts.Account, s accounts.Secret) (accounts.Pending, error)
	NoticeAcknowledged(revision int) (bool, error)
	AcceptNotice(revision int) error
}

// HostEventKind classifies one game-to-host event. The values mirror
// sdkhost.EventKind but keep the session testable without the Windows layer.
type HostEventKind uint8

const (
	HostRegistered HostEventKind = iota + 1
	HostQuery
	HostLoginRequested
	HostLogout
	HostFailure
)

// HostEvent is one decoded game-to-host event.
type HostEvent struct {
	Kind HostEventKind
	// Key is the registration-derived application key (HostRegistered).
	Key laohu.AppKey
	// ConfigKey decrypts ResData\1289.dat (HostRegistered). It is the app-key
	// tail the registration frame carried.
	ConfigKey [16]byte
	// Query carries the game's pre-login query (HostQuery).
	Query cnproto.Query
	// Err reports a protocol failure (HostFailure).
	Err error
}

// Host is the SDK IPC surface owned by the session. hostAdapter adapts
// *sdkhost.Host to it.
type Host interface {
	PipeName() string
	BindGame(pid uint32, createdAt uint64) error
	Events() <-chan HostEvent
	SendLogin(ctx context.Context, id cnproto.LoginIdentity) error
	Reply(ctx context.Context, r cnproto.PreLoginReply) error
	CompleteLoginRequest()
	Close() error
}

// Game is the started game process. *launch.Game implements it.
type Game interface {
	Identity() process.Identity
	Wait(ctx context.Context) (uint32, error)
	Close() error
}

// UI is one login window. *loginui.Session implements it.
type UI interface {
	Update(ctx context.Context, m loginui.Model) error
	Events() <-chan loginui.Event
	Close() error
}

// Config is the launcher-supplied input of one native CN login session.
type Config struct {
	// Root is the game installation root. It is inspected and locked for the
	// duration of the plan.
	Root string
	// ElevatedHost moves only SDK IPC and game creation to the same executable's
	// restricted elevated mode. Accounts, HTTP and the login UI stay here.
	ElevatedHost bool
	// LoginUI is the path of the authored WPF login helper executable.
	LoginUI string
	// HostExecutable is the executable the game reports as its launcher. It
	// defaults to the running process.
	HostExecutable string
	// Accounts is the saved-account store. It is required.
	Accounts Accounts
	// HTTPClient is the shared launcher HTTP client. A default is used when
	// nil.
	HTTPClient *http.Client
	// LauncherVersion is reported to the login service when the installation
	// has no readable ResVersion. It must normalize to 2–4 numeric parts;
	// anything else is reported as 0.0.0.0.
	LauncherVersion string
	// OSVersion is reported as the operating system. A Windows 10 default is
	// used when empty.
	OSVersion string
	// Now is the clock. It defaults to time.Now.
	Now func() time.Time
}

// Plan is a prepared native CN login: the installation is inspected, no
// process is running, and the host and game can be started.
type Plan struct {
	cfg      Config
	prepared *launch.Prepared
	info     launch.Info

	mu      sync.Mutex
	started bool
	closed  bool
}

// Prepare inspects root and returns a plan that starts the native login flow.
func Prepare(ctx context.Context, cfg Config) (*Plan, error) {
	if cfg.Accounts == nil {
		return nil, errors.New("cnlogin/session: accounts store is required")
	}
	if cfg.Root == "" {
		return nil, errors.New("cnlogin/session: game root is required")
	}
	prepared, err := launch.Prepare(ctx, cfg.Root)
	if err != nil {
		return nil, err
	}
	return &Plan{cfg: cfg, prepared: prepared, info: prepared.Info()}, nil
}

// Summary describes the plan for the launcher's compatibility line.
func (p *Plan) Summary() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	info := p.info
	return "国服原生登录：" + info.Profile.Name + "（SDK " + info.Profile.SDKVersion +
		"，NTEGame.exe " + shortHash(info.Profile.LauncherSHA256) + "…）"
}

// Start starts the SDK host and the game, then returns the running session.
//
// The game may already be running when the SDK binding fails; that failure is
// kept in the session so the caller can still observe and close the game. Start
// therefore returns a non-nil session with a nil error once the game process
// exists.
func (p *Plan) Start(ctx context.Context) (*Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, ErrClosed
	}
	if p.started {
		return nil, ErrStarted
	}
	if p.cfg.ElevatedHost {
		// The elevated process revalidates the installation and owns the single
		// instance gate before creating either the SDK host or the game.
		if err := p.prepared.Close(); err != nil {
			return nil, err
		}
		client, err := broker.Start(ctx, p.cfg.Root)
		if err != nil {
			return nil, err
		}
		p.started = true
		p.info.InstalledVersion = client.InstalledVersion()
		s := newSession(p.deps(newBrokerHost(client), client))
		go s.loop()
		return s, nil
	}
	hostExecutable := p.cfg.HostExecutable
	if hostExecutable == "" {
		executable, err := os.Executable()
		if err != nil {
			return nil, err
		}
		hostExecutable = executable
	}
	host, err := sdkhost.New(ctx, sdkhost.Options{Profile: p.prepared.Info().Profile, HostExecutable: hostExecutable})
	if err != nil {
		return nil, err
	}
	game, gameErr := p.prepared.Start(ctx, launch.Options{
		PipeName:           host.PipeName(),
		ConnectedClientPID: host.ConnectedClientPID,
	})
	if game == nil {
		_ = host.Close()
		if gameErr == nil {
			gameErr = errors.New("cnlogin/session: the game was not created")
		}
		return nil, gameErr
	}
	p.started = true
	s := newSession(p.deps(newHostAdapter(host), game))
	if gameErr != nil {
		s.st.setupErr = gameErr
	} else {
		identity := game.Identity()
		if err := s.deps.host.BindGame(identity.PID, identity.CreationTime); err != nil {
			s.st.setupErr = err
		}
	}
	go s.loop()
	return s, nil
}

// Close releases an unused plan. It does not stop a started session.
func (p *Plan) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	return p.prepared.Close()
}

// deps binds the real Windows implementations to the login flow.
func (p *Plan) deps(host Host, game Game) deps {
	now := p.cfg.Now
	if now == nil {
		now = time.Now
	}
	client := p.cfg.HTTPClient
	uiConfig := loginui.Config{Executable: p.cfg.LoginUI}
	if binder, ok := host.(interface {
		BindOverlay(context.Context, process.Identity, uintptr, uintptr) error
	}); ok {
		uiConfig.BindWindow = binder.BindOverlay
	}
	if binder, ok := host.(interface {
		EmbedOverlay(context.Context, process.Identity, uintptr, uintptr) error
	}); ok {
		uiConfig.EmbedWindow = binder.EmbedOverlay
	}
	return deps{
		host:     host,
		game:     game,
		accounts: p.cfg.Accounts,
		ui: func(ctx context.Context, model loginui.Model) (UI, error) {
			return loginui.Start(ctx, uiConfig, model)
		},
		welcome: func(ctx context.Context, model loginui.Model) (UI, error) {
			return loginui.Start(ctx, uiConfig, model)
		},
		loadConfig: func(ctx context.Context, key [16]byte) (laohu.Config, error) {
			return loadBootConfig(ctx, p.cfg.Root, key)
		},
		newClient: func(ctx context.Context, cfg laohu.Config, key laohu.AppKey, deviceID string) (*laohu.Client, error) {
			c, err := laohu.NewClient(cfg, laohu.Options{
				HTTPClient:       client,
				InstalledVersion: p.installedVersion(),
				DeviceID:         deviceID,
				OSVersion:        p.osVersion(),
				AppKey:           key,
				Now:              now,
			})
			if err != nil {
				return nil, err
			}
			if err := c.Init(ctx); err != nil {
				c.Close()
				return nil, err
			}
			return c, nil
		},
		now: now,
	}
}

// deps is the seam between the login flow and its Windows implementations.
type deps struct {
	host       Host
	game       Game
	accounts   Accounts
	ui         func(ctx context.Context, model loginui.Model) (UI, error)
	welcome    func(ctx context.Context, model loginui.Model) (UI, error)
	loadConfig func(ctx context.Context, key [16]byte) (laohu.Config, error)
	newClient  func(ctx context.Context, cfg laohu.Config, key laohu.AppKey, deviceID string) (*laohu.Client, error)
	now        func() time.Time
}

// Session runs one game with its login flow.
type Session struct {
	deps deps

	ctx      context.Context
	cancel   context.CancelFunc
	loopDone chan struct{}

	// completions carries state mutations from worker goroutines back to the
	// loop goroutine, which owns st, ui and uiEvents.
	completions chan func()

	st       loginState
	ui       UI
	uiEvents <-chan loginui.Event

	closeOnce sync.Once
	closeErr  error
}

func newSession(d deps) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	return &Session{
		deps:        d,
		ctx:         ctx,
		cancel:      cancel,
		loopDone:    make(chan struct{}),
		completions: make(chan func()),
	}
}

// Wait waits for the game to exit and stops the login flow first.
func (s *Session) Wait(ctx context.Context) (uint32, error) {
	code, err := s.deps.game.Wait(ctx)
	if err == nil {
		s.stop()
	}
	return code, err
}

// Close stops the login flow and releases the game handle and SDK host.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.stop()
		s.closeErr = errors.Join(s.deps.host.Close(), s.deps.game.Close())
	})
	return s.closeErr
}

func (s *Session) stop() {
	s.cancel()
	<-s.loopDone
}

// loop serializes all state changes: host events, login-window events and
// worker completions arrive here.
func (s *Session) loop() {
	defer close(s.loopDone)
	hostEvents := s.deps.host.Events()
	smsTicker := time.NewTicker(time.Second)
	defer smsTicker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			s.closeUI()
			// The banner is decoration; it must never outlive the session.
			s.closeBanner()
			// The login state belongs to this goroutine, so the HTTP client is
			// released here rather than from Close on the caller's thread.
			if s.st.client != nil {
				s.st.client.Close()
				s.st.client = nil
			}
			s.st.key.Zero()
			s.st.cfg = laohu.Config{}
			return
		case event, ok := <-hostEvents:
			if !ok {
				hostEvents = nil
				continue
			}
			s.handleHost(event)
		case event, ok := <-s.uiEvents:
			if !ok {
				// The helper exited without a final event (for example after a
				// crash). Treat it like closing the window so a later login
				// request can open a fresh one.
				s.endAction()
				s.ui, s.uiEvents = nil, nil
				s.deps.host.CompleteLoginRequest()
				continue
			}
			s.handleUI(event)
		case apply := <-s.completions:
			apply()
		case <-smsTicker.C:
			s.refreshSMS()
		}
	}
}

// async runs work on a worker goroutine and applies its result on the loop.
// work returns nil when it has nothing to apply.
func (s *Session) async(work func(ctx context.Context) func()) {
	s.asyncContext(s.ctx, work)
}

// asyncContext is async with a caller-owned context, used for work the login
// window can cancel.
func (s *Session) asyncContext(ctx context.Context, work func(ctx context.Context) func()) {
	go func() {
		apply := work(ctx)
		if apply == nil {
			return
		}
		select {
		case s.completions <- apply:
		case <-ctx.Done():
		}
	}()
}

// beginAction cancels any previous login and returns a context for a new one.
// Only the loop goroutine calls it.
func (s *Session) beginAction() context.Context {
	if s.st.action != nil {
		s.st.action()
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.st.action = cancel
	return ctx
}

// endAction cancels an in-flight login and forgets it.
func (s *Session) endAction() {
	if s.st.action != nil {
		s.st.action()
		s.st.action = nil
	}
}

func (s *Session) handleHost(event HostEvent) {
	switch event.Kind {
	case HostRegistered:
		s.registered(event)
	case HostLoginRequested:
		s.loginRequested()
	case HostQuery:
		s.replyQuery(event.Query)
	case HostLogout:
		s.logout()
	case HostFailure:
		if s.st.client == nil {
			s.st.setupErr = event.Err
		}
		s.st.status = "与游戏的连接出现异常，登录可能不可用。"
		s.applyModel()
	}
}

func (s *Session) loginRequested() {
	// A delivered login already answers this request. The welcome banner may
	// still be on screen, so check this before the window handle.
	if s.st.delivered {
		s.deps.host.CompleteLoginRequest()
		return
	}
	if s.ui != nil {
		return
	}
	// Every request is a new login attempt: never reuse an admission decision
	// cached from an earlier delivery (the service state may have changed).
	if s.st.admission != nil {
		s.st.admission.Reset()
	}
	s.st.busy = s.st.setupErr != nil
	s.st.status = setupMessage(s.st.setupErr)
	if s.st.screen == "" || s.st.screen == "welcome" {
		s.st.screen = "phone"
	}
	if s.st.autoLogin && s.st.setupErr == nil && s.st.client != nil {
		// The launcher already selected this account; log it in without asking.
		s.st.autoLogin = false
		s.st.screen = "accounts"
		s.st.selected = s.st.lastID
		s.loginSaved(s.st.selected)
		return
	}
	s.st.autoLogin = false
	s.openUI()
}

func (s *Session) openUI() {
	model := s.model()
	ui, err := s.deps.ui(s.ctx, model)
	if err != nil {
		s.st.status = "无法打开登录窗口，请重试。"
		s.st.busy = false
		s.deps.host.CompleteLoginRequest()
		s.applyModel()
		return
	}
	s.ui = ui
	s.uiEvents = ui.Events()
	s.st.shownSMSLabel = model.SMSLabel
}

func (s *Session) closeUI() {
	s.endAction()
	s.st.pendingQuery = nil
	if s.ui == nil {
		return
	}
	_ = s.ui.Close()
	s.ui, s.uiEvents = nil, nil
	s.deps.host.CompleteLoginRequest()
}

func (s *Session) replyQuery(query cnproto.Query) {
	if query.Type != cnproto.QueryPreLogin {
		return
	}
	if s.st.preLogin == nil {
		s.st.pendingQuery = &query
		return
	}
	s.st.pendingQuery = nil
	pre := *s.st.preLogin
	reply := cnproto.PreLoginReply{Code: int32(pre.Code), ContextID: query.ContextID, Message: pre.Message, Body: pre.Body}
	ctx := s.ctx
	go func() {
		_ = s.deps.host.Reply(ctx, reply)
	}()
	// Queries can arrive after the login window closed. Keep the cached result
	// and the session context alive independently of the window's login action.
}

func (s *Session) logout() {
	s.closeUI()
	s.closeBanner()
	if s.st.admission != nil {
		s.st.admission.Reset()
	}
	if s.st.preLogin != nil {
		s.st.preLogin.Body = nil
	}
	s.st.preLogin = nil
	s.st.pendingQuery = nil
	s.st.delivered = false
	// An in-game logout is an explicit choice; the next request asks again.
	s.st.autoLogin = false
	s.st.welcome = ""
	if s.st.screen == "welcome" {
		s.st.screen = s.initialScreen()
	}
	s.st.busy = false
	s.st.status = ""
}

func shortHash(hash string) string {
	if len(hash) <= 8 {
		return hash
	}
	return hash[:8]
}
