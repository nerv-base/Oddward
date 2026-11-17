// SPDX-License-Identifier: GPL-3.0-or-later

// Package laohu implements the China-server account protocol spoken with the
// laohu.com services: channel initialisation, SMS sign-in, saved channel
// validation, the OneLogin game identity exchange and the antifatigue
// (real-name) pre-login admission check.
//
// Behaviour follows the MIT-licensed Hi3Helper.Plugin.NTE reference (see docs/architecture.md).
//
// The package is split into pure parts (parameter builders, MD5/SHA-256
// signing, AES-128-ECB field and envelope encryption, strict response
// parsers, the admission decision and the phase catalogue) and a thin I/O
// layer: Client wraps an injected *http.Client and Admission serialises the
// pre-login checks that gate every identity delivery to the game. SignIn runs
// the whole sign-in sequence and returns only after admission allowed it.
//
// Secrets (phone numbers, captchas, tokens, keys, response bodies) never
// appear in errors or in String/GoString output. Error.Message returns a fixed
// Chinese explanation; the only server text it can show is the sanitised
// explanation of an HTTP failure, which is meant for local display only.
// Go strings cannot be wiped, so buffer zeroing here is best effort.
package laohu
