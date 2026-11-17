// SPDX-License-Identifier: GPL-3.0-or-later

// Launcher uses the normal locate -> native CN login -> explicit launch flow.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/video"

	"github.com/nerv-base/Oddward/internal/branding"
	"github.com/nerv-base/Oddward/internal/buildinfo"
	"github.com/nerv-base/Oddward/internal/checkin"
	"github.com/nerv-base/Oddward/internal/cnlogin/accounts"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
	"github.com/nerv-base/Oddward/internal/launcher/system"
	"github.com/nerv-base/Oddward/internal/launchsettings"
	"github.com/nerv-base/Oddward/internal/netcfg"
	"github.com/nerv-base/Oddward/internal/news"
	"github.com/nerv-base/Oddward/internal/tajiduo"
)

func main() {
	// Handle the restricted SDK host before opening account stores or starting
	// MyGo. Only this child process receives a UAC elevation request.
	if handled, err := system.RunNativeHost(context.Background(), os.Args[1:]); handled {
		if err != nil {
			log.Print("SDK host stopped before completing its session")
			os.Exit(1)
		}
		return
	}
	background := flag.String("background", os.Getenv("ODDWARD_BACKGROUND"), "read-only local background video; never bundled")
	poster := flag.String("background-still", os.Getenv("ODDWARD_BACKGROUND_STILL"), "read-only static fallback; defaults to video's bg_0.png sibling")
	slogan := flag.String("slogan", os.Getenv("ODDWARD_SLOGAN"), "read-only local campaign artwork; never bundled")
	logo := flag.String("logo", os.Getenv("ODDWARD_LOGO"), "read-only official game logo; never bundled")
	newsArt := flag.String("news-art", os.Getenv("ODDWARD_NEWS_ART"), "optional read-only story fallback; never bundled")
	gameRoot := flag.String("game-root", "", "initial selected game path; read-only revalidation, never automatic launch")
	dataDir := flag.String("data-dir", "", "optional absolute private launcher data directory (settings and accounts)")
	demo := flag.Bool("demo", false, "explicit memory-only game-state preview; normal entry does not simulate launch")
	loginUI := flag.String("login-ui", os.Getenv("ODDWARD_LOGIN_UI"), "optional absolute path to the authored login window helper; defaults to the embedded copy")
	flag.Parse()
	settingsPath, cachePath, err := nativeDataPaths(*dataDir)
	if err != nil {
		log.Fatal(err)
	}
	store, err := launchsettings.NewStore(settingsPath)
	if err != nil {
		log.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	saved, err := store.Load(context.Background())
	if err != nil {
		log.Printf("settings unreadable, using defaults: %v", err)
		saved = launchsettings.Settings{}
	}
	// One transport carries every request, so the proxy and mirror chosen in
	// the network panel apply to updates and announcements alike.
	netSettings, netState := system.SavedNetwork(saved.Network)
	network := netcfg.New(netSettings, nil)
	accountStore, err := accounts.Open(filepath.Join(filepath.Dir(cachePath), "accounts"), accounts.DPAPI{})
	if err != nil {
		log.Fatal(err)
	}
	wishArchives, err := system.NewWishArchives(filepath.Dir(cachePath))
	if err != nil {
		log.Fatal(err)
	}
	checkInRecords, err := checkin.Open(filepath.Join(filepath.Dir(cachePath), "checkin"))
	if err != nil {
		log.Fatal(err)
	}
	tajiduoClient, err := tajiduo.NewClient(tajiduo.Config{HTTPClient: &http.Client{Transport: network}})
	if err != nil {
		log.Fatal("cannot initialize Tajiduo client")
	}
	mygo.App.WhenReady(func() {
		s := newApplicationShell(*demo)
		tajiduoBackend := system.NewTajiduoBackend(accountStore, tajiduoClient, wishArchives, checkInRecords)
		s.WishLinkBackend = tajiduoBackend
		s.CheckInBackend = tajiduoBackend
		s.WishBackend, s.WishLocation = wishArchives, time.Local
		s.ManagedBackend = system.NativeManagedBackend{
			LoginUI:         resolveLoginUI(executable, *loginUI),
			Accounts:        accountStore,
			HTTPClient:      &http.Client{Transport: network},
			LauncherVersion: buildinfo.Version,
		}
		s.ManagedStore = system.NativeSettingsStore{Store: store}
		s.InitialManagedRoot = *gameRoot
		s.Model.Network, s.NetworkBackend = netState, system.SystemNetwork{Network: network, Store: store}
		selfUpdate(s, executable, network, store, saved)
		if art, err := launcher.LoadPoster(*background, *poster); err == nil {
			s.Poster = art
			// The launch tone follows each version's key art.
			if tone, ok := launcher.PosterAccent(*background, *poster); ok {
				s.Accent = tone
			}
		} else {
			log.Printf("static background unavailable: %v", err)
		}
		if art, err := launcher.LoadArtwork(*logo); err == nil {
			s.Logo = art
		} else {
			log.Printf("logo unavailable: %v", err)
		}
		if art, err := launcher.LoadArtwork(*slogan); err == nil {
			s.Slogan = art
		} else {
			log.Printf("slogan unavailable: %v", err)
		}
		if art, err := launcher.LoadArtwork(*newsArt); err == nil {
			s.News = art
		} else {
			log.Printf("news artwork unavailable: %v", err)
		}
		if *background != "" {
			if info, err := os.Stat(*background); err == nil && !info.IsDir() {
				if p, err := video.Open(*background); err == nil {
					s.Player = p.SetLoop(true).SetMuted(true)
				} else {
					log.Printf("background unavailable: %v", err)
				}
			} else {
				log.Print("background unavailable: local file not found")
			}
		}
		if s.Player == nil {
			s.Dispatch(state.Event{Kind: state.MediaFailed})
		}
		options := launcherWindowOptions()
		options.Content = ui.View(s.View)
		w := mygo.NewWindow(options)
		if err := w.SetIcon(branding.IconPNG); err != nil {
			log.Printf("application icon unavailable: %v", err)
		}
		s.PickGameDirectory = func(ctx context.Context) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: w, Title: "定位已有的异环游戏", ButtonLabel: "选择游戏目录", Directory: true})
			if err != nil {
				return "", err
			}
			if err = ctx.Err(); err != nil {
				return "", err
			}
			if len(paths) == 0 {
				return "", nil
			}
			return paths[0], nil
		}
		s.PickWishImport = func(ctx context.Context) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: w, Title: "导入抽卡档案", Filters: []mygo.FileFilter{{Name: "JSON", Extensions: []string{"json"}}}})
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", ctxErr
			}
			if err != nil {
				return "", err
			}
			if len(paths) == 0 {
				return "", nil
			}
			return paths[0], nil
		}
		s.PickWishExport = func(ctx context.Context, suggested string, format state.WishFormat) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			title := "导出 Oddward 完整档案"
			if format == state.WishNTEUID {
				title = "导出 NTEUID 互通档案"
			}
			path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: w, Title: title, DefaultPath: suggested, Filters: []mygo.FileFilter{{Name: "JSON", Extensions: []string{"json"}}}})
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", ctxErr
			}
			if err != nil {
				return "", err
			}
			if path != "" && !strings.EqualFold(filepath.Ext(path), ".json") {
				path += ".json"
			}
			return path, nil
		}
		// Bind wake before either asynchronous reader can start, including a static
		// background with no animation frames to incidentally flush the result.
		s.RequestFrame = w.Invalidate
		feed := news.NewClient()
		feed.HTTP.Transport = network
		s.Source = feed
		images, err := system.NewWishImages(filepath.Join(filepath.Dir(cachePath), "cache", "gamemedia"), feed.HTTP)
		if err != nil {
			log.Printf("wish item images unavailable: %v", err)
		} else {
			s.WishImages = images
		}
		s.Minimize = w.Minimize
		// Intentionally no Maximize/SetFullScreen callback: direct native calls can
		// bypass the fixed window's style flags even when the caption is hidden.
		s.CloseWindow = w.Close
		w.Invalidate()
		w.OnClose(func(e *mygo.CloseEvent) {
			if s.Model.Closed {
				return
			}
			previous := s.CloseWindow
			s.CloseWindow = nil
			s.Dispatch(state.Event{Kind: state.RequestQuit})
			s.CloseWindow = previous
			if !s.Model.Closed {
				e.PreventDefault()
				w.Invalidate()
			}
		})
		w.OnClosed(s.Dispose)
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

func nativeDataPaths(override string) (settings, cache string, err error) {
	if override != "" {
		if !filepath.IsAbs(override) {
			return "", "", os.ErrInvalid
		}
		root := filepath.Clean(override)
		return filepath.Join(root, "launcher-v1.json"), filepath.Join(root, "components"), nil
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return "", "", err
	}
	local, err := os.UserCacheDir()
	if err != nil {
		return "", "", err
	}
	return filepath.Join(config, "Oddward", "launcher-v1.json"), filepath.Join(local, "Oddward", "components"), nil
}
