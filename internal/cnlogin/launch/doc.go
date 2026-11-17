// SPDX-License-Identifier: GPL-3.0-or-later

// Package launch prepares and starts the China-server (国服) game client for
// the native platform host: read-only install preflight (paths, bridge pointer,
// SHA-256 digests), the patcher environment, the exact child environment
// block and argument string, running-client refusal, the single-instance
// mutex, the private SDK log directory and its marker-only cleanup, and
// CreateProcess with a retained, creation-time-checked process identity.
//
// Behaviour follows the MIT-licensed Hi3Helper.Plugin.NTE reference (see docs/architecture.md).
// The package never modifies game files, never injects into, writes memory of,
// or terminates the game, and never performs network requests. Compatibility
// profile matching on the returned digests is done by the caller.
package launch
