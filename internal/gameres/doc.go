// SPDX-License-Identifier: GPL-3.0-or-later

// Package gameres parses Windows CN game-resource metadata and plans how to
// materialize verified baseTag files. The caller supplies all bytes, local
// identities, object availability, and measured capacity. The package does
// not fetch resources, apply patches, or write an installation.
package gameres
