// SPDX-License-Identifier: GPL-3.0-or-later

package cnproto

import (
	"errors"
	"strings"
	"testing"
)

// Public profile hashes from spec §5.1 (hashes of vendor binaries, not secrets).
const (
	cn208Launcher = "3CF58ECBE4CD33A83E741C12437CA9BB67678D166548C5972579C1266F250D9F"
	cn208Bridge   = "4101B035406BECDA3EACBF0B2DABD7F43D9DF3F9294906706FC504623E7CD13B"
	cn209Launcher = "AF1C6D0DFF6B466E9ED0E1E1565DC26F72C4A1149151A53E749832226AB4FB1F"
	cn209Bridge   = "AC9D9551810264594082BBD1704BCA588220DA087F4F8FB8600C41F0135E38C4"
)

func TestResolveProfile(t *testing.T) {
	cases := []struct{ launcher, bridge, name, version string }{
		{cn208Launcher, cn208Bridge, "Cn208", "2.0.8.0"},
		{cn209Launcher, cn209Bridge, "Cn209", "2.0.9.0"},
		{strings.ToLower(cn209Launcher), strings.ToLower(cn209Bridge), "Cn209", "2.0.9.0"},
	}
	for _, c := range cases {
		p, err := ResolveProfile(c.launcher, c.bridge)
		if err != nil || p.Name != c.name || p.SDKVersion != c.version {
			t.Errorf("ResolveProfile(%s…) = %+v, %v", c.launcher[:8], p, err)
		}
	}
}

func TestResolveProfileFailsClosed(t *testing.T) {
	cases := map[string][2]string{
		"mixed-pair": {cn208Launcher, cn209Bridge},
		"swapped":    {cn208Bridge, cn208Launcher},
		"unknown":    {strings.Repeat("0", 64), strings.Repeat("0", 64)},
		"empty":      {"", ""},
		"prefix":     {cn208Launcher[:63], cn208Bridge},
	}
	for name, c := range cases {
		if _, err := ResolveProfile(c[0], c[1]); !errors.Is(err, ErrUnsupportedSDK) {
			t.Errorf("%s: err = %v, want ErrUnsupportedSDK", name, err)
		}
	}
}

func TestKnownProfilesIsACopy(t *testing.T) {
	ps := KnownProfiles()
	if len(ps) != 2 {
		t.Fatalf("profiles = %d", len(ps))
	}
	ps[0].SDKVersion = "tampered"
	if KnownProfiles()[0].SDKVersion == "tampered" {
		t.Fatal("KnownProfiles exposes the internal table")
	}
}

func TestPublicConstants(t *testing.T) {
	if AppID != 1289 || AppIDString != "1289" || Platform != 4 || ChannelID != 9 ||
		ServiceSDKVersion != "5.159.0" || ConfigName != "oneSdkConfig.config" || CopyDataTag != 0x70676D77 ||
		NotifyLogin != 1 || NotifyLogout != 4 || NotifyGeometry != 14 {
		t.Fatal("protocol constants drifted from spec")
	}
}
