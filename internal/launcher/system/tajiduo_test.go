// SPDX-License-Identifier: GPL-3.0-or-later
package system

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nerv-base/Oddward/internal/checkin"
	"github.com/nerv-base/Oddward/internal/cnlogin/accounts"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
	"github.com/nerv-base/Oddward/internal/tajiduo"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type tajiduoTestProtector struct{}

func (tajiduoTestProtector) Protect(p []byte) ([]byte, error) { return append([]byte(nil), p...), nil }
func (tajiduoTestProtector) Unprotect(p []byte) ([]byte, error) {
	return append([]byte(nil), p...), nil
}
func testTajiduoBackend(t *testing.T, uid string, namesFail ...bool) (*TajiduoBackend, *accounts.Store, string, *int) {
	t.Helper()
	return testTajiduoBackendInDir(t, t.TempDir(), uid, namesFail...)
}

func testTajiduoBackendInDir(t *testing.T, dir, uid string, namesFail ...bool) (*TajiduoBackend, *accounts.Store, string, *int) {
	t.Helper()
	namesDown := len(namesFail) > 0 && namesFail[0]
	store, err := accounts.Open(filepath.Join(dir, "accounts"), tajiduoTestProtector{})
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.Save(accounts.Account{Label: "138****0000"}, accounts.Secret{DeviceID: strings.Repeat("a", 32), ChannelUID: "42", ChannelToken: "fake-game-token"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetLast(account.ID); err != nil {
		t.Fatal(err)
	}
	rotations := 0
	binds := new(int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := func(data any) { json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data}) }
		switch r.URL.Path {
		case "/openApi/sms/new/login":
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "result": map[string]any{"userId": uid, "token": "fake-laohu-token"}})
		case "/usercenter/api/login":
			reply(map[string]any{"uid": "51", "accessToken": "fake-access", "refreshToken": "fake-login-refresh"})
		case "/usercenter/api/refreshToken":
			rotations++
			reply(map[string]any{"accessToken": "fake-access", "refreshToken": fmt.Sprintf("fake-rotated-refresh-%d", rotations)})
		case "/apihub/api/getGameBindRole":
			reply(map[string]any{"roleId": 123})
		case "/usercenter/api/v2/getGameRoles":
			reply(map[string]any{"bindRole": 123, "roles": []any{map[string]any{"roleId": 123, "roleName": "合成角色"}}})
		case "/usercenter/api/bindGameRole":
			*binds++
			reply(true)
		case "/apihub/awapi/yh/gacha":
			reply(map[string]any{"userid": "9_42", "roleid": "123", "luckType": 0, "luckTitle": "合成", "gachaDetails": []any{}})
		case "/apihub/awapi/yh/characters":
			if namesDown {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			reply([]any{map[string]any{"id": "11001", "name": "官方合成角色"}})
		default:
			t.Errorf("unexpected synthetic endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	client, err := tajiduo.NewClient(tajiduo.Config{HTTPClient: server.Client(), BaseURL: server.URL, LaohuBaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	archives, err := NewWishArchives(dir)
	if err != nil {
		t.Fatal(err)
	}
	records, err := checkin.Open(filepath.Join(dir, "checkin"))
	if err != nil {
		t.Fatal(err)
	}
	return NewTajiduoBackend(store, client, archives, records), store, account.ID, binds
}
func TestTajiduoCancelDoesNotSaveAndRoleReadDoesNotBind(t *testing.T) {
	b, s, id, binds := testTajiduoBackend(t, "42")
	ctx := context.Background()
	if err := b.Login(ctx, id, "13800000000", "123456"); err != nil {
		t.Fatal(err)
	}
	roles, err := b.Roles(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 1 || !roles[0].Bound || *binds != 0 {
		t.Fatal("role reading changed binding")
	}
	if _, err = s.UnlockTajiduo(id); !errors.Is(err, accounts.ErrNoTajiduo) {
		t.Fatal("pending login saved")
	}
	if err = b.Cancel(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UnlockTajiduo(id); !errors.Is(err, accounts.ErrNoTajiduo) {
		t.Fatal("cancel saved")
	}
}
func TestTajiduoLoginSavedUsesStoredGameCredentialWithoutSaving(t *testing.T) {
	b, s, id, binds := testTajiduoBackend(t, "42")
	ctx := context.Background()
	if err := b.LoginSaved(ctx, id); err != nil {
		t.Fatal(err)
	}
	roles, err := b.Roles(ctx, id)
	if err != nil || len(roles) != 1 || !roles[0].Bound {
		t.Fatalf("roles after saved login: %+v err=%v", roles, err)
	}
	if *binds != 0 {
		t.Fatal("role reading changed binding")
	}
	if _, err = s.UnlockTajiduo(id); !errors.Is(err, accounts.ErrNoTajiduo) {
		t.Fatal("saved login persisted before completion")
	}
}

func TestTajiduoCompleteThenFetchPreservesArchiveAfterUnlink(t *testing.T) {
	b, s, id, _ := testTajiduoBackend(t, "42")
	ctx := context.Background()
	if err := b.Login(ctx, id, "13800000000", "123456"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Roles(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := b.Complete(ctx, id, "123"); err != nil {
		t.Fatal(err)
	}
	grant, err := s.UnlockTajiduo(id)
	if err != nil || grant.RoleID != "123" {
		t.Fatal("completion not persisted")
	}
	device := grant.DeviceID
	grant.Wipe()
	report, err := b.Fetch(ctx, id, "123")
	if err != nil || report.Archive.RoleID != "123" {
		t.Fatalf("fetch merge failed: %v", err)
	}
	catalog, err := b.archives.Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if item, ok := catalog.Items["11001"]; !ok || item.Name != "官方合成角色" {
		t.Fatalf("official names were not saved after a fetch: %+v", catalog)
	}
	if err = b.Login(ctx, id, "13800000000", "123456"); err != nil {
		t.Fatal(err)
	}
	if b.sessions[id].session.Grant().DeviceID != device {
		t.Fatal("relink changed device")
	}
	if err = b.Cancel(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err = b.Unlink(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err = b.archives.Load(ctx, "123"); err != nil {
		t.Fatal("unlink removed archive")
	}
}

func TestTajiduoFetchKeepsMergedRecordsWhenNamesFail(t *testing.T) {
	b, _, id, _ := testTajiduoBackend(t, "42", true)
	ctx := context.Background()
	if err := b.Login(ctx, id, "13800000000", "123456"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Roles(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := b.Complete(ctx, id, "123"); err != nil {
		t.Fatal(err)
	}
	report, err := b.Fetch(ctx, id, "123")
	if err != nil || report.Archive.RoleID != "123" {
		t.Fatalf("a name-only failure must not fail the fetch: %+v %v", report, err)
	}
	if catalog, err := b.archives.Catalog(ctx); err != nil || len(catalog.Items) != 0 {
		t.Fatalf("failed names must leave no catalog: %+v %v", catalog, err)
	}
}

func TestTajiduoFetchRejectsAnotherRole(t *testing.T) {
	b, _, id, _ := testTajiduoBackend(t, "42")
	ctx := context.Background()
	if err := b.Login(ctx, id, "13800000000", "123456"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Roles(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := b.Complete(ctx, id, "123"); err != nil {
		t.Fatal(err)
	}
	_, err := b.Fetch(ctx, id, "999")
	var failure launcher.WishLinkError
	if !errors.As(err, &failure) || failure.Kind != state.WishLinkRoleMismatch {
		t.Fatalf("fetch must reject another role: %v", err)
	}
	if _, err := b.archives.Load(ctx, "999"); err == nil {
		t.Fatal("a rejected fetch must not write another archive")
	}
}

func TestTajiduoIdentityMismatchNeverSaves(t *testing.T) {
	b, s, id, _ := testTajiduoBackend(t, "99")
	err := b.Login(context.Background(), id, "13800000000", "123456")
	var failure launcher.WishLinkError
	if !errors.As(err, &failure) || failure.Kind != state.WishLinkMismatch {
		t.Fatal("wrong account not rejected")
	}
	if _, err = s.UnlockTajiduo(id); !errors.Is(err, accounts.ErrNoTajiduo) {
		t.Fatal("mismatch persisted")
	}
}

func TestTajiduoStatusOffersRelinkForCorruptAttachment(t *testing.T) {
	dir := t.TempDir()
	b, _, id, _ := testTajiduoBackendInDir(t, dir, "42")
	ctx := context.Background()
	if err := b.Login(ctx, id, "13800000000", "123456"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Roles(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := b.Complete(ctx, id, "123"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "accounts", id+".tjd")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := b.Status(ctx)
	if err != nil {
		t.Fatalf("a damaged attachment must not fail status: %v", err)
	}
	if status.AccountID != id || status.Linked || !status.NeedsRelink {
		t.Fatalf("damaged attachment must offer a relink: %+v", status)
	}
	if err := b.Login(ctx, id, "13800000000", "123456"); err != nil {
		t.Fatalf("a damaged attachment must not block relinking: %v", err)
	}
	if _, err := b.Roles(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := b.Complete(ctx, id, "123"); err != nil {
		t.Fatalf("relink did not replace the damaged attachment: %v", err)
	}
	if status, err = b.Status(ctx); err != nil || !status.Linked {
		t.Fatalf("relinked attachment not readable: %+v %v", status, err)
	}
}
