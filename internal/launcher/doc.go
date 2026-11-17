// SPDX-License-Identifier: GPL-3.0-or-later

// Package launcher is Oddward's native MyGo launcher UI.
//
// The package is the presentation and effect layer of a three-layer design:
//
//   - github.com/nerv-base/Oddward/internal/launcher/state owns every user-visible decision as a
//     pure reducer (state.Model, state.Event, state.Present). It imports no
//     UI framework and is the place for new behaviour rules.
//   - this package (Shell) renders a state.Model, turns input into
//     state.Events, and runs effects (news fetches, managed backend calls) on
//     background contexts that report back as events on the UI thread.
//     Backends are reached only through the ManagedBackend and ManagedStore
//     interfaces.
//   - github.com/nerv-base/Oddward/internal/launcher/system implements those interfaces with the
//     real game, settings and native CN login packages. cmd/launcher wires it
//     in; tests use fakes instead.
//
// File map:
//
//   - shell.go: Shell, its frame loop and top-level composition.
//   - view.go, palette.go: the "Frame" home screen (canvas, hero, news card,
//     launch pill) and the art-derived launch tone.
//   - navigation.go, media_bar.go, task_deck.go, banner_media.go: rail,
//     background-media console, transfer deck and news cover carousel.
//   - panels.go, controls.go, frost.go, icons.go: overlay sheets and shared
//     controls/materials.
//   - mock_features.go: the in-memory check-in demo page.
//   - wish_*.go: the local wish archive page, import/export panels, media
//     cache and the optional Tajiduo link/fetch flow.
//   - managed_*.go: normal-mode game management and native login orchestration.
//   - artwork.go: decoding of the version poster, logo and slogan.
//
// Visual tests render through ui.NewTester and assert geometry, colour and
// accessibility; preview_*_test.go are opt-in renderers for design review.
package launcher
