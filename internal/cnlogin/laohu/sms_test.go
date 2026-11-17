// SPDX-License-Identifier: GPL-3.0-or-later

package laohu

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestSMSCooldownSnapshotMatchesConsumedWindow(t *testing.T) {
	clock := &fakeClock{testTime}
	queries := 0
	client := newTestClient(t, clock, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/m/newApi/initConfig" {
			return response(http.StatusOK, `{"code":0,"result":{"loginOptionConfig":[92]}}`), nil
		}
		queries++
		return nil, errors.New("synthetic query failure")
	})
	ctx := context.Background()
	if err := client.Init(ctx); err != nil {
		t.Fatal(err)
	}
	assertPhase(t, client.SendSMS(ctx, "invalid"), PhaseInvalidMainlandPhone)
	if remaining := client.SMSCooldownRemaining(); remaining != 0 || queries != 0 {
		t.Fatalf("invalid phone consumed a window: remaining=%v requests=%d", remaining, queries)
	}
	assertPhase(t, client.SendSMS(ctx, testPhone), PhaseRequestFailed)
	if remaining := client.SMSCooldownRemaining(); remaining != 60*time.Second {
		t.Fatalf("failed request remaining=%v, want 60s", remaining)
	}
	clock.Advance(60*time.Second - time.Millisecond)
	assertPhase(t, client.SendSMS(ctx, "13900000000"), PhaseSMSCooldown)
	if remaining := client.SMSCooldownRemaining(); remaining != time.Millisecond || queries != 1 {
		t.Fatalf("blocked resend changed window: remaining=%v requests=%d", remaining, queries)
	}
	clock.Advance(time.Millisecond)
	if remaining := client.SMSCooldownRemaining(); remaining != 0 {
		t.Fatalf("boundary remaining=%v, want 0", remaining)
	}
	assertPhase(t, client.SendSMS(ctx, testPhone), PhaseRequestFailed)
	if remaining := client.SMSCooldownRemaining(); remaining != 60*time.Second || queries != 2 {
		t.Fatalf("new window not consumed: remaining=%v requests=%d", remaining, queries)
	}
	clock.Advance(61 * time.Second)
	if remaining := client.SMSCooldownRemaining(); remaining != 0 {
		t.Fatalf("expired window remaining=%v, want 0", remaining)
	}
}

func TestSMSCooldownSnapshotDoesNotWaitForConcurrentRequests(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := &fakeClock{testTime}
		started, release := make(chan struct{}), make(chan struct{})
		var releaseOnce sync.Once
		defer releaseOnce.Do(func() { close(release) })
		var queries atomic.Int32
		client := newTestClient(t, clock, func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/m/newApi/initConfig" {
				return response(http.StatusOK, `{"code":0,"result":{"loginOptionConfig":[92]}}`), nil
			}
			if queries.Add(1) == 1 {
				close(started)
			}
			select {
			case <-release:
				return nil, errors.New("synthetic pending request failure")
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
		})
		if err := client.Init(context.Background()); err != nil {
			t.Fatal(err)
		}
		results := make(chan error, 8)
		for range 8 {
			go func() { results <- client.SendSMS(context.Background(), testPhone) }()
		}
		<-started
		snapshot := make(chan time.Duration, 1)
		go func() { snapshot <- client.SMSCooldownRemaining() }()
		select {
		case remaining := <-snapshot:
			if remaining != 60*time.Second {
				t.Fatalf("in-flight window=%v, want 60s", remaining)
			}
		case <-time.After(time.Second):
			t.Fatal("cooldown snapshot waited for the HTTP request lock")
		}
		releaseOnce.Do(func() { close(release) })
		failures, blocked := 0, 0
		for range 8 {
			var api *Error
			if err := <-results; !errors.As(err, &api) {
				t.Fatalf("unexpected SMS result: %v", err)
			}
			switch api.Phase {
			case PhaseRequestFailed:
				failures++
			case PhaseSMSCooldown:
				blocked++
			default:
				t.Fatalf("unexpected SMS phase: %v", api.Phase)
			}
		}
		if queries.Load() != 1 || failures != 1 || blocked != 7 {
			t.Fatalf("concurrent sends: requests=%d failures=%d blocked=%d, want 1/1/7", queries.Load(), failures, blocked)
		}
	})
}
