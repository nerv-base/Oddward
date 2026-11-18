// SPDX-License-Identifier: GPL-3.0-or-later
package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPerfProfileStrictBoundedState(t *testing.T) {
	ctx := context.Background()
	for name, data := range map[string][]byte{
		"unknown":    []byte(`{"version":1,"url":"secret"}`),
		"wrong-case": []byte(`{"Version":1}`),
		"duplicate":  []byte(`{"version":1,"version":1}`),
		"null-rate":  []byte(`{"version":1,"netBytesPerSecond":null}`),
		"negative":   []byte(`{"version":1,"netBytesPerSecond":-1}`),
		"huge-rate":  []byte(`{"version":1,"netBytesPerSecond":1099511627777}`),
		"version":    []byte(`{"version":2}`),
		"trailing":   []byte(`{"version":1} {}`),
		"oversize":   bytes.Repeat([]byte(" "), 32769),
		"raw-key":    []byte(`{"version":1,"volumes":[{"key":"C:\\private"}]}`),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			profileDir := filepath.Join(dir, "download-perf")
			if err := os.Mkdir(profileDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(profileDir, "profile.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			p, err := LoadPerfProfile(ctx, dir)
			if !errors.Is(err, ErrPerfProfile) || strings.Contains(err.Error(), dir) {
				t.Fatalf("expected sanitized rejection, got %v", err)
			}
			if p.Rates("") != (PerfRates{900000, 367 << 20, 939 << 20}) {
				t.Fatal("corrupt data escaped into rates")
			}
			if err = RecordPerfSample(ctx, dir, strings.Repeat("a", 64), PerfSample{NetworkBytes: 36_000_000, NetworkDuration: 20 * time.Second}); !errors.Is(err, ErrPerfProfile) {
				t.Fatalf("overwrote invalid state: %v", err)
			}
		})
	}
}

func TestPerfProfileShortSamplesDoNotCreateStateAndInvalidSamplesReject(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	key := strings.Repeat("a", 64)
	if err := RecordPerfSample(ctx, dir, key, PerfSample{NetworkBytes: 1_000_000, NetworkDuration: 20*time.Second - time.Nanosecond}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "download-perf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("short sample persisted: %v", err)
	}
	for _, sample := range []PerfSample{{NetworkBytes: -1}, {HashReadBytes: 10}, {RebuildIOBytes: 20, RebuildDuration: -time.Second}, {NetworkBytes: 1 << 60, NetworkDuration: 20 * time.Second}} {
		if err := RecordPerfSample(ctx, dir, key, sample); !errors.Is(err, ErrPerfProfile) {
			t.Fatalf("invalid sample accepted: %+v: %v", sample, err)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := RecordPerfSample(ctx, dir, key, PerfSample{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestPerfProfileBoundedVolumeHistoryAndConcurrentWriters(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sample := PerfSample{HashReadBytes: 20 * (500 << 20), HashDuration: 20 * time.Second}
	for i := 0; i < 18; i++ {
		if err := RecordPerfSample(ctx, dir, fmt.Sprintf("%064x", i), sample); err != nil {
			t.Fatal(err)
		}
	}
	p, err := LoadPerfProfile(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Volumes) != 16 || p.Volumes[0].Key != fmt.Sprintf("%064x", 2) {
		t.Fatalf("unbounded or unfair volume history: %+v", p)
	}
	if got := p.Rates(fmt.Sprintf("%064x", 0)); got.HashBytesPerSecond != 367<<20 {
		t.Fatalf("evicted volume borrowed rate: %+v", got)
	}
	key := strings.Repeat("f", 64)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- RecordPerfSample(ctx, dir, key, PerfSample{NetworkBytes: 36_000_000, NetworkDuration: 20 * time.Second})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	p, err = LoadPerfProfile(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Rates(key).NetBytesPerSecond; got != 1709897 {
		t.Fatalf("lost concurrent EMA update: %d", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "download-perf", "profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(dir)) || bytes.Contains(data, []byte("http")) {
		t.Fatal("profile leaked path or URL")
	}
}

func TestPerfProfileBackendVolumeIdentityIsOpaqueAndStable(t *testing.T) {
	dir := t.TempDir()
	first, err := downloadPerfVolumeKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(dir, "child")
	if err = os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	second, err := downloadPerfVolumeKey(child)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !validPerfKey(first) {
		t.Fatalf("volume key not stable: %q %q", first, second)
	}
	b := &GameDownloadBackend{StateRoot: t.TempDir()}
	if err = b.recordDownloadPerf(context.Background(), dir, PerfSample{NetworkBytes: 36_000_000, NetworkDuration: 20 * time.Second}); err != nil {
		t.Fatal(err)
	}
	rates, err := b.downloadPerfRates(context.Background(), child)
	if err != nil || rates.NetBytesPerSecond != 1125000 {
		t.Fatalf("backend rates=%+v err=%v", rates, err)
	}
}

func TestPerfProfileDefaultsAndPersistentTwentySecondEMA(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	key := strings.Repeat("a", 64)
	p, err := LoadPerfProfile(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	want := PerfRates{900000, 367 << 20, 939 << 20}
	if got := p.Rates(key); got != want {
		t.Fatalf("missing profile rates=%+v want=%+v", got, want)
	}
	sample := PerfSample{NetworkBytes: 36_000_000, NetworkDuration: 20 * time.Second, HashReadBytes: 20 * (500 << 20), HashDuration: 20 * time.Second, RebuildIOBytes: 20 * (1000 << 20), RebuildDuration: 20 * time.Second}
	if err = RecordPerfSample(ctx, dir, key, sample); err != nil {
		t.Fatal(err)
	}
	p, err = LoadPerfProfile(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	// EMA alpha=1/4, seeded with the documented default.
	want = PerfRates{1_125_000, 400.25 * (1 << 20), 954.25 * (1 << 20)}
	if got := p.Rates(key); got != want {
		t.Fatalf("persisted rates=%+v want=%+v", got, want)
	}
	if got := p.Rates(strings.Repeat("b", 64)); got != (PerfRates{1_125_000, 367 << 20, 939 << 20}) {
		t.Fatalf("other volume borrowed disk rate: %+v", got)
	}
}
