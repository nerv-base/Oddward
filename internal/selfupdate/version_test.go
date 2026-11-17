// SPDX-License-Identifier: GPL-3.0-or-later

package selfupdate

import "testing"

func TestParseVersionAcceptsOnlyPlainSemVer(t *testing.T) {
	for _, ok := range []string{"0.1.0", "1.2.3", "10.20.30", "1.0.0-rc.1", "1.0.0-alpha", "1.0.0-alpha.beta.2"} {
		if _, err := ParseVersion(ok); err != nil {
			t.Errorf("ParseVersion(%q) = %v, want ok", ok, err)
		}
	}
	for _, bad := range []string{"", "v1.2.3", "1.2", "1.2.3.4", "01.2.3", "1.02.3", "1.2.3-", "1.2.3+build", "1.2.3-01", "1.2.3-a..b", "1.2.3 ", "-1.2.3", "1.2.x", "99999999999999999999.0.0"} {
		if _, err := ParseVersion(bad); err == nil {
			t.Errorf("ParseVersion(%q) accepted", bad)
		}
	}
}

func TestVersionsOrderBySemVerPrecedence(t *testing.T) {
	ordered := []string{"0.9.9", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.10.0", "2.0.0"}
	for i := 0; i+1 < len(ordered); i++ {
		a, _ := ParseVersion(ordered[i])
		b, _ := ParseVersion(ordered[i+1])
		if a.Compare(b) >= 0 || b.Compare(a) <= 0 {
			t.Errorf("%s should sort before %s", ordered[i], ordered[i+1])
		}
	}
	a, _ := ParseVersion("1.2.3")
	if a.Compare(a) != 0 || a.String() != "1.2.3" {
		t.Fatalf("identity: %v %q", a.Compare(a), a.String())
	}
}
