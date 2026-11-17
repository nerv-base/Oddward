// SPDX-License-Identifier: GPL-3.0-or-later

package cnproto

import (
	"errors"
	"strings"
)

// ErrUnsupportedSDK reports a launcher/bridge pair that matches no known
// compatibility profile. The IPC protocol is reverse-engineered per bridge
// build, so an unknown or mismatched pair fails closed rather than guess a
// layout (spec §5.1).
var ErrUnsupportedSDK = errors.New("cnproto: unsupported SDK build")

// Profile is one verified CN launcher/bridge build. Its only protocol effect is
// selecting the registration version string the game must send (§2.9); the
// hashes are public SHA-256 of vendor binaries, not secrets.
type Profile struct {
	// Name is the human-facing profile id (e.g. "Cn208").
	Name string
	// SDKVersion is the op 0 registration version string for this build.
	SDKVersion string
	// LauncherSHA256 is the hex SHA-256 of <root>\NTELauncher\NTEGame.exe.
	LauncherSHA256 string
	// BridgeSHA256 is the hex SHA-256 of the bridge DLL.
	BridgeSHA256 string
}

// knownProfiles is the fixed compatibility table from spec §5.1. Adding a
// profile is a data change, not a code change.
var knownProfiles = []Profile{
	{
		Name:           "Cn208",
		SDKVersion:     "2.0.8.0",
		LauncherSHA256: "3CF58ECBE4CD33A83E741C12437CA9BB67678D166548C5972579C1266F250D9F",
		BridgeSHA256:   "4101B035406BECDA3EACBF0B2DABD7F43D9DF3F9294906706FC504623E7CD13B",
	},
	{
		Name:           "Cn209",
		SDKVersion:     "2.0.9.0",
		LauncherSHA256: "AF1C6D0DFF6B466E9ED0E1E1565DC26F72C4A1149151A53E749832226AB4FB1F",
		BridgeSHA256:   "AC9D9551810264594082BBD1704BCA588220DA087F4F8FB8600C41F0135E38C4",
	},
}

// KnownProfiles returns a copy of the compatibility table so a caller cannot
// mutate the package-level data.
func KnownProfiles() []Profile {
	return append([]Profile(nil), knownProfiles...)
}

// ResolveProfile returns the profile whose launcher and bridge hashes both
// match (case-insensitively). A mismatched, partial, swapped, or unknown pair
// fails closed with ErrUnsupportedSDK.
func ResolveProfile(launcherSHA256, bridgeSHA256 string) (Profile, error) {
	for _, p := range knownProfiles {
		if strings.EqualFold(launcherSHA256, p.LauncherSHA256) && strings.EqualFold(bridgeSHA256, p.BridgeSHA256) {
			return p, nil
		}
	}
	return Profile{}, ErrUnsupportedSDK
}
