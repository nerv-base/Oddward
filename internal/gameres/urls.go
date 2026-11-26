// SPDX-License-Identifier: GPL-3.0-or-later

package gameres

import (
	"net/url"
	"strings"
)

// ConfigURL builds the remote Windows configuration URL for branch.
func ConfigURL(base, branch string) (string, error) {
	return resourceURL(base, branch, "Version", "Windows", "config.xml")
}

// VersionConfigURL builds the versioned remote Windows configuration URL for branch.
func VersionConfigURL(base, branch, version string) (string, error) {
	return resourceURL(base, branch, "Version", "Windows", "version", version, "config.xml")
}

// ManifestURL builds the versioned archive URL for the given branch.
func ManifestURL(base, branch, version string) (string, error) {
	return resourceURL(base, branch, "Version", "Windows", "version", version, "ResList.bin.zip")
}

// DiffURL builds the versioned lastdiff.bin URL for the given branch.
func DiffURL(base, branch, version string) (string, error) {
	return resourceURL(base, branch, "Version", "Windows", "version", version, "lastdiff.bin")
}

// CASURL builds the content-addressed URL shared by files, paks and patch blobs.
func CASURL(base, branch string, object Object) (string, error) {
	if !validObject(object, maxPakBytes) {
		return "", ErrMalformedManifest
	}
	return resourceURL(base, branch, "Res", object.MD5[:1], object.String())
}

func resourceURL(base string, components ...string) (string, error) {
	for _, component := range components {
		if !validURLComponent(component) {
			return "", ErrPathUnsafe
		}
	}

	// This validates URL syntax and path boundaries. Origin selection and trust
	// remain the caller's responsibility.
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.Hostname() == "" || u.User != nil ||
		strings.ContainsAny(base, "?#") || u.EscapedPath() != u.Path {
		return "", ErrPathUnsafe
	}
	prefix := strings.TrimSuffix(u.Path, "/")
	if prefix != "" {
		for _, component := range strings.Split(strings.TrimPrefix(prefix, "/"), "/") {
			if !validURLComponent(component) {
				return "", ErrPathUnsafe
			}
		}
	}
	u.Path = prefix + "/" + strings.Join(components, "/")
	return u.String(), nil
}

func validURLComponent(s string) bool {
	if len(s) == 0 || len(s) > 64 || strings.Contains(s, "..") {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
			i > 0 && (c == '-' || c == '_' || c == '.')) {
			return false
		}
	}
	return true
}
