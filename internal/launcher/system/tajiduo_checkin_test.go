// SPDX-License-Identifier: GPL-3.0-or-later
package system

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/checkin"
	"github.com/nerv-base/Oddward/internal/cnlogin/accounts"
	"github.com/nerv-base/Oddward/internal/launcher/state"
	"github.com/nerv-base/Oddward/internal/tajiduo"
)

// checkInWorld is a synthetic Tajiduo server and ledger that a test can steer:
// it answers the sign-state reads, the two writes and the role home exactly as
// the recorded protocol describes, and it counts what it was asked to do.
type checkInWorld struct {
	mu sync.Mutex

	todaySign bool
	days      int
	gamePosts int
	community int
	// alreadyGame and alreadyCommunity make a write answer "already signed"
	// while the state read is left as the test sets it.
	alreadyGame      bool
	alreadyCommunity bool
	// dropCommunity closes the connection mid-write, so the answer is lost
	// after the request reached the server.
	dropCommunity bool
	// refreshStatus, when set, replaces the token rotation answer.
	refreshStatus int
	communityDone bool

	backend   *TajiduoBackend
	store     *accounts.Store
	accountID string
	dir       string
}

func writeCode(w http.ResponseWriter, code int, msg string, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": msg, "data": data})
}

func replyCode(w http.ResponseWriter, code int, msg string) {
	writeCode(w, code, msg, map[string]any{})
}

// reply is the synthetic server's success envelope.
func reply(w http.ResponseWriter, data any) { writeCode(w, 0, "ok", data) }

func newCheckInWorld(t *testing.T, linked bool) *checkInWorld {
	t.Helper()
	world := &checkInWorld{days: 0, refreshStatus: http.StatusOK}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/usercenter/api/refreshToken":
			world.mu.Lock()
			status := world.refreshStatus
			world.mu.Unlock()
			if status != http.StatusOK {
				w.WriteHeader(status)
				replyCode(w, 1001, "unavailable")
				return
			}
			reply(w, map[string]any{"accessToken": "fake-access", "refreshToken": "fake-rotated-" + strconv.Itoa(int(time.Now().UnixNano()))})
		case "/apihub/awapi/signin/state":
			world.mu.Lock()
			body := map[string]any{"day": 17, "days": world.days, "month": 11, "reSignCnt": 0, "todaySign": world.todaySign}
			world.mu.Unlock()
			reply(w, body)
		case "/apihub/awapi/sign/rewards":
			reply(w, []any{
				map[string]any{"icon": "", "name": "本性像素", "num": 60},
				map[string]any{"icon": "", "name": "都市活力", "num": 20},
			})
		case "/apihub/awapi/sign":
			world.mu.Lock()
			world.gamePosts++
			already := world.alreadyGame
			world.mu.Unlock()
			if already {
				replyCode(w, 1001, "今日已签到")
				return
			}
			world.mu.Lock()
			world.todaySign = true
			world.days++
			world.mu.Unlock()
			reply(w, map[string]any{"ok": true})
		case "/apihub/api/getSignState":
			world.mu.Lock()
			signed := world.communityDone
			world.mu.Unlock()
			reply(w, signed)
		case "/apihub/api/signin":
			world.mu.Lock()
			world.community++
			drop, already := world.dropCommunity, world.alreadyCommunity
			world.mu.Unlock()
			if drop {
				if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
					conn.Close()
				}
				return
			}
			if already {
				replyCode(w, 1001, "今日已签到")
				return
			}
			world.mu.Lock()
			world.communityDone = true
			world.mu.Unlock()
			reply(w, map[string]any{"exp": 10, "goldCoin": 5})
		case "/apihub/awapi/yh/roleHome":
			reply(w, map[string]any{
				"userid": "9_42", "roleid": "123", "rolename": "合成测试角色",
				"serverid": "1001", "servername": "天空之城", "lev": 61, "worldlevel": 12,
				"tycoonLevel":  7,
				"staminaValue": 187, "staminaMaxValue": 240,
				"citystaminaValue": 92, "citystaminaMaxValue": 120,
				"dayvalue": 100, "weekcopiesremainCnt": 2,
				"roleloginDays": 128, "charidCnt": 24,
				"achieveProgress": map[string]any{"achievementCnt": 512, "total": 900},
				"areaProgress":    []any{map[string]any{"id": "area-1", "name": "新港", "progress": 12, "total": 30}},
				"characters": []any{map[string]any{
					"id": "1019", "name": "合成角色", "alev": 40, "slev": 3, "likeabilitylev": 7,
					"awakenLev": 2, "elementType": "CHARACTER_ELEMENT_TYPE_PSYCHE", "quality": "ITEM_QUALITY_ORANGE",
				}},
				"realestate": map[string]any{"ownCnt": 3, "showId": "house-1", "showName": "小屋", "total": 9},
				"vehicle":    map[string]any{"ownCnt": 2, "showId": "car-1", "showName": "载具", "total": 5},
			})
		default:
			t.Errorf("unexpected synthetic endpoint %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	store, err := accounts.Open(filepath.Join(dir, "accounts"), tajiduoTestProtector{})
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.Save(accounts.Account{Label: "138****0000"}, accounts.Secret{
		DeviceID: strings.Repeat("a", 32), ChannelUID: "42", ChannelToken: "fake-game-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if linked {
		grant := accounts.TajiduoGrant{
			DeviceID:     "0123456789ABCDEF0123456789ABCDEF",
			CenterUID:    "42",
			RefreshToken: "fake-refresh",
			LaohuUserID:  "42",
			RoleID:       "123",
		}
		if err := store.LinkTajiduo(account.ID, grant); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetLast(account.ID); err != nil {
		t.Fatal(err)
	}
	records, err := checkin.Open(filepath.Join(dir, "checkin"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := tajiduo.NewClient(tajiduo.Config{HTTPClient: server.Client(), BaseURL: server.URL, LaohuBaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	archives, err := NewWishArchives(dir)
	if err != nil {
		t.Fatal(err)
	}
	world.backend = NewTajiduoBackend(store, client, archives, records)
	world.store, world.accountID, world.dir = store, account.ID, dir
	return world
}

func (w *checkInWorld) ledger(t *testing.T, kind state.CheckInKind, roleScoped bool) (checkin.Record, bool) {
	t.Helper()
	records, err := checkin.Open(filepath.Join(w.dir, "checkin"))
	if err != nil {
		t.Fatal(err)
	}
	key := checkin.Key{AccountID: w.accountID, Kind: checkin.Kind(kind), Date: checkin.Day(time.Now())}
	if roleScoped {
		key.RoleID = "123"
	}
	rec, ok, err := records.Lookup(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return rec, ok
}

func (w *checkInWorld) count(fn func() int) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return fn()
}

func TestCheckInStateReadsTheLedgerWithoutNetwork(t *testing.T) {
	world := newCheckInWorld(t, true)
	ctx := context.Background()
	snap, err := world.backend.CheckInState(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if snap.AccountID != world.accountID || !snap.Linked || snap.RoleID != "123" {
		t.Fatalf("linked state not reported: %+v", snap)
	}
	if snap.Day != checkin.Day(time.Now()) {
		t.Fatalf("snapshot day = %q", snap.Day)
	}
	for k := 0; k < state.CheckInKinds; k++ {
		if !snap.Known[k] || snap.Signed[k] || snap.Result[k] != state.CheckResultNone {
			t.Fatalf("fresh account must start empty, kind %d: %+v", k, snap)
		}
	}
	if world.count(func() int { return world.gamePosts + world.community }) != 0 {
		t.Fatal("a ledger read must not write")
	}
	if world.count(func() int { return world.gamePosts }) != 0 || world.count(func() int { return world.community }) != 0 {
		t.Fatal("a ledger read must not post")
	}
}

func TestCheckInStateRefreshConfirmsNothingSigned(t *testing.T) {
	world := newCheckInWorld(t, true)
	snap, err := world.backend.CheckInState(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	for k := 0; k < state.CheckInKinds; k++ {
		if snap.Signed[k] || snap.Result[k] != state.CheckResultNone || snap.Failure[k] != state.CheckInFailureNone {
			t.Fatalf("the server reported nothing signed; got %+v", snap)
		}
	}
	if snap.Fail != state.CheckInFailureNone {
		t.Fatalf("a successful refresh must not report a failure: %v", snap.Fail)
	}
}

func TestCheckInGameSignWritesOnceAndRecordsTheReward(t *testing.T) {
	world := newCheckInWorld(t, true)
	ctx := context.Background()
	out, err := world.backend.Sign(ctx, world.accountID, state.CheckGame, false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != state.CheckResultSuccess || !out.Signed || out.Detail != "本性像素 ×60" {
		t.Fatalf("game sign outcome = %+v", out)
	}
	if got := world.count(func() int { return world.gamePosts }); got != 1 {
		t.Fatalf("expected exactly one game write, got %d", got)
	}
	if rec, ok := world.ledger(t, state.CheckGame, true); !ok || rec.Result != checkin.Success || rec.Detail != "本性像素 ×60" {
		t.Fatalf("ledger = %+v ok=%v", rec, ok)
	}
	snap, err := world.backend.CheckInState(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Result[state.CheckGame] != state.CheckResultSuccess || !snap.Signed[state.CheckGame] {
		t.Fatalf("a recorded success must survive a restart: %+v", snap)
	}
	if snap.Result[state.CheckCommunity] != state.CheckResultNone {
		t.Fatal("the game write must not touch the community task")
	}
	again, err := world.backend.Sign(ctx, world.accountID, state.CheckGame, false)
	if err != nil {
		t.Fatal(err)
	}
	if again.Result != state.CheckResultAlready || !again.Signed {
		t.Fatalf("a second attempt on the same day = %+v", again)
	}
	if got := world.count(func() int { return world.gamePosts }); got != 1 {
		t.Fatalf("an already-signed day must not be posted again, got %d writes", got)
	}
}

func TestCheckInSignRefusesWhenAnotherLauncherHoldsTheLease(t *testing.T) {
	world := newCheckInWorld(t, true)
	other, err := checkin.Open(filepath.Join(world.dir, "checkin"))
	if err != nil {
		t.Fatal(err)
	}
	release, err := other.LockSign()
	if err != nil {
		t.Fatal(err)
	}
	out, err := world.backend.Sign(context.Background(), world.accountID, state.CheckGame, false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != state.CheckResultFailed || out.Failure != state.CheckInFailureBusy {
		t.Fatalf("a leased attempt must report busy, got %+v", out)
	}
	if got := world.count(func() int { return world.gamePosts }); got != 0 {
		t.Fatalf("a busy attempt must not post, got %d writes", got)
	}
	release()
	out, err = world.backend.Sign(context.Background(), world.accountID, state.CheckGame, false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != state.CheckResultSuccess {
		t.Fatalf("attempt after release = %+v", out)
	}
}

func TestCheckInCommunitySignIsIndependent(t *testing.T) {
	world := newCheckInWorld(t, true)
	ctx := context.Background()
	out, err := world.backend.Sign(ctx, world.accountID, state.CheckCommunity, false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != state.CheckResultSuccess || out.Detail != "经验 +10 · 金币 +5" {
		t.Fatalf("community sign outcome = %+v", out)
	}
	if got := world.count(func() int { return world.community }); got != 1 {
		t.Fatalf("expected one community write, got %d", got)
	}
	if got := world.count(func() int { return world.gamePosts }); got != 0 {
		t.Fatal("the community write must not post the game sign-in")
	}
	snap, err := world.backend.CheckInState(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Result[state.CheckCommunity] != state.CheckResultSuccess || snap.Signed[state.CheckCommunity] == false {
		t.Fatalf("community state = %+v", snap)
	}
	if snap.Result[state.CheckGame] != state.CheckResultNone {
		t.Fatal("the community write must not mark the game task done")
	}
}

func TestCheckInLostAnswerIsUnverifiedAndNeverRetriedBlindly(t *testing.T) {
	world := newCheckInWorld(t, true)
	ctx := context.Background()
	world.mu.Lock()
	world.dropCommunity = true
	world.mu.Unlock()
	out, err := world.backend.Sign(ctx, world.accountID, state.CheckCommunity, false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != state.CheckResultUnverified || out.Failure != state.CheckInFailureOffline || out.Signed {
		t.Fatalf("a lost answer must be unverified, not failed or done: %+v", out)
	}
	snap, err := world.backend.CheckInState(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Result[state.CheckCommunity] != state.CheckResultUnverified || snap.Signed[state.CheckCommunity] {
		t.Fatalf("the ledger must keep the lost answer unverified: %+v", snap)
	}
	if got := world.count(func() int { return world.community }); got != 1 {
		t.Fatalf("a lost answer must not be retried automatically, got %d writes", got)
	}
	// The server still reports the day as open, so the stale intent is retired
	// into an answer the user may act on.
	snap, err = world.backend.CheckInState(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Result[state.CheckCommunity] != state.CheckResultFailed || snap.Signed[state.CheckCommunity] {
		t.Fatalf("a server-confirmed open day must retire the intent: %+v", snap)
	}
	if !strings.Contains(snap.Detail[state.CheckCommunity], "可以重新签到") {
		t.Fatalf("detail = %q", snap.Detail[state.CheckCommunity])
	}
	world.mu.Lock()
	world.dropCommunity = false
	world.mu.Unlock()
	retry, err := world.backend.Sign(ctx, world.accountID, state.CheckCommunity, false)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Result != state.CheckResultSuccess {
		t.Fatalf("a user-initiated retry after confirmation = %+v", retry)
	}
}

func TestCheckInRefusesAnotherAccount(t *testing.T) {
	world := newCheckInWorld(t, true)
	if _, err := world.backend.Sign(context.Background(), "another-account", state.CheckGame, false); err == nil {
		t.Fatal("a stale page must not sign for the current account")
	} else if kind := checkInFailureKind(err); kind != state.CheckInFailureMismatch {
		t.Fatalf("failure kind = %v", kind)
	}
	if err := world.backend.SetAuto(context.Background(), "another-account", state.CheckGame, true); err == nil {
		t.Fatal("a stale page must not persist a switch for the current account")
	}
	if got := world.count(func() int { return world.gamePosts }); got != 0 {
		t.Fatalf("no write may leave the process, got %d", got)
	}
}

func TestCheckInAlreadySignedContradictionIsUnverified(t *testing.T) {
	world := newCheckInWorld(t, true)
	world.mu.Lock()
	world.alreadyGame = true
	world.todaySign = false
	world.mu.Unlock()
	out, err := world.backend.Sign(context.Background(), world.accountID, state.CheckGame, false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != state.CheckResultUnverified || out.Signed {
		t.Fatalf("a contradicting already-signed answer must not be a success: %+v", out)
	}
	if !strings.Contains(out.Detail, "状态未确认") {
		t.Fatalf("detail = %q", out.Detail)
	}
}

func TestCheckInAlreadySignedWithConfirmationIsDone(t *testing.T) {
	world := newCheckInWorld(t, true)
	world.mu.Lock()
	world.alreadyGame = true
	world.todaySign = true
	world.mu.Unlock()
	out, err := world.backend.Sign(context.Background(), world.accountID, state.CheckGame, false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != state.CheckResultAlready || !out.Signed {
		t.Fatalf("confirmed already-signed = %+v", out)
	}
	if rec, ok := world.ledger(t, state.CheckGame, true); !ok || rec.Result != checkin.Already {
		t.Fatalf("ledger = %+v ok=%v", rec, ok)
	}
}

func TestCheckInRoleStatusMapsEveryServerField(t *testing.T) {
	world := newCheckInWorld(t, true)
	role, err := world.backend.RoleStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if role.Name != "合成测试角色" || role.Server != "天空之城" || role.RoleID != "123" {
		t.Fatalf("identity = %+v", role)
	}
	if role.Level != 61 || role.WorldLevel != 12 {
		t.Fatalf("levels = %+v", role)
	}
	if role.Stamina != 187 || role.StaminaMax != 240 || role.CityStamina != 92 || role.CityStaminaMax != 120 {
		t.Fatalf("stamina = %+v", role)
	}
	if role.DayValue != 100 || role.WeekRemain != 2 {
		t.Fatalf("activity = %+v", role)
	}
	if role.LoginDays != 128 || role.CharCount != 24 || role.Achievement != 512 || role.AchievementTotal != 900 {
		t.Fatalf("extras = %+v", role)
	}
	if role.FetchedAt.IsZero() {
		t.Fatal("the panel must know when the numbers were read")
	}
	details := role.Details
	if details == nil || len(details.Areas) != 1 || details.Areas[0].Name != "新港" || details.Areas[0].Progress != 12 || details.Areas[0].Total != 30 {
		t.Fatalf("area details = %+v", details)
	}
	if len(details.Characters) != 1 || details.Characters[0].Name != "合成角色" ||
		details.Characters[0].Level != 40 || details.Characters[0].MixLevel != 3 ||
		details.Characters[0].Likeability != 7 || details.Characters[0].Awaken != 2 ||
		details.Characters[0].ElementType != "CHARACTER_ELEMENT_TYPE_PSYCHE" ||
		details.Characters[0].Quality != "ITEM_QUALITY_ORANGE" {
		t.Fatalf("character details = %+v", details.Characters)
	}
	if details.RealEstate != (state.CheckInCollection{Own: 3, Total: 9, ShowName: "小屋"}) {
		t.Fatalf("real estate = %+v", details.RealEstate)
	}
	if details.Vehicle != (state.CheckInCollection{Own: 2, Total: 5, ShowName: "载具"}) {
		t.Fatalf("vehicle = %+v", details.Vehicle)
	}
}

func TestCheckInAutoSwitchesPersistPerAccount(t *testing.T) {
	world := newCheckInWorld(t, true)
	ctx := context.Background()
	if err := world.backend.SetAuto(ctx, world.accountID, state.CheckGame, true); err != nil {
		t.Fatal(err)
	}
	snap, err := world.backend.CheckInState(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Auto[state.CheckGame] || snap.Auto[state.CheckCommunity] {
		t.Fatalf("auto switches = %v", snap.Auto)
	}
}

func TestCheckInUnlinkedAccountNeverWrites(t *testing.T) {
	world := newCheckInWorld(t, false)
	ctx := context.Background()
	snap, err := world.backend.CheckInState(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Linked || snap.AccountID != world.accountID {
		t.Fatalf("unlinked snapshot = %+v", snap)
	}
	if _, err := world.backend.Sign(ctx, world.accountID, state.CheckGame, false); err == nil {
		t.Fatal("an unlinked account must refuse the write")
	} else if kind := checkInFailureKind(err); kind != state.CheckInFailureAuth {
		t.Fatalf("failure kind = %v", kind)
	}
	if _, err := world.backend.RoleStatus(ctx); err == nil {
		t.Fatal("an unlinked account has no role to report")
	}
	if got := world.count(func() int { return world.gamePosts + world.community }); got != 0 {
		t.Fatalf("no write may leave the process, got %d", got)
	}
}

func TestCheckInExpiredCredentialAsksForRelink(t *testing.T) {
	world := newCheckInWorld(t, true)
	world.mu.Lock()
	world.refreshStatus = http.StatusUnauthorized
	world.mu.Unlock()
	snap, err := world.backend.CheckInState(context.Background(), true)
	if err != nil {
		t.Fatalf("an expired credential should keep the readable link: %v", err)
	}
	if !snap.Linked || snap.AccountID != world.accountID {
		t.Fatalf("snapshot = %+v", snap)
	}
	if !snap.NeedsRelink || snap.Fail != state.CheckInFailureAuth {
		t.Fatalf("relink state = linked:%v needs:%v fail:%v", snap.Linked, snap.NeedsRelink, snap.Fail)
	}
}

// recordStore opens the same ledger the backend uses.
func (w *checkInWorld) recordStore(t *testing.T) *checkin.Store {
	t.Helper()
	records, err := checkin.Open(filepath.Join(w.dir, "checkin"))
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func TestCheckInGameSignIsRoleScoped(t *testing.T) {
	world := newCheckInWorld(t, true)
	ctx := context.Background()
	// Another role on the same account already signed today. The sign-state read
	// is keyed by gameId only, so it cannot prove that this role signed.
	world.mu.Lock()
	world.todaySign = true
	world.mu.Unlock()
	out, err := world.backend.Sign(ctx, world.accountID, state.CheckGame, false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != state.CheckResultSuccess || !out.Signed {
		t.Fatalf("this role still needs its own write, got %+v", out)
	}
	if got := world.count(func() int { return world.gamePosts }); got != 1 {
		t.Fatalf("the role-scoped write must be sent once, got %d", got)
	}
	if rec, ok := world.ledger(t, state.CheckGame, true); !ok || rec.Result != checkin.Success {
		t.Fatalf("ledger = %+v ok=%v", rec, ok)
	}
}

func TestCheckInCommunityRecordIsAccountScoped(t *testing.T) {
	world := newCheckInWorld(t, true)
	ctx := context.Background()
	out, err := world.backend.Sign(ctx, world.accountID, state.CheckCommunity, false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != state.CheckResultSuccess {
		t.Fatalf("community sign outcome = %+v", out)
	}
	records := world.recordStore(t)
	rec, ok, err := records.Lookup(ctx, checkin.Key{
		AccountID: world.accountID, Kind: checkin.Community, Date: checkin.Day(time.Now()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || rec.Result != checkin.Success {
		t.Fatalf("the community record must not be pinned to a role: %+v ok=%v", rec, ok)
	}
}

func TestCheckInPendingRecordBlocksAnotherWrite(t *testing.T) {
	world := newCheckInWorld(t, true)
	ctx := context.Background()
	records := world.recordStore(t)
	rec := checkin.Record{
		AccountID: world.accountID, RoleID: "123", Kind: checkin.Game,
		Date: checkin.Day(time.Now()), Result: checkin.Pending, At: time.Now(),
	}
	if err := records.Put(ctx, rec); err != nil {
		t.Fatal(err)
	}
	out, err := world.backend.Sign(ctx, world.accountID, state.CheckGame, false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != state.CheckResultUnverified || out.Detail == "" {
		t.Fatalf("an earlier attempt without an answer must not be repeated blindly: %+v", out)
	}
	if got := world.count(func() int { return world.gamePosts }); got != 0 {
		t.Fatalf("a pending attempt must not be posted again, got %d writes", got)
	}
	// The ledger keeps the open intent: it must not be overwritten by a fresh
	// answer the caller never asked to confirm.
	if rec, ok := world.ledger(t, state.CheckGame, true); !ok || rec.Result != checkin.Pending {
		t.Fatalf("ledger = %+v ok=%v", rec, ok)
	}
}

func TestCheckInCommunityConflictAsksTheUser(t *testing.T) {
	world := newCheckInWorld(t, true)
	ctx := context.Background()
	records := world.recordStore(t)
	rec := checkin.Record{
		AccountID: world.accountID, Kind: checkin.Community,
		Date: checkin.Day(time.Now()), Result: checkin.Success, Detail: "经验 +10 · 金币 +5", At: time.Now(),
	}
	if err := records.Put(ctx, rec); err != nil {
		t.Fatal(err)
	}
	// The server says the community has not been signed today.
	snap, err := world.backend.CheckInState(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Result[state.CheckCommunity] != state.CheckResultUnverified {
		t.Fatalf("a local success the server denies must become unverified: %+v", snap)
	}
	if !strings.Contains(snap.Detail[state.CheckCommunity], "服务器显示今日未签到") {
		t.Fatalf("detail = %q", snap.Detail[state.CheckCommunity])
	}
	again, ok, err := world.recordStore(t).Lookup(ctx, checkin.Key{
		AccountID: world.accountID, Kind: checkin.Community, Date: checkin.Day(time.Now()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || again.Result != checkin.Unverified {
		t.Fatalf("the ledger must record the conflict: %+v ok=%v", again, ok)
	}
}

func TestCheckInRefreshNeverClaimsGameFromAccountState(t *testing.T) {
	world := newCheckInWorld(t, true)
	ctx := context.Background()
	// The account-level game state says signed, but this role has no record of
	// its own; a page refresh must not paint it as done.
	world.mu.Lock()
	world.todaySign = true
	world.mu.Unlock()
	snap, err := world.backend.CheckInState(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Signed[state.CheckGame] {
		t.Fatalf("account-level state must not sign this role off: %+v", snap)
	}
	if snap.Result[state.CheckGame] == state.CheckResultAlready || snap.Result[state.CheckGame] == state.CheckResultSuccess {
		t.Fatalf("a refresh must not claim a game result it has no role-level evidence for: %+v", snap)
	}
	if got := world.count(func() int { return world.gamePosts }); got != 0 {
		t.Fatalf("a refresh must never write, got %d writes", got)
	}
}
