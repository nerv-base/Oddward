// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package news

import "os"

func replaceCachedNewsFile(stage, destination string) error { return os.Rename(stage, destination) }
