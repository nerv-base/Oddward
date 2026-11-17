// SPDX-License-Identifier: GPL-3.0-or-later

// Package cnproto encodes and decodes the local IPC frames exchanged with the
// China-server game SDK bridge (WPGameClientSDK_64.dll).
//
// Frames received over the named pipe (op 1000 environment init, op 0 client
// registration, op 40 queries) are parsed here; frames sent back (op 1001 on
// the pipe, op 0x40D / op 2 / op 49 as WM_COPYDATA payloads) are built here.
// The package also holds the CN protocol constants, the window anchor
// computations and the table of verified launcher/bridge SDK profiles.
//
// It is pure: no Windows calls, no file system, no clock. Secrets (the
// application key, tokens, response bodies) never appear in errors or in
// formatted output, and buffers this package allocates for them are zeroed.
//
// Behaviour follows the MIT-licensed Hi3Helper.Plugin.NTE reference (see docs/architecture.md).
package cnproto
