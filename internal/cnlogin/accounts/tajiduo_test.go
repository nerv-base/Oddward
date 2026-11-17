// SPDX-License-Identifier: GPL-3.0-or-later

package accounts

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tajiduoPath(dir, id string) string { return filepath.Join(dir, id+".tjd") }

// sampleGrant returns a grant whose LaohuUserID matches sampleSecret(n)'s
// ChannelUID, so it can be linked to that account.
func sampleGrant(n int) TajiduoGrant {
	return TajiduoGrant{
		DeviceID:     strings.ToUpper(fmt.Sprintf("%032x", 0xdef000+n)),
		CenterUID:    fmt.Sprintf("center-%d", n),
		RefreshToken: fmt.Sprintf("refresh-%d-SECRETVALUE", n),
		LaohuUserID:  fmt.Sprintf("uid-%d-7788", n),
		RoleID:       fmt.Sprintf("role-%d", n),
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
	if a.TajiduoLinked {
		t.Fatal("fresh account reports Tajiduo linked")
	}
	g := sampleGrant(1)
	mustLink(t, s, a.ID, g)

	got, err := s.UnlockTajiduo(a.ID)
	if err != nil {
		t.Fatalf("UnlockTajiduo: %v", err)
	}
	if got != g {
		t.Fatalf("UnlockTajiduo = %+v, want %+v", got, g)
	}
	raw, err := os.ReadFile(tajiduoPath(dir, a.ID))
	if err != nil {
		t.Fatalf("attachment file: %v", err)
	}
	if bytes.Contains(raw, []byte(g.RefreshToken)) {
		t.Fatalf("attachment stores refresh token in clear: %s", raw)
	}
	if _, err := s.UnlockTajiduo(strings.Repeat("0", 32)); !errors.Is(err, ErrNoTajiduo) {
		t.Fatalf("Unlock unknown attachment = %v, want ErrNoTajiduo", err)
	}
}

func TestTajiduoLinkRejectsMismatchedLaohuUserID(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	g := sampleGrant(1)
	g.LaohuUserID = "some-other-tiger-account"
	if err := s.LinkTajiduo(a.ID, g); !errors.Is(err, ErrTajiduoMismatch) {
		t.Fatalf("LinkTajiduo mismatched = %v, want ErrTajiduoMismatch", err)
	}
	if _, err := os.Stat(tajiduoPath(dir, a.ID)); !os.IsNotExist(err) {
		t.Fatalf("mismatched link wrote an attachment: %v", err)
	}
	if _, err := s.UnlockTajiduo(a.ID); !errors.Is(err, ErrNoTajiduo) {
		t.Fatalf("Unlock after rejected link = %v, want ErrNoTajiduo", err)
	}
}

func TestTajiduoLinkNeedsExistingAccount(t *testing.T) {
	s, dir, _ := newTestStore(t)
	if err := s.LinkTajiduo(strings.Repeat("0", 32), sampleGrant(1)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("LinkTajiduo missing account = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(tajiduoPath(dir, strings.Repeat("0", 32))); !os.IsNotExist(err) {
		t.Fatal("link to a missing account wrote a file")
	}
}

func TestTajiduoAADBindsAccountID(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	b := mustSave(t, s, "B", sampleSecret(2))
	mustLink(t, s, a.ID, sampleGrant(1))

	// Copy A's sealed attachment to B and rewrite only the public id so the file
	// name, the header id and the record id all agree. The AAD still carries A's
	// id, so decryption under B must fail.
	raw, err := os.ReadFile(tajiduoPath(dir, a.ID))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["id"] = b.ID
	moved, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tajiduoPath(dir, b.ID), moved, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UnlockTajiduo(b.ID); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Unlock of transplanted attachment = %v, want ErrCorrupt", err)
	}
	// A's own attachment is untouched.
	if got, err := s.UnlockTajiduo(a.ID); err != nil || got != sampleGrant(1) {
		t.Fatalf("A attachment broken by transplant: %v", err)
	}
}

func TestTajiduoAADBindsCenterUID(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	mustLink(t, s, a.ID, sampleGrant(1))
	edit := func(m map[string]any) {
		m["centerUid"] = "center-hijacked"
	}
	path := tajiduoPath(dir, a.ID)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	edit(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UnlockTajiduo(a.ID); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Unlock after centerUid tamper = %v, want ErrCorrupt", err)
	}
}

func TestTajiduoUpdateRotatesToken(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	g := sampleGrant(1)
	mustLink(t, s, a.ID, g)
	before, err := os.ReadFile(recordPath(dir, a.ID))
	if err != nil {
		t.Fatal(err)
	}

	rotated := g
	rotated.RefreshToken = "refresh-rotated-2-SECRETVALUE"
	if err := s.UpdateTajiduo(a.ID, rotated); err != nil {
		t.Fatalf("UpdateTajiduo: %v", err)
	}
	got, err := s.UnlockTajiduo(a.ID)
	if err != nil || got != rotated {
		t.Fatalf("Unlock after update = %+v %v, want %+v", got, err, rotated)
	}
	after, err := os.ReadFile(recordPath(dir, a.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("UpdateTajiduo changed the .acct bytes")
	}
}

func TestTajiduoUpdateRejectsImmutableFields(t *testing.T) {
	s, _, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	g := sampleGrant(1)
	mustLink(t, s, a.ID, g)

	changedCenter := g
	changedCenter.CenterUID = "center-moved"
	if err := s.UpdateTajiduo(a.ID, changedCenter); !errors.Is(err, ErrTajiduoImmutable) {
		t.Fatalf("Update centerUid = %v, want ErrTajiduoImmutable", err)
	}
	changedLaohu := g
	changedLaohu.LaohuUserID = "uid-moved"
	if err := s.UpdateTajiduo(a.ID, changedLaohu); !errors.Is(err, ErrTajiduoImmutable) {
		t.Fatalf("Update laohuUserID = %v, want ErrTajiduoImmutable", err)
	}
	changedDevice := g
	changedDevice.DeviceID = "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF"
	if err := s.UpdateTajiduo(a.ID, changedDevice); !errors.Is(err, ErrTajiduoImmutable) {
		t.Fatalf("Update deviceID = %v, want ErrTajiduoImmutable", err)
	}
	if got, err := s.UnlockTajiduo(a.ID); err != nil || got != g {
		t.Fatalf("rejected updates changed the grant: %+v %v", got, err)
	}

	// A grant with no attachment cannot be updated.
	b := mustSave(t, s, "B", sampleSecret(2))
	if err := s.UpdateTajiduo(b.ID, sampleGrant(2)); !errors.Is(err, ErrNoTajiduo) {
		t.Fatalf("Update without attachment = %v, want ErrNoTajiduo", err)
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
		t.Fatalf("attachment survived unlink: %v", err)
	}
	if err := s.UnlinkTajiduo(a.ID); err != nil {
		t.Fatalf("second UnlinkTajiduo = %v, want nil", err)
	}
	if _, err := s.UnlockTajiduo(a.ID); !errors.Is(err, ErrNoTajiduo) {
		t.Fatalf("Unlock after unlink = %v, want ErrNoTajiduo", err)
	}
	// Unlinking an account that never had an attachment is a no-op.
	b := mustSave(t, s, "B", sampleSecret(2))
	if err := s.UnlinkTajiduo(b.ID); err != nil {
		t.Fatalf("Unlink never-linked = %v, want nil", err)
	}
}

func TestTajiduoRemoveDeletesAttachment(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	mustLink(t, s, a.ID, sampleGrant(1))
	if err := s.Remove(a.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(tajiduoPath(dir, a.ID)); !os.IsNotExist(err) {
		t.Fatalf("Remove left the attachment behind: %v", err)
	}
	if _, err := os.Stat(recordPath(dir, a.ID)); !os.IsNotExist(err) {
		t.Fatalf("Remove left the record behind: %v", err)
	}
}

func TestTajiduoOrphanAttachmentHandledLikeExistingCleanup(t *testing.T) {
	s, dir, _ := newTestStore(t)
	orphan := strings.Repeat("ab", 16)
	if err := os.WriteFile(tajiduoPath(dir, orphan), []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	list, corrupt := mustList(t, s)
	if len(list) != 0 {
		t.Fatalf("orphan attachment appeared as an account: %+v", list)
	}
	// Existing record cleanup only counts unusable files; it never deletes them,
	// so the orphan is counted here too and left on disk.
	if corrupt != 1 {
		t.Fatalf("orphan attachment corrupt count = %d, want 1", corrupt)
	}
	if _, err := os.Stat(tajiduoPath(dir, orphan)); err != nil {
		t.Fatalf("orphan attachment was deleted; existing cleanup only counts: %v", err)
	}
	// It is still cleanable through the idempotent unlink.
	if err := s.UnlinkTajiduo(orphan); err != nil {
		t.Fatalf("Unlink orphan = %v", err)
	}
	if _, err := os.Stat(tajiduoPath(dir, orphan)); !os.IsNotExist(err) {
		t.Fatal("Unlink did not remove the orphan")
	}
}

func TestTajiduoListReflectsLinked(t *testing.T) {
	s, _, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	_ = a
	list, _ := mustList(t, s)
	if len(list) != 1 || list[0].TajiduoLinked || list[0].TajiduoState != "" {
		t.Fatalf("before link List = %+v, want no Tajiduo state", list)
	}
	mustLink(t, s, list[0].ID, sampleGrant(1))
	list, _ = mustList(t, s)
	if len(list) != 1 || !list[0].TajiduoLinked || list[0].TajiduoState != "linked" {
		t.Fatalf("after link List = %+v, want a linked Tajiduo state", list)
	}
	if err := s.UnlinkTajiduo(list[0].ID); err != nil {
		t.Fatal(err)
	}
	list, _ = mustList(t, s)
	if len(list) != 1 || list[0].TajiduoLinked || list[0].TajiduoState != "" {
		t.Fatalf("after unlink List = %+v, want no Tajiduo state", list)
	}
}

func TestTajiduoListReportsNeedsRelinkForCorruptAttachment(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	if err := os.WriteFile(tajiduoPath(dir, a.ID), []byte("{not a sealed grant"), 0o600); err != nil {
		t.Fatal(err)
	}
	list, _ := mustList(t, s)
	if len(list) != 1 || list[0].TajiduoLinked || list[0].TajiduoState != "needs-relink" {
		t.Fatalf("List = %+v, want needs-relink", list)
	}
}

func TestTajiduoAttachmentOpsLeaveAccountBytesUnchanged(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	original, err := os.ReadFile(recordPath(dir, a.ID))
	if err != nil {
		t.Fatal(err)
	}
	compare := func(stage string) {
		t.Helper()
		now, err := os.ReadFile(recordPath(dir, a.ID))
		if err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		if !bytes.Equal(original, now) {
			t.Fatalf("%s changed the .acct bytes", stage)
		}
	}
	g := sampleGrant(1)
	mustLink(t, s, a.ID, g)
	compare("Link")
	g.RefreshToken = "refresh-after-link"
	if err := s.UpdateTajiduo(a.ID, g); err != nil {
		t.Fatal(err)
	}
	compare("Update")
	if err := s.UnlinkTajiduo(a.ID); err != nil {
		t.Fatal(err)
	}
	compare("Unlink")
}

func TestTajiduoCorruptAttachment(t *testing.T) {
	cases := map[string]func(t *testing.T, path string){
		"garbage": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("not json at all"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"truncated secret": func(t *testing.T, path string) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			m["secret"] = "AAAA"
			out, _ := json.Marshal(m)
			if err := os.WriteFile(path, out, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"tampered ciphertext": func(t *testing.T, path string) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			// Flip a bit of the decoded ciphertext. Flipping a base64 character
			// can leave the decoded bytes unchanged when it only touches
			// padding bits, which would test nothing.
			b, err := base64.StdEncoding.DecodeString(m["secret"].(string))
			if err != nil || len(b) < 8 {
				t.Fatalf("secret is not base64 ciphertext: %v", err)
			}
			b[len(b)/2] ^= 0x01
			m["secret"] = base64.StdEncoding.EncodeToString(b)
			out, _ := json.Marshal(m)
			if err := os.WriteFile(path, out, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"wrong version": func(t *testing.T, path string) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			m["version"] = 99
			out, _ := json.Marshal(m)
			if err := os.WriteFile(path, out, 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			s, dir, _ := newTestStore(t)
			a := mustSave(t, s, "A", sampleSecret(1))
			mustLink(t, s, a.ID, sampleGrant(1))
			corrupt(t, tajiduoPath(dir, a.ID))
			if _, err := s.UnlockTajiduo(a.ID); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Unlock corrupt attachment = %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestTajiduoGrantFormattingRedacts(t *testing.T) {
	g := sampleGrant(1)
	var logged strings.Builder
	slog.New(slog.NewTextHandler(&logged, nil)).Info("link", "grant", g)
	outputs := []string{
		g.String(),
		fmt.Sprint(g),
		fmt.Sprintf("%v %+v %#v %s", g, g, g, g),
		fmt.Sprintf("%v", &g),
		fmt.Sprintf("%x %X %q", g, g, g),
		fmt.Sprintf("%+v", struct{ G TajiduoGrant }{g}),
		logged.String(),
	}
	for _, out := range outputs {
		for _, plain := range []string{g.RefreshToken, g.DeviceID, g.CenterUID, g.LaohuUserID, "SECRETVALUE"} {
			if strings.Contains(out, plain) {
				t.Fatalf("formatted grant leaks %q: %s", plain, out)
			}
		}
		if !strings.Contains(out, "redacted") {
			t.Fatalf("formatted grant does not say it is redacted: %s", out)
		}
	}
}

func TestTajiduoGrantWipe(t *testing.T) {
	g := sampleGrant(1)
	g.Wipe()
	if g != (TajiduoGrant{}) {
		t.Fatalf("Wipe left a non-empty grant: %+v", g)
	}
}

func TestNewTajiduoDeviceID(t *testing.T) {
	a, err := NewTajiduoDeviceID()
	if err != nil {
		t.Fatalf("NewTajiduoDeviceID: %v", err)
	}
	if !isUpperHex(a, 32) {
		t.Fatalf("device id = %q, want 32 uppercase hex chars", a)
	}
	b, err := NewTajiduoDeviceID()
	if err != nil {
		t.Fatalf("NewTajiduoDeviceID: %v", err)
	}
	if a == b {
		t.Fatal("two device ids are identical")
	}
}

func TestTajiduoGrantValidation(t *testing.T) {
	base := sampleGrant(1)
	bad := map[string]func(*TajiduoGrant){
		"short device id":    func(g *TajiduoGrant) { g.DeviceID = "abc" },
		"lowercase device":   func(g *TajiduoGrant) { g.DeviceID = strings.ToLower(g.DeviceID) },
		"empty center uid":   func(g *TajiduoGrant) { g.CenterUID = "" },
		"empty refresh":      func(g *TajiduoGrant) { g.RefreshToken = "" },
		"control in refresh": func(g *TajiduoGrant) { g.RefreshToken = "a\nb" },
		"blank laohu":        func(g *TajiduoGrant) { g.LaohuUserID = "   " },
		"control in role":    func(g *TajiduoGrant) { g.RoleID = "role\tx" },
	}
	for name, mutate := range bad {
		g := base
		mutate(&g)
		if err := g.validate(); !errors.Is(err, ErrInvalidSecret) {
			t.Fatalf("%s: validate = %v, want ErrInvalidSecret", name, err)
		}
	}
	ok := base
	ok.RoleID = ""
	if err := ok.validate(); err != nil {
		t.Fatalf("empty roleId must be valid: %v", err)
	}
}

func TestTajiduoLinkRejectsInvalidGrant(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	g := sampleGrant(1)
	g.DeviceID = strings.ToLower(g.DeviceID)
	if err := s.LinkTajiduo(a.ID, g); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("Link invalid grant = %v, want ErrInvalidSecret", err)
	}
	if _, err := os.Stat(tajiduoPath(dir, a.ID)); !os.IsNotExist(err) {
		t.Fatal("invalid grant wrote an attachment")
	}
}
