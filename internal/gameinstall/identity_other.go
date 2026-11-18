//go:build !windows

// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import "os"

func verifyFileID(f *os.File) (string, error) { return "unknown", nil }
