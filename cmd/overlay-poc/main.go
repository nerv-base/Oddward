//go:build windows

// Command overlay-poc is the MyGo Native UI overlay proof of concept. It is a
// separate experimental entry point: it never touches the production login
// flow, the account store or the SDK host.
//
// Use cmd/overlay-host as the stand-in game window:
//
//	overlay-host -status host.json -control 47001
//	overlay-poc -host <hwnd> -mode owner -control 47002
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/nerv-base/Oddward/internal/overlaywin"
)

type poc struct {
	mu sync.Mutex

	win  *mygo.Window
	hwnd uintptr

	identity overlaywin.Identity
	mode     string // owner or child
	watcher  *overlaywin.Watcher

	screen   string // login or banner
	phone    string
	captcha  string
	remember bool
	status   string

	banner         string
	bannerAlpha    float64
	bannerDone     chan struct{}
	bannerSelfExit bool
	bannerEx       uintptr

	focused      string
	selPhone     [2]int
	selCaptcha   [2]int
	focusRequest string
	restoreFocus bool
	restoreSel   bool

	bound      bool
	visible    bool
	hostClient overlaywin.Rect
	activeHost uintptr
	recreates  int
	hooksSeen  uint64
	lastError  string
	recreating bool
	quitOnce   sync.Once
}

func main() {
	var (
		hostFlag = flag.Uint64("host", 0, "target host HWND")
		hostPID  = flag.Uint("host-pid", 0, "target host PID (used when -host is 0)")
		hostMade = flag.String("host-created", "", "expected host creation time (decimal FILETIME)")
		mode     = flag.String("mode", "owner", "binding mode: owner or child")
		control  = flag.Int("control", 0, "control TCP port on 127.0.0.1")
		banner   = flag.String("banner", "", "start in welcome-banner mode with this text")
		duration = flag.Duration("duration", 0, "exit after this duration")
	)
	flag.Parse()

	p := &poc{screen: "login", mode: *mode}
	if *banner != "" {
		p.screen = "banner"
		p.banner = *banner
		p.bannerSelfExit = true
	}
	// The overlay window is recreated whenever the host HWND is; closing the
	// only window must not end the process.
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.WhenReady(func() {
		if err := p.start(uintptr(*hostFlag), uint32(*hostPID), *hostMade); err != nil {
			log.Fatal(err)
		}
		if err := p.watcher.Install(); err != nil {
			log.Fatal(err)
		}
		if *control != 0 {
			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *control))
			if err != nil {
				log.Fatalf("control listen: %v", err)
			}
			go p.serveControl(ln)
		}
		go func() {
			t := time.NewTicker(50 * time.Millisecond)
			defer t.Stop()
			for range t.C {
				mygo.RunOnMain(p.tick)
			}
		}()
		if *duration > 0 {
			go func() {
				time.Sleep(*duration)
				p.quit()
			}()
		}
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

func (p *poc) start(hostHWND uintptr, hostPID uint32, creation string) error {
	if hostHWND == 0 && hostPID != 0 {
		hostHWND = overlaywin.FindMainWindow(hostPID)
	}
	if hostHWND == 0 {
		return fmt.Errorf("overlay: no target host window")
	}
	pid := overlaywin.ProcessOfWindow(hostHWND)
	if pid == 0 {
		return fmt.Errorf("overlay: host %#x has no process", hostHWND)
	}
	if creation == "" {
		return fmt.Errorf("overlay: -host-created is required")
	}
	h, id, err := overlaywin.OpenIdentity(pid)
	if err != nil {
		return err
	}
	defer func() { _ = overlaywin.CloseIdentity(h) }()
	want, err := overlaywin.ParseCreationTime(creation)
	if err != nil {
		return err
	}
	if id.CreationTime != want {
		return fmt.Errorf("overlay: host creation time %d, want %d", id.CreationTime, want)
	}
	p.identity = id
	w, err := overlaywin.NewWatcher(id)
	if err != nil {
		return err
	}
	p.watcher = w

	p.win = nil
	if err := p.createWindow(hostHWND); err != nil {
		return err
	}
	return nil
}

// createWindow creates the MyGo window, binds it to the current host window
// and shows it without activation. It must run on the UI thread.
func (p *poc) createWindow(hostHWND uintptr) error {
	client, err := overlaywin.ClientRectOf(hostHWND)
	if err != nil {
		return err
	}
	var w *mygo.Window
	create := func() error {
		w = mygo.NewWindow(mygo.WindowOptions{
			Title:         "Oddward Overlay PoC",
			Width:         int(client.W),
			Height:        int(client.H),
			X:             int(client.X),
			Y:             int(client.Y),
			Frameless:     true,
			Transparent:   true,
			SkipTaskbar:   true,
			AlwaysOnTop:   false,
			DisableResize: true,
			Hidden:        true,
			Content:       ui.View(p.view),
		})
		return nil
	}
	if err := overlaywin.WithThreadDPI(hostHWND, create); err != nil {
		return err
	}
	if w == nil {
		return fmt.Errorf("overlay: window creation failed")
	}
	hwnd := w.NativeHandle()
	if hwnd == 0 {
		return fmt.Errorf("overlay: window has no HWND")
	}
	if err := p.bind(hostHWND, hwnd); err != nil {
		w.Close()
		return err
	}
	p.win = w
	p.hwnd = hwnd
	p.activeHost = hostHWND
	p.bound = true
	p.place(hostHWND, hwnd)
	w.ShowInactive()
	p.visible = true
	if p.screen == "banner" {
		p.applyBanner()
	}
	return nil
}

func (p *poc) bind(hostHWND, hwnd uintptr) error {
	switch p.mode {
	case "owner":
		return overlaywin.SetOwner(hwnd, hostHWND)
	case "child":
		client, err := overlaywin.ClientRectOf(hostHWND)
		if err != nil {
			return err
		}
		return overlaywin.SetChild(hwnd, hostHWND, client)
	default:
		return fmt.Errorf("overlay: unknown mode %q", p.mode)
	}
}

func (p *poc) place(hostHWND, hwnd uintptr) {
	client, err := overlaywin.ClientRectOf(hostHWND)
	if err != nil || client.Empty() {
		return
	}
	if p.mode == "child" {
		_ = overlaywin.SetPos(hwnd, overlaywin.Rect{W: client.W, H: client.H}, overlaywin.SWPFrameChanged)
		return
	}
	_ = overlaywin.SetPos(hwnd, client, 0)
	p.hostClient = client
}

// tick reconciles the overlay with the host window. Called on the UI thread.
func (p *poc) tick() {
	if p.recreating {
		return
	}
	st := p.watcher.State()
	p.hooksSeen = p.watcher.CallbackCount()
	if !st.Valid {
		p.recreateOverlay()
		return
	}
	if p.activeHost != 0 && st.HWND != p.activeHost {
		p.recreateOverlay()
		return
	}
	if st.Minimized || !st.Visible {
		if p.visible && p.win != nil {
			p.win.Hide()
			p.visible = false
		}
		return
	}
	if p.win == nil || p.hwnd == 0 {
		return
	}
	if !p.visible {
		p.win.ShowInactive()
		p.visible = true
	}
	p.place(st.HWND, p.hwnd)
}

// recreateOverlay destroys the overlay window and creates a new one after the
// game HWND disappears, preserving the in-memory form and caret selection.
func (p *poc) recreateOverlay() {
	p.recreating = true
	p.recreates++
	defer func() { p.recreating = false }()
	if p.win != nil {
		p.win.Close()
		p.win = nil
		p.hwnd = 0
		p.visible = false
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st := p.watcher.State()
		if st.Valid && st.Visible && !st.Minimized {
			p.restoreFocus = p.focused != ""
			p.restoreSel = true
			if err := p.createWindow(st.HWND); err != nil {
				p.lastError = err.Error()
				return
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	p.lastError = "overlay: host window did not come back"
}

func (p *poc) view(c *ui.Context) {
	t := ui.DarkTheme()
	t.Background = ui.RGBA(0, 0, 0, 0)
	t.Surface = ui.RGBA(0xFF, 0xFF, 0xFF, 1)
	t.SurfaceHover = ui.RGBA(0xF7, 0xF7, 0xF7, 1)
	t.SurfacePressed = ui.RGBA(0xE2, 0xE2, 0xE2, 1)
	t.Border = ui.RGBA(0xD0, 0xD1, 0xD3, 1)
	t.Text = ui.RGBA(0x2A, 0x2B, 0x2E, 1)
	t.TextMuted = ui.RGBA(0x8A, 0x8C, 0x90, 1)
	t.Accent = ui.RGBA(0xFD, 0xFD, 0xFD, 1)
	t.AccentHover = ui.RGBA(0xFF, 0xFF, 0xFF, 1)
	t.AccentPressed = ui.RGBA(0xE2, 0xE2, 0xE2, 1)
	t.AccentText = ui.RGBA(0x1F, 0x20, 0x23, 1)
	t.Selection = ui.RGBA(0x8E, 0x90, 0x94, 1)
	c.SetTheme(t)

	if p.screen == "banner" {
		p.bannerView(c)
		return
	}
	p.loginView(c)
}

func (p *poc) loginView(c *ui.Context) {
	// Semi-transparent full-screen mask, like the WPF Grid mask (alpha 166).
	ui.Column(c).Fill().Justify(ui.Center).AlignItems(ui.Center).Background(ui.RGBA(0, 0, 0, 0.65)).Children(func() {
		ui.Column(c).Width(420).Height(520).Center().Radius(18).
			Background(ui.RGBA(0x2E, 0x2F, 0x33, 1)).
			Border(2, ui.RGBA(0x4B, 0x4C, 0x51, 1)).
			Padding(6).Children(func() {
			ui.Column(c).Fill().Radius(12).Clip().
				Background(ui.RGBA(0xE3, 0xE3, 0xE3, 1)).Children(func() {
				p.header(c)
				p.form(c)
			})
		})
	})
}

func (p *poc) header(c *ui.Context) {
	ui.Row(c).FillWidth().Height(64).Background(ui.RGBA(0x1E, 0x1F, 0x22, 1)).
		Padding(14, 0).Gap(10).Children(func() {
		ui.Button(c, "←").Label("返回").Width(36).Height(32)
		ui.Spacer(c)
		ui.Text(c, "ODDWARD").FontSize(18).Bold().TextColor(ui.RGBA(0xFF, 0xFF, 0xFF, 1))
		ui.Text(c, "非官方").FontSize(10).TextColor(ui.RGBA(0x9A, 0x9C, 0xA0, 1))
		ui.Spacer(c)
		if ui.Button(c, "✕").Label("关闭").Width(36).Height(32).Clicked() {
			go p.quit()
		}
	})
}

func (p *poc) form(c *ui.Context) {
	ui.Column(c).FillWidth().Padding(32, 24).Gap(12).Children(func() {
		ui.Text(c, "手机号登录").FontSize(17).Bold()
		ui.Text(c, "登录后启动《异环》").FontSize(12).TextColor(ui.RGBA(0x8A, 0x8C, 0x90, 1))

		phone := ui.TextInput(c, &p.phone).Placeholder("手机号").Label("手机号").MaxLength(11)
		if p.restoreFocus && p.focusRequest == "" {
			p.focusRequest = p.focused
		}
		if p.focusRequest == "phone" {
			phone.Focus()
			p.focusRequest = ""
			if p.restoreSel {
				phone.SetTextSelection(p.selPhone[0], p.selPhone[1])
			}
		}
		if phone.Focused() {
			p.focused = "phone"
			p.selPhone[0], p.selPhone[1] = phone.TextSelection()
		}

		ui.Row(c).FillWidth().Gap(8).Children(func() {
			captcha := ui.TextInput(c, &p.captcha).Placeholder("验证码").Label("验证码").MaxLength(8)
			if p.focusRequest == "captcha" {
				captcha.Focus()
				p.focusRequest = ""
				if p.restoreSel {
					captcha.SetTextSelection(p.selCaptcha[0], p.selCaptcha[1])
				}
			}
			if captcha.Focused() {
				p.focused = "captcha"
				p.selCaptcha[0], p.selCaptcha[1] = captcha.TextSelection()
			}
			if ui.Button(c, "获取验证码").Label("获取验证码").Width(120).Clicked() {
				p.note("验证码已发送（PoC 不发请求）")
			}
		})
		ui.Checkbox(c, &p.remember, "在此设备保存账号").Label("在此设备保存账号")

		if ui.PrimaryButton(c, "登录").Label("登录").Clicked() {
			if p.phone == "" {
				p.note("请输入手机号")
			} else {
				p.note("PoC：表单已提交到本地，不会连接登录服务")
			}
		}
		if ui.Button(c, "其他账号登录").Label("其他账号登录").Clicked() {
			p.note("PoC：账号列表未接入")
		}
		if p.status != "" {
			ui.Text(c, p.status).FontSize(12).TextColor(ui.RGBA(0x8A, 0x8C, 0x90, 1))
		}
		ui.Spacer(c)
		ui.Text(c, "MyGo Native UI overlay PoC").FontSize(10).TextColor(ui.RGBA(0x9A, 0x9C, 0xA0, 1))
	})
}

func (p *poc) bannerView(c *ui.Context) {
	ui.Column(c).Fill().Padding(24, 48, 24, 0).AlignItems(ui.Center).Background(ui.RGBA(0, 0, 0, 0)).Children(func() {
		ui.Row(c).Padding(18, 10, 22, 10).Gap(10).Radius(22).
			Background(ui.RGBA(0xE3, 0xE3, 0xE3, 0.92)).
			Border(1, ui.RGBA(0x3A, 0x3B, 0x3F, 1)).
			Center().Children(func() {
			ui.Column(c).Width(18).Height(18).Radius(4).
				Border(2, ui.RGBA(0x2A, 0x2B, 0x2E, 1))
			ui.Text(c, p.banner).FontSize(15).Bold().TextColor(ui.RGBA(0x2A, 0x2B, 0x2E, 1))
		})
	})
}

func (p *poc) note(s string) {
	p.status = s
}

func (p *poc) applyBanner() {
	if p.win == nil {
		return
	}
	if before, err := overlaywin.MakeBannerStyle(p.hwnd); err == nil {
		p.bannerEx = before
	}
	p.win.SetIgnoreMouseEvents(true)
	p.win.SetOpacity(0)
	p.bannerAlpha = 0
	go p.animateBanner()
}

func (p *poc) animateBanner() {
	fade := func(from, to float64, d time.Duration) {
		steps := int(d / (16 * time.Millisecond))
		if steps < 1 {
			steps = 1
		}
		for i := 1; i <= steps; i++ {
			v := from + (to-from)*float64(i)/float64(steps)
			mygo.RunOnMain(func() {
				if p.win != nil {
					p.win.SetOpacity(v)
				}
				p.mu.Lock()
				p.bannerAlpha = v
				p.mu.Unlock()
			})
		}
	}
	if overlaywin.AnimationsEnabled() {
		fade(0, 1, 220*time.Millisecond)
		time.Sleep(2400 * time.Millisecond)
		fade(1, 0, 380*time.Millisecond)
	} else {
		mygo.RunOnMain(func() {
			if p.win != nil {
				p.win.SetOpacity(1)
			}
			p.mu.Lock()
			p.bannerAlpha = 1
			p.mu.Unlock()
		})
		time.Sleep(2400 * time.Millisecond)
	}
	mygo.RunOnMain(func() {
		if p.win != nil {
			p.win.Hide()
		}
		p.visible = false
		if !p.bannerSelfExit {
			p.mu.Lock()
			p.screen = "login"
			p.mu.Unlock()
		}
	})
	if p.bannerDone != nil {
		close(p.bannerDone)
	}
	if p.bannerSelfExit {
		p.quit()
	}
}

func (p *poc) serveControl(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			sc := bufio.NewScanner(conn)
			for sc.Scan() {
				b, _ := json.Marshal(p.control(sc.Text()))
				_, _ = conn.Write(append(b, '\n'))
			}
		}()
	}
}

func (p *poc) control(line string) map[string]any {
	var cmd string
	fmt.Sscanf(line, "%s", &cmd)
	arg := ""
	if i := len(cmd); i < len(line) {
		arg = line[i:]
		for len(arg) > 0 && (arg[0] == ' ' || arg[0] == '\t') {
			arg = arg[1:]
		}
	}
	switch cmd {
	case "state":
		return p.snapshot()
	case "rect":
		r, _ := overlaywin.RectOf(p.hwnd)
		return map[string]any{"ok": true, "hwnd": p.hwnd, "rect": r, "visible": overlaywin.Visible(p.hwnd)}
	case "host":
		st := p.watcher.State()
		return map[string]any{"ok": true, "hwnd": st.HWND, "client": st.Client, "minimized": st.Minimized, "visible": st.Visible}
	case "hooks":
		return map[string]any{"ok": true, "callbacks": p.watcher.CallbackCount()}
	case "focus":
		mygo.RunOnMain(func() { p.focusRequest = arg })
		return map[string]any{"ok": true}
	case "banner":
		if arg == "off" {
			mygo.RunOnMain(func() {
				if p.win != nil {
					p.win.Hide()
				}
				p.visible = false
			})
			return map[string]any{"ok": true}
		}
		mygo.RunOnMain(func() {
			p.banner = arg
			p.screen = "banner"
			p.applyBanner()
		})
		return map[string]any{"ok": true}
	case "opacity":
		v, _ := strconv.ParseFloat(arg, 64)
		mygo.RunOnMain(func() {
			if p.win != nil {
				p.win.SetOpacity(v)
			}
		})
		return map[string]any{"ok": true}
	case "quit":
		go p.quit()
		return map[string]any{"ok": true}
	}
	return map[string]any{"ok": false, "error": "unknown command"}
}

func (p *poc) snapshot() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, _ := overlaywin.RectOf(p.hwnd)
	style, _ := overlaywin.Style(p.hwnd)
	ex, _ := overlaywin.ExStyle(p.hwnd)
	return map[string]any{
		"ok": true, "hwnd": p.hwnd, "mode": p.mode,
		"screen": p.screen, "phone": p.phone, "captcha": p.captcha,
		"remember": p.remember, "focused": p.focused,
		"sel_phone": p.selPhone, "sel_captcha": p.selCaptcha,
		"banner": p.banner, "opacity": p.bannerAlpha,
		"visible": p.visible, "bound": p.bound,
		"host_hwnd": p.watcher.State().HWND,
		"rect":      r, "hooks": p.watcher.CallbackCount(),
		"style": style, "ex": ex, "recreates": p.recreates,
		"error": p.lastError,
	}
}

func (p *poc) quit() {
	p.quitOnce.Do(func() {
		mygo.RunOnMain(func() {
			if p.watcher != nil {
				p.watcher.Uninstall()
			}
			// The login overlay is transient; closing it returns keyboard
			// focus to the game window it covered. The welcome banner never
			// takes focus, so it leaves the foreground window alone.
			if p.screen != "banner" && p.activeHost != 0 && overlaywin.Visible(p.activeHost) && !overlaywin.Minimized(p.activeHost) {
				overlaywin.SetForeground(p.activeHost)
			}
			if p.win != nil {
				p.win.Close()
			}
			mygo.App.Quit()
		})
	})
	os.Exit(0)
}
