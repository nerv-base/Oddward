// SPDX-License-Identifier: GPL-3.0-or-later

// Package buildinfo carries the identity a release build is stamped with.
// Development builds leave Version empty, which disables self-update.
package buildinfo

// Version is the SemVer of this build without a "v" prefix. Release builds set
// it with -ldflags "-X github.com/nerv-base/Oddward/internal/buildinfo.Version=X.Y.Z".
var Version = ""

// Repository is the GitHub repository whose releases carry updates.
const Repository = "nerv-base/Oddward"

// UpdatePublicKey is the base64 Ed25519 public key used to verify update manifests.
const UpdatePublicKey = "tjdSyezA+LYoG1N8xCngMi4AQw8ROLdWl585zpJfBM8="
