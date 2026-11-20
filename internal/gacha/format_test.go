// SPDX-License-Identifier: GPL-3.0-or-later

package gacha

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFormatRoundTrips(t *testing.T) {
	a, _, err := Merge(Archive{}, fixtureSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	b, err := ExportOddward(a, fixtureTime())
	if err != nil {
		t.Fatal(err)
	}
	imp, err := DecodeImport(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, imp.Archive) {
		t.Fatalf("lossy oddward: %+v", imp)
	}
	b2, err := ExportOddward(imp.Archive, fixtureTime())
	if err != nil || !bytes.Equal(b, b2) {
		t.Fatal("unstable full export")
	}
	b, err = ExportNTEUID(a, fixtureTime())
	if err != nil {
		t.Fatal(err)
	}
	imp, err = DecodeImport(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(imp.Archive.Snapshots) != 0 {
		t.Fatal("nteuid snapshots")
	}
	for _, r := range imp.Archive.Records {
		if r.Luck != LuckUnknown {
			t.Fatal("nteuid luck")
		}
	}
	merged, report, err := MergeImported(a, imp)
	if err != nil || report.Added != 0 || report.Conflicts != 0 || !reflect.DeepEqual(merged, a) {
		t.Fatalf("lossy merge: %+v %v", report, err)
	}
	empty, report, err := MergeImported(Archive{}, imp)
	if err != nil || report.Added != 2 || empty.RoleID != a.RoleID {
		t.Fatal("import new archive")
	}
	_, report, err = MergeImported(empty, imp)
	if err != nil || report.Added != 0 {
		t.Fatal("import idempotence")
	}
	_, _, err = MergeImported(Archive{RoleID: "9"}, imp)
	if !errors.Is(err, ErrRoleMismatch) {
		t.Fatal("import role mismatch")
	}
}

func TestLuckTierFourRoundTrips(t *testing.T) {
	a := fixtureArchive()
	a.Records[0].Luck = 4
	b, err := ExportOddward(a, fixtureTime())
	if err != nil {
		t.Fatal(err)
	}
	imp, err := DecodeImport(b)
	if err != nil {
		t.Fatal(err)
	}
	if imp.Archive.Records[0].Luck != 4 {
		t.Fatalf("luck=%d", imp.Archive.Records[0].Luck)
	}
}

func TestUTCPrecisionAndValidation(t *testing.T) {
	a := fixtureArchive()
	a.Records[0].At = time.Date(2026, 11, 14, 20, 34, 56, 789999999, time.FixedZone("plus8", 8*3600))
	b, err := ExportOddward(a, fixtureTime())
	if err != nil {
		t.Fatal(err)
	}
	imp, err := DecodeImport(b)
	if err != nil {
		t.Fatal(err)
	}
	if imp.Archive.Records[0].At != fixtureTime() || imp.Archive.Records[0].At.Location() != time.UTC {
		t.Fatalf("not UTC milliseconds: %v", imp.Archive.Records[0].At)
	}
	for _, bad := range []string{"", strings.Repeat("a", 65), "a\n", "中文", "\x7f"} {
		a := fixtureArchive()
		a.Records[0].ItemID = bad
		if _, err := ExportOddward(a, fixtureTime()); err == nil {
			t.Errorf("item accepted: %q", bad)
		}
	}
	for _, bad := range []string{"", "１２", "abc", strings.Repeat("1", 21)} {
		a := fixtureArchive()
		a.RoleID = bad
		if _, err := ExportOddward(a, fixtureTime()); err == nil {
			t.Errorf("role accepted %q", bad)
		}
	}
	for _, luck := range []int{-2, 5} {
		a := fixtureArchive()
		a.Records[0].Luck = luck
		if _, err := ExportOddward(a, fixtureTime()); err == nil {
			t.Errorf("luck %d", luck)
		}
	}
}

// The literal is the bytes the file-backed build exported for the same fixture,
// kept so a format change cannot slip in with the storage move.
func TestExportOddwardGoldenBytes(t *testing.T) {
	const golden = `{"format":"oddward-gacha","version":1,"role_id":"123456","exported_at":1794659696789,"records":[{"pool":"limited","item_id":"1001","time_ms":1794659696789,"pity":71,"luck":2},{"pool":"weapon","item_id":"fork_test","time_ms":1794656096789,"pity":60,"luck":-1}],"snapshots":[]}`
	got, err := ExportOddward(fixtureArchive(), fixtureTime())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != golden {
		t.Fatalf("export bytes changed:\n got %s\nwant %s", got, golden)
	}
}

func TestStrictDecode(t *testing.T) {
	base := `{"format":"nteuid-gacha","version":1,"uid":"123456","source":"tajiduo","updated_at":0,"pools":{"limited":{"records":[{"item_id":"1001","time_ms":1,"pity":1}]}}}`
	cases := map[string][]byte{
		"duplicate":        []byte(strings.Replace(base, `"version":1`, `"version":1,"version":1`, 1)),
		"nested duplicate": []byte(strings.Replace(base, `"pity":1`, `"pity":1,"pity":2`, 1)),
		"unknown":          []byte(strings.Replace(base, `"version":1`, `"version":1,"extra":0`, 1)),
		"nested unknown":   []byte(strings.Replace(base, `"pity":1`, `"pity":1,"luck":0`, 1)),
		"trailing":         []byte(base + ` {}`), "utf8": append([]byte(base), 0xff),
		"version":  []byte(strings.Replace(base, `"version":1`, `"version":2`, 1)),
		"format":   []byte(strings.Replace(base, "nteuid-gacha", "other", 1)),
		"large":    bytes.Repeat([]byte(" "), MaxDocumentBytes+1),
		"uid":      []byte(strings.Replace(base, "123456", "12a", 1)),
		"source":   []byte(strings.Replace(base, "tajiduo", "other", 1)),
		"pity":     []byte(strings.Replace(base, `"pity":1`, `"pity":0`, 1)),
		"time":     []byte(strings.Replace(base, `"time_ms":1`, `"time_ms":0`, 1)),
		"updated":  []byte(strings.Replace(base, `"updated_at":0`, `"updated_at":-1`, 1)),
		"fraction": []byte(strings.Replace(base, `"updated_at":0`, `"updated_at":0.5`, 1)),
		"array":    []byte(`[]`), "null": []byte(`null`),
		"pool":        []byte(strings.Replace(base, "limited", "other", 1)),
		"missing":     []byte(strings.Replace(base, `"updated_at":0,`, "", 1)),
		"null object": []byte(strings.Replace(base, `"pools":{"limited":{"records":[{"item_id":"1001","time_ms":1,"pity":1}]}}`, `"pools":null`, 1)),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeImport(data); err == nil {
				t.Fatal("accepted invalid document")
			}
		})
	}
	if _, err := DecodeImport([]byte(base)); err != nil {
		t.Fatal(err)
	}
	odd, err := ExportOddward(fixtureArchive(), fixtureTime())
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{bytes.Replace(odd, []byte(`"luck":2`), []byte(`"luck":5`), 1), bytes.Replace(odd, []byte(`"pool":"limited"`), []byte(`"pool":"other"`), 1), bytes.Replace(odd, []byte(`"luck":2`), []byte(`"luck":2,"extra":0`), 1)} {
		if _, err := DecodeImport(data); err == nil {
			t.Fatal("invalid full document")
		}
	}
}
