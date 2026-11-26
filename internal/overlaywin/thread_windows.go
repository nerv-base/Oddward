// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package overlaywin

import "runtime"

// Win32 window-procedure and window-long calls are thread-affine. The adapter
// pins the calling goroutine for the duration of those calls so Go's scheduler
// cannot migrate it to another OS thread.
func runtimeLock()   { runtime.LockOSThread() }
func runtimeUnlock() { runtime.UnlockOSThread() }
