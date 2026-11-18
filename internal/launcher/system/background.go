// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"net/http"

	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launchmedia"
)

func NewLauncherBackground(dir string, transport http.RoundTripper) launcher.BackgroundSource {
	return launchmedia.New(dir, transport)
}
