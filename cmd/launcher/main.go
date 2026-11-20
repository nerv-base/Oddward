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
	"github.com/nerv-base/Oddward/internal/brandingcache"
	"github.com/nerv-base/Oddward/internal/buildinfo"
	"github.com/nerv-base/Oddward/internal/bwiki"
	"github.com/nerv-base/Oddward/internal/checkin"
	"github.com/nerv-base/Oddward/internal/cnlogin/accounts"
	"github.com/nerv-base/Oddward/internal/database"
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
	// Every structured record lives in one database beside the settings file, so
	// the launcher cannot end up with half of its state in another directory. A
	// failure here is fatal: the update stub only rolls back a version that exits
	// non-zero before it reports itself healthy.
	dataRoot := filepath.Dir(cachePath)
	db, err := database.Open(context.Background(), database.Options{Path: filepath.Join(dataRoot, database.FileName)})
	if err != nil {
		log.Fatalf("launcher database unavailable: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("launcher database close: %v", err)
		}
	}()
	store, err := launchsettings.NewStore(db, settingsPath)
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
	accountStore, err := accounts.Open(db, filepath.Join(dataRoot, "accounts"), accounts.DPAPI{})
	if err != nil {
		log.Fatal(err)
	}
	wishArchives, err := system.NewWishArchives(db, dataRoot)
	if err != nil {
		log.Fatal(err)
	}
	bwikiStore, err := bwiki.Open(db, filepath.Join(dataRoot, "bwiki"), &http.Client{Transport: network})
	if err != nil {
		log.Printf("community wiki cache unavailable: %v", err)
	} else {
		wishArchives.SetBWiki(bwikiStore)
	}
	checkInRecords, err := checkin.Open(db, filepath.Join(dataRoot, "checkin"))
	if err != nil {
		log.Fatal(err)
	}
	// The production backends are built before the window exists: adopting the
	// records of an older build has to finish before the interface can read or
	// write any of them.
	downloads := system.NewGameDownloadBackend(db, dataRoot, store, &http.Client{Transport: network}, buildinfo.Version)
	brandingStore, err := brandingcache.Open(db, filepath.Join(dataRoot, "branding"))
	if err != nil {
		log.Printf("branding cache unavailable: %v", err)
	}
	feed := news.NewClient()
	feed.HTTP.Transport = network
	announcements := system.NewLauncherNews(db, feed, filepath.Join(dataRoot, "cache", "news"))
	adoptLegacyRecords(db, store, wishArchives, accountStore, checkInRecords, downloads, announcements, brandingStore)
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
		s.GameDownloads = downloads
		prefs := saved.Downloads
		if prefs == (launchsettings.Downloads{}) {
			prefs = launchsettings.DefaultDownloads()
		}
		s.Model, _ = state.Reduce(s.Model, state.Event{Kind: state.DownloadSettingsLoaded, DownloadSettings: state.DownloadPreferences{
			RateLimitKiB: prefs.RateLimitKiB,
		}})
		s.Model, _ = state.Reduce(s.Model, state.Event{Kind: state.UIPreferencesLoaded, CloseToTray: saved.UI.CloseToTray, AccentFollow: saved.UI.AccentFollow, CheckUpdates: saved.UI.CheckUpdates})
		s.DataDir = filepath.Dir(settingsPath)
		s.SaveUIChoices = func(closeToTray, accentFollow, checkUpdates bool) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := store.Update(ctx, func(st *launchsettings.Settings) {
				st.UI = launchsettings.UI{CloseToTray: closeToTray, AccentFollow: accentFollow, CheckUpdates: checkUpdates}
			}); err != nil {
				log.Printf("save launcher window choices: %v", err)
			}
		}
		// The home account row lists the same saved accounts the login window
		// offers; choosing one only changes which account the next login
		// proposes.
		s.HomeAccounts = func(ctx context.Context, _ uint64) ([]state.SavedAccount, string, error) {
			list, _, err := accountStore.List()
			if err != nil {
				return nil, "", err
			}
			out := make([]state.SavedAccount, 0, len(list))
			for _, account := range list {
				out = append(out, state.SavedAccount{ID: account.ID, Label: account.Label})
			}
			last, err := accountStore.Last()
			if err != nil {
				return out, "", err
			}
			return out, last, nil
		}
		s.HomeAccountUse = func(_ context.Context, id string) error {
			return accountStore.SetLast(id)
		}
		s.ManagedBackend = system.NativeManagedBackend{
			Downloads:       downloads,
			LoginUI:         resolveLoginUI(executable, *loginUI),
			Accounts:        accountStore,
			HTTPClient:      &http.Client{Transport: network},
			LauncherVersion: buildinfo.Version,
		}
		s.ManagedStore = system.NativeSettingsStore{Store: store}
		s.InitialManagedRoot = *gameRoot
		s.Model.Network, s.NetworkBackend = netState, system.SystemNetwork{Network: network, Store: store}
		selfUpdate(s, executable, network, store, saved)
		if brandingStore != nil {
			adapter := brandingCacheAdapter{cache: brandingStore}
			s.BrandingCache = adapter
			if record, err := adapter.Load(context.Background()); err == nil {
				if art, decodeErr := ui.DecodeBitmap(record.Image); decodeErr == nil {
					s.Slogan = art
					s.BrandingSlogan = record.Slogan
					s.BrandingVersion = record.VersionName
				}
			}
		}
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
			if art != nil {
				s.Slogan = art
				s.SloganLocal = true
			}
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
		if !*demo && *background == "" && *poster == "" {
			s.BackgroundSource = system.NewLauncherBackground(filepath.Join(filepath.Dir(cachePath), "cache", "launcher-media"), network)
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
		s.Source = announcements
		images, err := system.NewWishImages(db, filepath.Join(dataRoot, "cache", "gamemedia"), feed.HTTP)
		if err != nil {
			log.Printf("wish item images unavailable: %v", err)
		} else {
			if bwikiStore != nil {
				images.SetBWiki(bwikiStore)
			}
			s.WishImages = images
		}
		s.Minimize = w.Minimize
		// Intentionally no Maximize/SetFullScreen callback: direct native calls can
		// bypass the fixed window's style flags even when the caption is hidden.
		s.CloseWindow = w.Close
		s.HideWindow = w.Hide
		s.OpenFolder = func(path string) {
			if path == "" {
				return
			}
			go func() {
				if err := mygo.Shell.OpenPath(path); err != nil {
					log.Printf("open folder %q: %v", path, err)
				}
			}()
		}
		installTray(w, s)
		w.Invalidate()
		w.OnClose(func(e *mygo.CloseEvent) {
			if s.Model.Closed {
				return
			}
			if s.Model.CloseToTray {
				e.PreventDefault()
				w.Hide()
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
