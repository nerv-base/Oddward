// SPDX-License-Identifier: GPL-3.0-or-later

// Package overlaywin is the Win32 window-lifecycle adapter for Oddward's MyGo
// Native UI login overlay and welcome banner.
//
// Target processes are verified by PID and creation FILETIME using query and
// synchronize rights only. Every window mutation targets the helper's own HWND
// (or a test fixture owned by the test process); no DLL or code is injected
// into any external process.
package overlaywin
