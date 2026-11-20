// SPDX-License-Identifier: GPL-3.0-or-later

package accounts

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
)

func tajiduoPath(dir, id string) string { return filepath.Join(dir, id+".tjd") }

// sampleGrant matches sampleSecret(n): the grant belongs to that game account.
func sampleGrant(n int) TajiduoGrant {
	return TajiduoGrant{
		DeviceID:     fmt.Sprintf("%032X", 0xdef000+n),
		CenterUID:    fmt.Sprintf("center-%d", n),
		RefreshToken: fmt.Sprintf("refresh-%d-SECRETVALUE", n),
		LaohuUserID:  fmt.Sprintf("uid-%d-7788", n),
		RoleID:       fmt.Sprintf("role-%d", n),
	}
}

// tajiduoRowBytes reads the stored attachment of id.
func tajiduoRowBytes(t *testing.T, s *Store, id string) []byte {
	t.Helper()
	var raw []byte
	if err := s.db.QueryRow(t.Context(), `SELECT file FROM account_tajiduo WHERE account_id = ?`, id).Scan(&raw); err != nil {
		t.Fatalf("read Tajiduo row %s: %v", id, err)
	}
	return raw
}

// tamperTajiduo rewrites the stored attachment, which is what editing the file
// used to be: the ciphertext no longer matches its AAD.
func tamperTajiduo(t *testing.T, s *Store, id string, edit func(map[string]any)) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(tajiduoRowBytes(t, s, id), &m); err != nil {
		t.Fatal(err)
	}
	edit(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(t.Context(), `UPDATE account_tajiduo SET file = ? WHERE account_id = ?`, out, id); err != nil {
		t.Fatal(err)
	}
}

func mustLink(t *testing.T, s *Store, id string, g TajiduoGrant) {
	t.Helper()
	if err := s.LinkTajiduo(id, g); err != nil {
		t.Fatalf("LinkTajiduo: %v", err)
	}
}

func TestTajiduoLinkUnlockRoundTrip(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	g := sampleGrant(1)
	mustLink(t, s, a.ID, g)
	raw, err := os.ReadFile(tajiduoPath(dir, a.ID))
	if err != nil {
		t.Fatalf("mirror file: %v", err)
	}
	// The center uid is public metadata (it names the row and is bound into the
	// AAD in clear), but the refresh token never leaves the ciphertext.
	for _, plain := range []string{g.RefreshToken, "SECRETVALUE"} {
		if bytes.Contains(raw, []byte(plain)) {
			t.Fatalf("attachment mirror leaks %q", plain)
		}
	}
	if !bytes.Equal(raw, tajiduoRowBytes(t, s, a.ID)) {
		t.Fatal("attachment mirror differs from the stored bytes")
	}
	got, err := s.UnlockTajiduo(a.ID)
	if err != nil {
		t.Fatalf("UnlockTajiduo: %v", err)
	}
	if got != g {
		t.Fatalf("UnlockTajiduo = %+v, want the linked grant", got)
	}
	if _, err := s.UnlockTajiduo(strings.Repeat("0", 32)); !errors.Is(err, ErrNoTajiduo) {
		t.Fatalf("UnlockTajiduo of an unknown account = %v, want ErrNoTajiduo", err)
	}
}

func TestTajiduoLinkRejectsMismatchedLaohuUserID(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	g := sampleGrant(2) // belongs to another game account
	if err := s.LinkTajiduo(a.ID, g); !errors.Is(err, ErrTajiduoMismatch) {
		t.Fatalf("LinkTajiduo with a foreign grant = %v, want ErrTajiduoMismatch", err)
	}
	if _, err := os.Stat(tajiduoPath(dir, a.ID)); !os.IsNotExist(err) {
		t.Fatalf("a rejected link wrote a mirror: %v", err)
	}
	if n := countRows(t, s, `SELECT count(*) FROM account_tajiduo`); n != 0 {
		t.Fatalf("a rejected link wrote %d rows", n)
	}
}

func TestTajiduoLinkNeedsExistingAccount(t *testing.T) {
	s, dir, _ := newTestStore(t)
	id := strings.Repeat("a", 32)
	if err := s.LinkTajiduo(id, sampleGrant(1)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("LinkTajiduo for a missing account = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(tajiduoPath(dir, id)); !os.IsNotExist(err) {
		t.Fatalf("a rejected link wrote a mirror: %v", err)
	}
}

func TestTajiduoAADBindsAccountID(t *testing.T) {
	s, _, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	b := mustSave(t, s, "B", sampleSecret(2))
	mustLink(t, s, a.ID, sampleGrant(1))
	// Move A's attachment onto B, rewriting only the id it claims to belong to.
	raw := tajiduoRowBytes(t, s, a.ID)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["id"] = b.ID
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(t.Context(), `INSERT INTO account_tajiduo (account_id, center_uid, file, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(account_id) DO UPDATE SET file = excluded.file`,
		b.ID, "center-1", out, database.Stamp(time.Unix(0, 0))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UnlockTajiduo(b.ID); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("UnlockTajiduo of a transplanted attachment = %v, want ErrCorrupt", err)
	}
	if _, err := s.UnlockTajiduo(a.ID); err != nil {
		t.Fatalf("the original attachment broke: %v", err)
	}
}

func TestTajiduoAADBindsCenterUID(t *testing.T) {
	s, _, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	mustLink(t, s, a.ID, sampleGrant(1))
	tamperTajiduo(t, s, a.ID, func(m map[string]any) { m["centerUid"] = "center-other" })
	if _, err := s.UnlockTajiduo(a.ID); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("UnlockTajiduo after a center uid change = %v, want ErrCorrupt", err)
	}
	list, _ := mustList(t, s)
	if len(list) != 1 || list[0].TajiduoLinked || list[0].TajiduoState != "needs-relink" {
		t.Fatalf("List = %+v, want needs-relink", list)
	}
}

func TestTajiduoUpdateRotatesToken(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	before := rowBytes(t, s, a.ID)
	g := sampleGrant(1)
	mustLink(t, s, a.ID, g)
	g.RefreshToken = "refresh-rotated-SECRETVALUE"
	if err := s.UpdateTajiduo(a.ID, g); err != nil {
		t.Fatalf("UpdateTajiduo: %v", err)
	}
	got, err := s.UnlockTajiduo(a.ID)
	if err != nil {
		t.Fatalf("UnlockTajiduo: %v", err)
	}
	if got != g {
		t.Fatalf("stored grant = %+v, want the rotated one", got)
	}
	if !bytes.Equal(before, rowBytes(t, s, a.ID)) {
		t.Fatal("updating the attachment changed the account record")
	}
	mirror, err := os.ReadFile(recordPath(dir, a.ID))
	if err != nil || !bytes.Equal(mirror, before) {
		t.Fatalf("updating the attachment changed the account mirror: %v", err)
	}
}

func TestTajiduoUpdateRejectsImmutableFields(t *testing.T) {
	s, _, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	g := sampleGrant(1)
	mustLink(t, s, a.ID, g)
	cases := map[string]func(*TajiduoGrant){
		"center uid": func(g *TajiduoGrant) { g.CenterUID = "center-other" },
		"laohu user": func(g *TajiduoGrant) { g.LaohuUserID = "uid-other" },
		"device id":  func(g *TajiduoGrant) { g.DeviceID = fmt.Sprintf("%032X", 0x123456) },
	}
	for name, edit := range cases {
		next := g
		edit(&next)
		if err := s.UpdateTajiduo(a.ID, next); !errors.Is(err, ErrTajiduoImmutable) {
			t.Fatalf("UpdateTajiduo with a new %s = %v, want ErrTajiduoImmutable", name, err)
		}
	}
	if got, err := s.UnlockTajiduo(a.ID); err != nil || got != g {
		t.Fatalf("a rejected update changed the grant: %+v, %v", got, err)
	}
	// An account without an attachment cannot be updated into one.
	b := mustSave(t, s, "B", sampleSecret(2))
	if err := s.UpdateTajiduo(b.ID, sampleGrant(2)); !errors.Is(err, ErrNoTajiduo) {
		t.Fatalf("UpdateTajiduo without an attachment = %v, want ErrNoTajiduo", err)
	}
}

func TestTajiduoUnlinkIdempotent(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	mustLink(t, s, a.ID, sampleGrant(1))
	if err := s.UnlinkTajiduo(a.ID); err != nil {
		t.Fatalf("UnlinkTajiduo: %v", err)
	}
	if _, err := os.Stat(tajiduoPath(dir, a.ID)); !os.IsNotExist(err) {
		t.Fatalf("UnlinkTajiduo left the mirror: %v", err)
	}
	if _, err := s.UnlockTajiduo(a.ID); !errors.Is(err, ErrNoTajiduo) {
		t.Fatalf("UnlockTajiduo after unlink = %v, want ErrNoTajiduo", err)
	}
	if err := s.UnlinkTajiduo(a.ID); err != nil {
		t.Fatalf("second UnlinkTajiduo = %v, want nil", err)
	}
	if err := s.UnlinkTajiduo("not an id"); err != nil {
		t.Fatalf("UnlinkTajiduo of an invalid id = %v, want nil", err)
	}
}

func TestTajiduoRemoveDeletesAttachment(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	mustLink(t, s, a.ID, sampleGrant(1))
	if err := s.Remove(a.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	for _, name := range []string{recordPath(dir, a.ID), tajiduoPath(dir, a.ID)} {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Fatalf("Remove left %s: %v", filepath.Base(name), err)
		}
	}
	if n := countRows(t, s, `SELECT count(*) FROM account_tajiduo`); n != 0 {
		t.Fatalf("Remove left %d attachments", n)
	}
}

func TestTajiduoListReflectsLinked(t *testing.T) {
	s, _, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	if list, _ := mustList(t, s); list[0].TajiduoLinked || list[0].TajiduoState != "" {
		t.Fatalf("unlinked account reported %+v", list[0])
	}
	mustLink(t, s, a.ID, sampleGrant(1))
	if list, _ := mustList(t, s); !list[0].TajiduoLinked || list[0].TajiduoState != "linked" {
		t.Fatalf("linked account reported %+v", list[0])
	}
	if err := s.UnlinkTajiduo(a.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := mustList(t, s); list[0].TajiduoLinked || list[0].TajiduoState != "" {
		t.Fatalf("unlinked account reported %+v", list[0])
	}
}

func TestTajiduoListReportsNeedsRelinkForCorruptAttachment(t *testing.T) {
	s, _, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	mustLink(t, s, a.ID, sampleGrant(1))
	if _, err := s.db.Exec(t.Context(), `UPDATE account_tajiduo SET file = ? WHERE account_id = ?`, []byte("garbage"), a.ID); err != nil {
		t.Fatal(err)
	}
	list, _ := mustList(t, s)
	if len(list) != 1 || list[0].TajiduoLinked || list[0].TajiduoState != "needs-relink" {
		t.Fatalf("List = %+v, want a damaged attachment to need a relink", list)
	}
	if _, err := s.UnlockTajiduo(a.ID); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("UnlockTajiduo of a damaged attachment = %v, want ErrCorrupt", err)
	}
}

func TestTajiduoAttachmentOpsLeaveAccountBytesUnchanged(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	stored := rowBytes(t, s, a.ID)
	mirrorBefore, err := os.ReadFile(recordPath(dir, a.ID))
	if err != nil {
		t.Fatal(err)
	}
	g := sampleGrant(1)
	mustLink(t, s, a.ID, g)
	if err := s.UpdateTajiduo(a.ID, g); err != nil {
		t.Fatal(err)
	}
	if err := s.UnlinkTajiduo(a.ID); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, rowBytes(t, s, a.ID)) {
		t.Fatal("an attachment operation rewrote the account record")
	}
	if mirror, err := os.ReadFile(recordPath(dir, a.ID)); err != nil || !bytes.Equal(mirror, mirrorBefore) {
		t.Fatalf("an attachment operation rewrote the account mirror: %v", err)
	}
}

func TestTajiduoCorruptAttachment(t *testing.T) {
	cases := map[string]func(map[string]any){
		"garbage": func(m map[string]any) { m["version"] = "nope" },
		"truncated secret": func(m map[string]any) {
			m["secret"] = base64.StdEncoding.EncodeToString([]byte("AAAA"))
		},
		"flipped ciphertext": func(m map[string]any) {
			raw, err := base64.StdEncoding.DecodeString(m["secret"].(string))
			if err != nil || len(raw) < 8 {
				t.Fatalf("secret is not base64 ciphertext: %v", err)
			}
			raw[len(raw)/2] ^= 0x01
			m["secret"] = base64.StdEncoding.EncodeToString(raw)
		},
		"wrong version": func(m map[string]any) { m["version"] = 99 },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			s, _, _ := newTestStore(t)
			a := mustSave(t, s, "A", sampleSecret(1))
			mustLink(t, s, a.ID, sampleGrant(1))
			tamperTajiduo(t, s, a.ID, edit)
			if _, err := s.UnlockTajiduo(a.ID); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("UnlockTajiduo after %s = %v, want ErrCorrupt", name, err)
			}
		})
	}
}

func TestTajiduoGrantFormattingRedacts(t *testing.T) {
	g := sampleGrant(1)
	for _, format := range []func() string{
		func() string { return g.String() },
		func() string { return fmt.Sprintf("%v", g) },
		func() string { return fmt.Sprintf("%+v", g) },
		func() string { return fmt.Sprintf("%#v", g) },
		func() string { return fmt.Sprintf("%s", g) },
		func() string { return g.GoString() },
	} {
		out := format()
		if strings.Contains(out, g.RefreshToken) || strings.Contains(out, g.CenterUID) || strings.Contains(out, "refresh-1") {
			t.Fatalf("grant formatting leaks the credential: %s", out)
		}
	}
	if out, ok := g.LogValue().Any().(string); !ok || strings.Contains(out, g.RefreshToken) {
		t.Fatalf("LogValue leaks the credential: %v", out)
	}
}

func TestTajiduoGrantWipe(t *testing.T) {
	g := sampleGrant(1)
	g.Wipe()
	if g.RefreshToken != "" || g.CenterUID != "" || g.LaohuUserID != "" || g.DeviceID != "" || g.RoleID != "" {
		t.Fatalf("Wipe left %+v", g)
	}
}

func TestNewTajiduoDeviceID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		id, err := NewTajiduoDeviceID()
		if err != nil {
			t.Fatalf("NewTajiduoDeviceID: %v", err)
		}
		if !isUpperHex(id, 32) {
			t.Fatalf("device id = %q, want 32 uppercase hex chars", id)
		}
		if seen[id] {
			t.Fatalf("device id repeated: %s", id)
		}
		seen[id] = true
	}
}

func TestTajiduoGrantValidation(t *testing.T) {
	cases := map[string]func(*TajiduoGrant){
		"short device id":    func(g *TajiduoGrant) { g.DeviceID = "abc" },
		"lowercase device":   func(g *TajiduoGrant) { g.DeviceID = strings.ToLower(g.DeviceID) },
		"blank center uid":   func(g *TajiduoGrant) { g.CenterUID = "" },
		"blank refresh":      func(g *TajiduoGrant) { g.RefreshToken = "" },
		"control in refresh": func(g *TajiduoGrant) { g.RefreshToken = "a\nb" },
		"blank laohu":        func(g *TajiduoGrant) { g.LaohuUserID = "" },
		"control in role":    func(g *TajiduoGrant) { g.RoleID = "a\x00b" },
	}
	for name, edit := range cases {
		g := sampleGrant(1)
		edit(&g)
		if err := g.validate(); !errors.Is(err, ErrInvalidSecret) {
			t.Fatalf("validate with a %s = %v, want ErrInvalidSecret", name, err)
		}
	}
	ok := sampleGrant(1)
	ok.RoleID = ""
	if err := ok.validate(); err != nil {
		t.Fatalf("an empty role id must be valid: %v", err)
	}
}

func TestTajiduoLinkRejectsInvalidGrant(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	g := sampleGrant(1)
	g.RefreshToken = ""
	if err := s.LinkTajiduo(a.ID, g); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("LinkTajiduo with an invalid grant = %v, want ErrInvalidSecret", err)
	}
	if _, err := os.Stat(tajiduoPath(dir, a.ID)); !os.IsNotExist(err) {
		t.Fatalf("a rejected grant wrote a mirror: %v", err)
	}
}
