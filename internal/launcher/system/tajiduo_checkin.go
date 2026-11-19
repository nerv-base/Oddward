// SPDX-License-Identifier: GPL-3.0-or-later
package system

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/nerv-base/Oddward/internal/checkin"
	"github.com/nerv-base/Oddward/internal/cnlogin/accounts"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
	"github.com/nerv-base/Oddward/internal/tajiduo"
)

var _ launcher.CheckInBackend = (*TajiduoBackend)(nil)

// checkInFailure maps an internal error to the coarse reason the page shows.
// Cancellation is passed through so a closing window stops quietly.
func checkInFailure(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return launcher.CheckInError{Kind: checkInFailureKind(err)}
}

// checkInFailureKind is the shared classification for reported and internal
// failures.
func checkInFailureKind(err error) state.CheckInFailure {
	kind := state.CheckInFailureStorage
	switch {
	case errors.Is(err, tajiduo.ErrAuthExpired), errors.Is(err, tajiduo.ErrNeedsRelink),
		errors.Is(err, accounts.ErrNoTajiduo), errors.Is(err, accounts.ErrCorrupt):
		kind = state.CheckInFailureAuth
	case errors.Is(err, tajiduo.ErrChallengeRequired):
		kind = state.CheckInFailureChallenge
	case errors.Is(err, tajiduo.ErrProtocolChanged):
		kind = state.CheckInFailureProtocol
	case errors.Is(err, tajiduo.ErrNetwork):
		kind = state.CheckInFailureOffline
	case errors.Is(err, tajiduo.ErrIdentityMismatch), errors.Is(err, accounts.ErrTajiduoMismatch):
		kind = state.CheckInFailureMismatch
	case errors.Is(err, tajiduo.ErrNoBoundRole), errors.Is(err, tajiduo.ErrInvalidInput):
		kind = state.CheckInFailureNoRole
	case errors.Is(err, checkin.ErrBusy):
		kind = state.CheckInFailureBusy
	case errors.Is(err, checkin.ErrCorrupt):
		kind = state.CheckInFailureStorage
	}
	var reported launcher.CheckInError
	if errors.As(err, &reported) && reported.Kind != state.CheckInFailureNone {
		return reported.Kind
	}
	return kind
}

// CheckInState resolves the saved account, the persisted switches and the
// ledger's answer for today. refresh additionally asks the server to confirm
// today's state; a recoverable refresh failure is reported inside the snapshot
// so the page keeps showing what is already known.
func (b *TajiduoBackend) CheckInState(ctx context.Context, refresh bool) (launcher.CheckInSnapshot, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out launcher.CheckInSnapshot
	out.Day = checkin.Day(time.Now())
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if b.accounts == nil || b.checkin == nil {
		return out, launcher.CheckInError{Kind: state.CheckInFailureStorage}
	}
	id, err := b.accounts.Last()
	if err != nil {
		return out, checkInFailure(err)
	}
	out.AccountID = id
	if id == "" {
		return out, nil
	}
	link, err := b.savedLink(id)
	switch {
	case errors.Is(err, accounts.ErrNoTajiduo):
		return out, nil
	case errors.Is(err, accounts.ErrCorrupt):
		// A damaged attachment stays recoverable: the page asks for a relink
		// instead of hiding the entry point.
		out.NeedsRelink = true
		return out, nil
	case err != nil:
		return out, checkInFailure(err)
	}
	grant := link.session.Grant()
	out.Linked, out.RoleID = true, grant.RoleID
	for k := 0; k < state.CheckInKinds; k++ {
		out.Known[k] = true
	}
	if link.session.NeedsRelink() {
		out.NeedsRelink = true
	}

	auto, err := b.checkin.Auto(ctx, id)
	if err != nil {
		return out, checkInFailure(err)
	}
	out.Auto = auto
	today := checkin.Day(time.Now())
	var haveRec [state.CheckInKinds]bool
	var recs [state.CheckInKinds]checkin.Record
	for k := 0; k < state.CheckInKinds; k++ {
		rec, ok, err := b.checkin.Lookup(ctx, b.signKey(id, grant.RoleID, state.CheckInKind(k), today))
		if err != nil {
			return out, checkInFailure(err)
		}
		if !ok {
			continue
		}
		haveRec[k], recs[k] = true, rec
		out.At[k] = rec.At
		switch rec.Result {
		case checkin.Pending, checkin.Unverified:
			// A write left the process without an answer. It is never shown as
			// done, and never retried blindly: the page asks the server first.
			out.Result[k] = state.CheckResultUnverified
			out.Detail[k] = "上次签到没有收到结果，刷新状态确认后再试"
			out.Signed[k] = false
		case checkin.Success:
			out.Result[k], out.Signed[k] = state.CheckResultSuccess, true
			out.Detail[k] = rec.Detail
		case checkin.Already:
			out.Result[k], out.Signed[k] = state.CheckResultAlready, true
			out.Detail[k] = rec.Detail
		case checkin.Failed:
			out.Result[k] = state.CheckResultFailed
			out.Detail[k] = rec.Detail
		}
	}

	if !refresh || out.NeedsRelink {
		return out, nil
	}
	a, err := link.session.Access(ctx)
	if err != nil {
		if kind := checkInFailureKind(err); kind == state.CheckInFailureAuth || kind == state.CheckInFailureMismatch {
			// The local link is still readable; only its credential is unusable.
			// Keep the snapshot so the page can offer a relink instead of
			// dropping back to "not linked" and losing the account context.
			out.NeedsRelink, out.Fail = true, kind
			return out, nil
		}
		out.Fail = checkInFailureKind(err)
		return out, nil
	}
	for k := 0; k < state.CheckInKinds; k++ {
		kind := state.CheckInKind(k)
		key := b.signKey(id, grant.RoleID, kind, today)
		if kind == state.CheckGame {
			// The server's game state is keyed by gameId only, so it describes
			// the account, not this role. It can neither confirm nor retire a
			// result recorded for this role: only that role's own write can.
			continue
		}
		signed, err := b.client.CommunitySignState(ctx, a)
		if err != nil {
			if failure := checkInFailureKind(err); failure == state.CheckInFailureAuth || failure == state.CheckInFailureMismatch {
				out.NeedsRelink, out.Fail = true, failure
				return out, nil
			}
			out.Fail = checkInFailureKind(err)
			return out, nil
		}
		if signed {
			out.Signed[k], out.Known[k] = true, true
			if out.Result[k] == state.CheckResultNone || out.Result[k] == state.CheckResultUnverified || out.Result[k] == state.CheckResultFailed {
				out.Result[k] = state.CheckResultAlready
				out.Detail[k] = "服务器显示今日已签到"
				out.Failure[k] = state.CheckInFailureNone
				b.finish(ctx, key, checkin.Already, out.Detail[k], atOrNow(out.At[k]))
			}
			continue
		}
		if !haveRec[k] {
			continue
		}
		switch recs[k].Result {
		case checkin.Success, checkin.Already:
			// The ledger and the server disagree about the same account and
			// community. Neither is trusted over the other: the user decides.
			out.Result[k], out.Signed[k] = state.CheckResultUnverified, false
			out.Detail[k] = "服务器显示今日未签到，但本地记录显示已完成；请核对后手动重新签到"
			out.Failure[k] = state.CheckInFailureNone
			b.finish(ctx, key, checkin.Unverified, out.Detail[k], atOrNow(out.At[k]))
		case checkin.Pending, checkin.Unverified:
			// The server is the authority for today: the earlier attempt did not
			// complete the task, so it is retired instead of retried blindly.
			out.Result[k] = state.CheckResultFailed
			out.Detail[k] = "上次签到没有收到结果；服务器确认今日仍未签到，可以重新签到"
			out.Failure[k] = state.CheckInFailureNone
			out.Signed[k], out.Known[k] = false, true
			b.finish(ctx, key, checkin.Failed, out.Detail[k], atOrNow(out.At[k]))
		}
	}
	return out, nil
}

// signKey is the ledger key of one daily task. Community sign-in is scoped to
// the account and the community, so its record must not carry a role: after a
// rebind the account is still signed in.
func (b *TajiduoBackend) signKey(accountID, roleID string, kind state.CheckInKind, day string) checkin.Key {
	key := checkin.Key{AccountID: accountID, Kind: checkin.Kind(kind), Date: day}
	if kind == state.CheckGame {
		key.RoleID = roleID
	}
	return key
}

// Sign runs one daily task for the saved account and the linked role. The ledger
// is consulted before anything is sent: a recorded answer is never written
// twice, and an earlier attempt whose answer never arrived is only repeated when
// the caller confirms it. The intent is stored before the request leaves the
// process, so a crash leaves a pending answer instead of a silent retry.
func (b *TajiduoBackend) Sign(ctx context.Context, accountID string, kind state.CheckInKind, confirm bool) (launcher.CheckInOutcome, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out launcher.CheckInOutcome
	out.Kind = kind
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if !kind.Valid() {
		return out, launcher.CheckInError{Kind: state.CheckInFailureProtocol}
	}
	if b.accounts == nil || b.client == nil || b.checkin == nil {
		return out, launcher.CheckInError{Kind: state.CheckInFailureStorage}
	}
	id, err := b.accounts.Last()
	if err != nil {
		return out, checkInFailure(err)
	}
	if id == "" {
		return out, launcher.CheckInError{Kind: state.CheckInFailureAuth}
	}
	if accountID != id {
		// The page was built for another account (or none): never sign with
		// the current credential on its behalf.
		return out, launcher.CheckInError{Kind: state.CheckInFailureMismatch}
	}
	link, err := b.savedLink(id)
	if err != nil {
		return out, checkInFailure(err)
	}
	if !link.committed || link.session.NeedsRelink() {
		return out, launcher.CheckInError{Kind: state.CheckInFailureAuth}
	}
	a, err := link.session.Access(ctx)
	if err != nil {
		return out, checkInFailure(err)
	}
	// The ledger lock only covers one write, so a second launcher process could
	// still decide "not signed yet" at the same time. Hold a lease for the whole
	// attempt: the loser reports Busy instead of spending the day twice.
	release, err := b.checkin.LockSign()
	if err != nil {
		out.Result, out.Failure = state.CheckResultFailed, checkInFailureKind(err)
		return out, nil
	}
	defer release()
	now := time.Now()
	key := b.signKey(id, a.RoleID, kind, checkin.Day(now))
	out.At, out.Known = now, true

	// The ledger decides what a click may do. A clean answer already recorded
	// for today is reported as it is; an earlier attempt that left the process
	// without an answer is only repeated when the user confirms it.
	if rec, ok, err := b.checkin.Lookup(ctx, key); err != nil {
		out.Result, out.Failure = state.CheckResultFailed, checkInFailureKind(err)
		return out, nil
	} else if ok {
		switch rec.Result {
		case checkin.Success, checkin.Already:
			// The day is spent: report it as already signed instead of
			// pretending this click produced a new reward.
			out.Result, out.Signed, out.Known = state.CheckResultAlready, true, true
			out.Detail = rec.Detail
			if out.Detail == "" {
				out.Detail = "服务器显示今日已签到"
			}
			return out, nil
		case checkin.Pending, checkin.Unverified:
			if !confirm {
				out.Result = state.CheckResultUnverified
				out.Detail = "上次签到没有收到结果；请先刷新状态确认，再次提交需要你确认"
				return out, nil
			}
		}
	}

	if kind == state.CheckCommunity {
		signed, err := b.client.CommunitySignState(ctx, a)
		if err != nil {
			// Nothing was written yet, so this is a clean failure.
			out.Result, out.Failure = state.CheckResultFailed, checkInFailureKind(err)
			return out, nil
		}
		if signed {
			out.Result, out.Signed = state.CheckResultAlready, true
			out.Detail = "服务器显示今日已签到"
			b.finish(ctx, key, checkin.Already, out.Detail, now)
			return out, nil
		}
	}

	// The intent is durable before the request leaves the process: a crash or
	// a lost answer keeps it pending instead of retrying it later.
	if err := b.checkin.Put(ctx, checkin.Record{AccountID: key.AccountID, RoleID: key.RoleID, Kind: key.Kind, Date: key.Date, Result: checkin.Pending, At: now}); err != nil {
		out.Result, out.Failure = state.CheckResultFailed, checkInFailureKind(err)
		return out, nil
	}

	if kind == state.CheckCommunity {
		reward, err := b.client.CommunitySign(ctx, a)
		switch {
		case err == nil:
			out.Result = state.CheckResultSuccess
			if reward.Known {
				out.Detail = "经验 +" + strconv.Itoa(reward.Exp) + " · 金币 +" + strconv.Itoa(reward.Coin)
			}
		case errors.Is(err, tajiduo.ErrAlreadySigned):
			out.Result, out.Signed = state.CheckResultAlready, true
			out.Detail = "服务器显示今日已签到"
		default:
			out.Result, out.Failure = lostAnswer(err)
		}
		b.finish(ctx, key, checkin.Result(out.Result), out.Detail, now)
		return out, nil
	}

	err = b.client.GameSign(ctx, a)
	switch {
	case err == nil:
		out.Result, out.Signed = state.CheckResultSuccess, true
		out.Detail = b.gameRewardText(ctx, a)
	case errors.Is(err, tajiduo.ErrAlreadySigned):
		// The state is account-wide, so it is checked before claiming success.
		if again, stateErr := b.client.GameSignState(ctx, a); stateErr == nil && again.TodaySign {
			out.Result, out.Signed = state.CheckResultAlready, true
			out.Detail = "服务器显示今日奖励已领取"
		} else {
			out.Result, out.Failure = state.CheckResultUnverified, state.CheckInFailureProtocol
			out.Detail = "服务器称已签到，但状态未确认"
		}
	default:
		out.Result, out.Failure = lostAnswer(err)
	}
	b.finish(ctx, key, checkin.Result(out.Result), out.Detail, now)
	return out, nil
}

// lostAnswer classifies a write that did not return a clean answer. Anything
// that may have reached the server is unverified, never failed, so no caller
// can retry it blindly.
func lostAnswer(err error) (state.CheckInResult, state.CheckInFailure) {
	kind := checkInFailureKind(err)
	switch kind {
	case state.CheckInFailureAuth, state.CheckInFailureChallenge:
		return state.CheckResultFailed, kind
	}
	return state.CheckResultUnverified, kind
}

// finish records the settled answer. A failure here cannot change what the
// server already answered, so the outcome stands.
func (b *TajiduoBackend) finish(ctx context.Context, key checkin.Key, result checkin.Result, detail string, at time.Time) {
	_ = b.checkin.Put(ctx, checkin.Record{AccountID: key.AccountID, RoleID: key.RoleID, Kind: key.Kind, Date: key.Date, Result: result, Detail: detail, At: atOrNow(at)})
}

// atOrNow keeps the original attempt time when there is one.
func atOrNow(at time.Time) time.Time {
	if at.IsZero() {
		return time.Now()
	}
	return at
}

// gameRewardText describes the item the server awarded. It is best-effort: an
// unreadable reward list leaves the detail empty rather than inventing one.
func (b *TajiduoBackend) gameRewardText(ctx context.Context, a tajiduo.Access) string {
	signState, err := b.client.GameSignState(ctx, a)
	if err != nil || signState.Days <= 0 {
		return ""
	}
	rewards, err := b.client.GameRewards(ctx, a)
	if err != nil || signState.Days > len(rewards) {
		return ""
	}
	reward := rewards[signState.Days-1]
	if reward.Item == "" {
		return ""
	}
	return reward.Item + " ×" + strconv.Itoa(reward.Count)
}

// RoleStatus reads the role summary; the server is the only source of numbers.
func (b *TajiduoBackend) RoleStatus(ctx context.Context) (state.CheckInRole, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return state.CheckInRole{}, err
	}
	if b.accounts == nil || b.client == nil {
		return state.CheckInRole{}, launcher.CheckInError{Kind: state.CheckInFailureStorage}
	}
	id, err := b.accounts.Last()
	if err != nil {
		return state.CheckInRole{}, checkInFailure(err)
	}
	if id == "" {
		return state.CheckInRole{}, launcher.CheckInError{Kind: state.CheckInFailureAuth}
	}
	link, err := b.savedLink(id)
	if err != nil {
		return state.CheckInRole{}, checkInFailure(err)
	}
	a, err := link.session.Access(ctx)
	if err != nil {
		return state.CheckInRole{}, checkInFailure(err)
	}
	home, err := b.client.RoleHome(ctx, a)
	if err != nil {
		return state.CheckInRole{}, checkInFailure(err)
	}
	return state.CheckInRole{
		RoleID: home.RoleID, Name: home.RoleName, Server: home.ServerName,
		Level: home.Level, WorldLevel: home.WorldLevel,
		Stamina: home.Stamina, StaminaMax: home.StaminaMax,
		CityStamina: home.CityStamina, CityStaminaMax: home.CityStaminaMax,
		DayValue: home.DayValue, WeekRemain: home.WeekCopiesRemain,
		LoginDays: home.LoginDays, CharCount: home.CharCount,
		Achievement: home.Achievement, AchievementTotal: home.AchievementTotal,
		FetchedAt: home.FetchedAt,
		Details:   checkInDetails(home),
	}, nil
}

// checkInDetails copies the optional decorations the role-home response
// carries. It returns nil when the server sent none, so the page can omit the
// whole section instead of drawing empty cards.
func checkInDetails(home tajiduo.RoleHome) *state.CheckInDetails {
	details := &state.CheckInDetails{
		RealEstate: state.CheckInCollection{
			Own: home.RealEstate.Own, Total: home.RealEstate.Total, ShowName: home.RealEstate.ShowName,
		},
		Vehicle: state.CheckInCollection{
			Own: home.Vehicle.Own, Total: home.Vehicle.Total, ShowName: home.Vehicle.ShowName,
		},
	}
	for _, a := range home.Areas {
		details.Areas = append(details.Areas, state.CheckInArea{
			ID: a.ID, Name: a.Name, Progress: a.Progress, Total: a.Total,
		})
	}
	for _, c := range home.Characters {
		details.Characters = append(details.Characters, state.CheckInCharacter{
			ID: c.ID, Name: c.Name, ElementType: c.ElementType, Quality: c.Quality,
			Level: c.Level, MixLevel: c.MixLevel, Likeability: c.Likeability, Awaken: c.Awaken,
		})
	}
	if len(details.Areas) == 0 && len(details.Characters) == 0 &&
		details.RealEstate.Total == 0 && details.Vehicle.Total == 0 {
		return nil
	}
	return details
}

// SetAuto persists one automatic switch for the saved account.
func (b *TajiduoBackend) SetAuto(ctx context.Context, accountID string, kind state.CheckInKind, enabled bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !kind.Valid() {
		return launcher.CheckInError{Kind: state.CheckInFailureProtocol}
	}
	if b.accounts == nil || b.checkin == nil {
		return launcher.CheckInError{Kind: state.CheckInFailureStorage}
	}
	id, err := b.accounts.Last()
	if err != nil {
		return checkInFailure(err)
	}
	if id == "" {
		return launcher.CheckInError{Kind: state.CheckInFailureAuth}
	}
	if accountID != id {
		return launcher.CheckInError{Kind: state.CheckInFailureMismatch}
	}
	if err := b.checkin.SetAuto(ctx, id, checkin.Kind(kind), enabled); err != nil {
		return checkInFailure(err)
	}
	return nil
}
