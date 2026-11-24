// Package overlaywin is the Win32 adapter for the MyGo overlay PoC.
//
// It deliberately mirrors the security semantics of the WPF GameOverlay:
// the target process is identified by PID and creation time, every write is
// limited to the overlay's own HWND, and no code is injected into the game.
// It is a platform adapter only: no UI and no login-domain logic lives here.
package overlaywin
