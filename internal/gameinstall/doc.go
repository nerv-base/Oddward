// SPDX-License-Identifier: GPL-3.0-or-later

// Package gameinstall manages launcher-owned installation records and explicit
// adoption and verification. Adoption reads official state without changing it;
// a structural detection or metadata adoption never proves file integrity or
// that a game can launch. Full verification uses file-granular durable MD5
// records and current Windows volume/file identities. Callers serialize changes
// to a single installation, and destructive operations require fresh evidence.
package gameinstall
