// SPDX-License-Identifier: GPL-3.0-or-later

// Package gamedl downloads and verifies immutable game resource and patch blobs.
// It never writes an installation tree. Callers supply trusted URL resolution
// and their configured HTTP client; persisted tasks contain no URLs or roots.
package gamedl
