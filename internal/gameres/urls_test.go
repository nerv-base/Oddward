// SPDX-License-Identifier: GPL-3.0-or-later

package gameres

import (
	"errors"
	"strings"
	"testing"
)

func TestResourceURLs(t *testing.T) {
	const base = "https://cdn.example.invalid/resource/prefix/"
	const branch = "PC_preview-1"
	const version = "2.4.1"
	object := Object{MD5: "f23456789abcdef0123456789abcdef0", Size: 1 << 36}
	for _, tc := range []struct {
		name string
		get  func() (string, error)
		want string
	}{
		{"config", func() (string, error) { return ConfigURL(base, branch) }, "resource/prefix/PC_preview-1/Version/Windows/config.xml"},
		{"version config", func() (string, error) { return VersionConfigURL(base, branch, version) }, "resource/prefix/PC_preview-1/Version/Windows/version/2.4.1/config.xml"},
		{"manifest", func() (string, error) { return ManifestURL(base, branch, version) }, "resource/prefix/PC_preview-1/Version/Windows/version/2.4.1/ResList.bin.zip"},
		{"diff", func() (string, error) { return DiffURL(base, branch, version) }, "resource/prefix/PC_preview-1/Version/Windows/version/2.4.1/lastdiff.bin"},
		{"CAS", func() (string, error) { return CASURL(base, branch, object) }, "resource/prefix/PC_preview-1/Res/f/f23456789abcdef0123456789abcdef0.68719476736"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.get()
			if want := "https://cdn.example.invalid/" + tc.want; err != nil || got != want {
				t.Fatalf("URL = %q, %v; want %q", got, err, want)
			}
		})
	}
}

func TestResourceURLsRejectUnsafeComponents(t *testing.T) {
	const base = "https://cdn.example.invalid"
	for _, component := range []string{"", ".", "..", "a..b", "../version", "a/b", `a\b`, "a:b", "a?b", "a#b", "a%2fb", "a%2eb", "a\x00b", " a", strings.Repeat("a", 65)} {
		t.Run(component, func(t *testing.T) {
			if got, err := ConfigURL(base, component); got != "" || !errors.Is(err, ErrPathUnsafe) {
				t.Fatalf("ConfigURL() = %q, %v; want %v", got, err, ErrPathUnsafe)
			}
			if got, err := VersionConfigURL(base, component, "2.4.1"); got != "" || !errors.Is(err, ErrPathUnsafe) {
				t.Fatalf("VersionConfigURL(branch) = %q, %v; want %v", got, err, ErrPathUnsafe)
			}
			if got, err := VersionConfigURL(base, "preview", component); got != "" || !errors.Is(err, ErrPathUnsafe) {
				t.Fatalf("VersionConfigURL(version) = %q, %v; want %v", got, err, ErrPathUnsafe)
			}
			if got, err := ManifestURL(base, "preview", component); got != "" || !errors.Is(err, ErrPathUnsafe) {
				t.Fatalf("ManifestURL() = %q, %v; want %v", got, err, ErrPathUnsafe)
			}
			if got, err := DiffURL(base, "preview", component); got != "" || !errors.Is(err, ErrPathUnsafe) {
				t.Fatalf("DiffURL() = %q, %v; want %v", got, err, ErrPathUnsafe)
			}
		})
	}
}

func TestResourceURLsRejectUnsupportedBase(t *testing.T) {
	for _, base := range []string{
		"", "//cdn.example.invalid", "http://cdn.example.invalid", "https:cdn.example.invalid", "https:///no-authority",
		"https://example.invalid:bad/resource", "https://user:synthetic-secret@cdn.example.invalid",
		"https://cdn.example.invalid/resource?token=synthetic-secret", "https://cdn.example.invalid/resource?",
		"https://cdn.example.invalid/resource#x", "https://cdn.example.invalid/resource#",
		"https://cdn.example.invalid/resource/..", "https://cdn.example.invalid/resource/%2e%2e",
		"https://cdn.example.invalid/resource%2fprefix", "https://cdn.example.invalid//resource",
		`https://cdn.example.invalid/resource\prefix`,
	} {
		got, err := ConfigURL(base, "release")
		if got != "" || !errors.Is(err, ErrPathUnsafe) {
			t.Fatalf("ConfigURL() = %q, %v; want %v", got, err, ErrPathUnsafe)
		}
		if strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatal("ConfigURL exposed its input in an error")
		}
	}
}

func TestCASURLRejectsInvalidObjects(t *testing.T) {
	const digest = "f23456789abcdef0123456789abcdef0"
	for _, object := range []Object{
		{MD5: digest, Size: 0}, {MD5: digest, Size: -1}, {MD5: digest, Size: 1<<36 + 1},
		{MD5: "", Size: 25}, {MD5: "../" + digest, Size: 25}, {MD5: strings.ToUpper(digest), Size: 25},
	} {
		if got, err := CASURL("https://cdn.example.invalid", "release", object); got != "" || !errors.Is(err, ErrMalformedManifest) {
			t.Fatalf("CASURL() = %q, %v; want %v", got, err, ErrMalformedManifest)
		}
	}
}
