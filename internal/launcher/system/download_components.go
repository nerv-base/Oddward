package system

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nerv-base/Oddward/internal/launcher/state"
	"github.com/nerv-base/Oddward/internal/launcherfiles"
)

// componentVersionURL is the vendor manifest that lists the official launcher
// pieces. Tests replace it with a local server.
var componentVersionURL = launcherfiles.VersionURL

// componentsStageDir keeps downloaded launcher components outside the game
// root until the game transaction has been published.
func componentsStageDir(root string) string {
	return filepath.Join(downloadWork(root), "components")
}

// stageRuntimeComponents downloads the official launcher pieces Oddward needs
// to sign in and start the game into the download work area. A fresh install of
// the game resources alone does not include them, so the first install is only
// usable once they are published.
func (b *GameDownloadBackend) stageRuntimeComponents(ctx context.Context, root string, report func(state.Event)) error {
	manifest, err := launcherfiles.Fetch(ctx, b.Client, componentVersionURL)
	if err != nil {
		return fmt.Errorf("获取运行组件清单失败：%w", err)
	}
	files, err := manifest.Select()
	if err != nil {
		return fmt.Errorf("官方组件清单缺少必要文件：%w", err)
	}
	if launcherfiles.Present(root, files) {
		return nil
	}
	stage := componentsStageDir(root)
	if launcherfiles.Present(stage, files) {
		return nil
	}
	total := launcherfiles.TotalSize(files)
	meter := newThroughputMeter()
	var lastDone int64
	progress := func(done, total int64) {
		if delta := done - lastDone; delta > 0 {
			meter.Record(delta)
			lastDone = done
		}
		if report == nil {
			return
		}
		rate, eta := meter.Snapshot(done, total)
		report(state.Event{
			Kind:       state.DownloadProgress,
			Stage:      state.StageComponents,
			Downloaded: done,
			Total:      total,
			Rate:       rate,
			ETASecs:    eta,
		})
	}
	progress(0, total)
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return fmt.Errorf("创建运行组件暂存目录失败：%w", err)
	}
	if err := launcherfiles.Install(ctx, b.Client, stage, manifest, files, progress); err != nil {
		return fmt.Errorf("下载运行组件失败：%w", err)
	}
	return nil
}

// publishRuntimeComponents moves the staged components under the game root and
// points the game at the SDK bridge. It runs only after the installation
// transaction has been published.
func publishRuntimeComponents(root string) error {
	stage := componentsStageDir(root)
	staged := filepath.Join(stage, "NTELauncher")
	if _, err := os.Stat(staged); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	target := launcherfiles.LauncherDir(root)
	if _, err := os.Stat(target); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.Rename(staged, target); err != nil {
			return err
		}
	}
	pointer := filepath.Join(root, "Client", "WindowsNoEditor", "HT", "Binaries", "Win64", "GameLauncher.config")
	if err := os.MkdirAll(filepath.Dir(pointer), 0o700); err != nil {
		return err
	}
	bridge := filepath.Join(target, "WPGameClientSDK_64.dll")
	return os.WriteFile(pointer, []byte(filepath.ToSlash(bridge)), 0o600)
}
