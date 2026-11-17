// SPDX-License-Identifier: GPL-3.0-or-later

package selfupdate

import (
	"errors"
	"path/filepath"
	"time"
)

// ErrNoApp means no app folder can be started; the installation is broken.
var ErrNoApp = errors.New("没有可以启动的版本，请重新运行安装程序")

// Starter starts exe with args and returns a channel that receives the exit
// result once the process ends.
type Starter func(exe string, args []string) (<-chan error, error)

// healthPoll is how often Launch looks for the healthy marker.
const healthPoll = 100 * time.Millisecond

// Launch is the stub's job: start the newest ready version that has not
// failed. A version that has never opened its window gets wait to do so; if
// it exits with an error first it is marked failed and the next one is tried.
// A version that was healthy before is trusted and left alone.
func Launch(root string, args []string, start Starter, wait time.Duration) (App, error) {
	for {
		apps, err := Apps(root)
		if err != nil {
			return App{}, err
		}
		app, ok := Choose(apps)
		if !ok {
			return App{}, ErrNoApp
		}
		exited, err := start(filepath.Join(app.Dir, AppExe), args)
		if err == nil && app.Healthy {
			return app, nil
		}
		if err == nil {
			if healthy, failed := watch(app.Dir, exited, wait); healthy || !failed {
				return app, nil
			}
		}
		if err := MarkFailed(app.Dir); err != nil {
			return App{}, err
		}
	}
}

// watch waits for the healthy marker, an exit or the timeout. failed is set
// only when the process ended with an error before becoming healthy.
func watch(dir string, exited <-chan error, wait time.Duration) (healthy, failed bool) {
	marker := filepath.Join(dir, healthyMarker)
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(healthPoll)
	defer tick.Stop()
	for {
		if regular(marker) {
			return true, false
		}
		select {
		case err := <-exited:
			if regular(marker) {
				return true, false
			}
			return false, err != nil
		case <-deadline.C:
			return false, false
		case <-tick.C:
		}
	}
}
