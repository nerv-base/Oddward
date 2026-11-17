// SPDX-License-Identifier: GPL-3.0-or-later

package selfupdate

import (
	"errors"
	"strconv"
	"strings"
)

// Version is a SemVer 2.0.0 version without build metadata. Release tags are
// "v" + Version.String(); the "v" never appears inside manifests.
type Version struct {
	Major, Minor, Patch uint64
	Pre                 []string
}

var errVersion = errors.New("selfupdate: not a MAJOR.MINOR.PATCH[-PRERELEASE] version")

// ParseVersion accepts exactly MAJOR.MINOR.PATCH with an optional
// pre-release; leading zeros, build metadata and a "v" prefix are rejected so
// that a version has one spelling in tags, folders and manifests.
func ParseVersion(s string) (Version, error) {
	if len(s) > 64 {
		return Version{}, errVersion
	}
	core, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, errVersion
	}
	var nums [3]uint64
	for i, p := range parts {
		n, ok := numeric(p)
		if !ok {
			return Version{}, errVersion
		}
		nums[i] = n
	}
	v := Version{Major: nums[0], Minor: nums[1], Patch: nums[2]}
	if hasPre {
		for _, id := range strings.Split(pre, ".") {
			if id == "" || strings.Trim(id, "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ-") != "" {
				return Version{}, errVersion
			}
			if _, isNum := numeric(id); !isNum && strings.Trim(id, "0123456789") == "" {
				return Version{}, errVersion // numeric identifier with a leading zero
			}
			v.Pre = append(v.Pre, id)
		}
	}
	return v, nil
}

func numeric(s string) (uint64, bool) {
	if s == "" || strings.Trim(s, "0123456789") != "" || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

func (v Version) String() string {
	s := strconv.FormatUint(v.Major, 10) + "." + strconv.FormatUint(v.Minor, 10) + "." + strconv.FormatUint(v.Patch, 10)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	return s
}

// Compare returns -1, 0 or +1 following SemVer precedence.
func (v Version) Compare(o Version) int {
	for _, d := range [][2]uint64{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if d[0] != d[1] {
			return cmp(d[0] < d[1])
		}
	}
	switch {
	case len(v.Pre) == 0 && len(o.Pre) == 0:
		return 0
	case len(v.Pre) == 0:
		return 1
	case len(o.Pre) == 0:
		return -1
	}
	for i := 0; i < len(v.Pre) && i < len(o.Pre); i++ {
		a, b := v.Pre[i], o.Pre[i]
		if a == b {
			continue
		}
		an, aNum := numeric(a)
		bn, bNum := numeric(b)
		switch {
		case aNum && bNum:
			return cmp(an < bn)
		case aNum:
			return -1
		case bNum:
			return 1
		default:
			return cmp(a < b)
		}
	}
	switch {
	case len(v.Pre) < len(o.Pre):
		return -1
	case len(v.Pre) > len(o.Pre):
		return 1
	}
	return 0
}

func cmp(less bool) int {
	if less {
		return -1
	}
	return 1
}
