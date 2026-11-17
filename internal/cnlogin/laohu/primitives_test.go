// SPDX-License-Identifier: GPL-3.0-or-later
package laohu

import "testing"

func TestExportedPrimitivesMatchExistingImplementation(t *testing.T) {
	const fakeKey = "0123456789abcdef0123456789abcdef"
	fields := parameters{"z": "last+value", "a": "v alue", "empty": "", "sign": "ignored-existing-sign"}
	// Fixed synthetic digest computed independently using .NET's MD5.
	if got := SignParameters(fields, fakeKey); got != "76f3dfc1e6d31c216dc4c2387e1b18a0" {
		t.Fatal("signature vector differs")
	}
	signed := parameters{}
	for k, v := range fields {
		signed[k] = v
	}
	signed.sign(fakeKey)
	if got := SignParameters(fields, fakeKey); got != signed["sign"] {
		t.Fatal("exported signature differs")
	}
	if fields["sign"] != "ignored-existing-sign" {
		t.Fatal("signature mutated input")
	}
	if got := CanonicalParameters(fields); got != fields.canonical() {
		t.Fatal("canonical differs")
	}
	if got := FormParameters(fields); got != fields.form() {
		t.Fatal("form differs")
	}
	for _, plain := range []string{"13800000000", "123456", "", "中文"} {
		want, e := encryptField(plain, fakeKey)
		got, err := EncryptField(plain, fakeKey)
		if e != err || got != want {
			t.Fatal("field encryption differs")
		}
	}
}
