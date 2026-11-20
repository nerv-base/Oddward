// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/database/dbtest"
	"github.com/nerv-base/Oddward/internal/gameres"
)

// sameInstallRecord compares two records field by field. Timestamps use Equal
// because a zero time survives a JSON round trip as a UTC zero value, which is
// not the same struct as time.Time{} even though both are zero.
func sameInstallRecord(a, b InstallRecord) bool {
	return a.Schema == b.Schema && a.InstallKey == b.InstallKey && a.Root == b.Root &&
		a.Mode == b.Mode && a.State == b.State && a.Branch == b.Branch &&
		a.Version == b.Version && a.ListHash == b.ListHash && a.ManifestDigest == b.ManifestDigest &&
		a.TxnID == b.TxnID && a.BaseVersion == b.BaseVersion && a.VerifyCoverage == b.VerifyCoverage &&
		a.VerifiedTargets == b.VerifiedTargets && a.PublishedAt.Equal(b.PublishedAt) &&
		a.CreatedAt.Equal(b.CreatedAt) && a.RootFileID == b.RootFileID &&
		a.OfficialDigest == b.OfficialDigest && a.BaselineMismatch == b.BaselineMismatch &&
		reflect.DeepEqual(a.Targets, b.Targets)
}

// samePreloadRecord compares two records field by field for the same reason.
func samePreloadRecord(a, b PreloadRecord) bool {
	return a.Schema == b.Schema && a.State == b.State && a.Branch == b.Branch &&
		a.Version == b.Version && a.ListHash == b.ListHash && a.Route == b.Route &&
		a.TaskID == b.TaskID && a.ApplyTxnID == b.ApplyTxnID &&
		a.SavedAt.Equal(b.SavedAt) && a.UpdatedAt.Equal(b.UpdatedAt) &&
		a.TargetVersionSeenAt.Equal(b.TargetVersionSeenAt) &&
		reflect.DeepEqual(a.ObservedFormalVersions, b.ObservedFormalVersions) &&
		reflect.DeepEqual(a.Budget, b.Budget) && reflect.DeepEqual(a.Targets, b.Targets) &&
		reflect.DeepEqual(a.Files, b.Files) && reflect.DeepEqual(a.Reused, b.Reused) &&
		reflect.DeepEqual(a.Patches, b.Patches)
}

// writeInstallMirror leaves an install.json record under stateRoot the way the
// previous build did, without any database behind it, and returns its install key.
func writeInstallMirror(t *testing.T, stateRoot, root string, record InstallRecord, mod time.Time) string {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	key, err := InstallKey(root)
	if err != nil {
		t.Fatal(err)
	}
	id, err := installRootFileID(root)
	if err != nil {
		t.Fatal(err)
	}
	record.Schema = 2
	record.InstallKey = key
	record.Root = root
	record.RootFileID = id
	document, err := installJSON(record)
	if err != nil {
		t.Fatal(err)
	}
	path := installMirrorPath(stateRoot, key)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, document, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
	return key
}

// legacyInstallStateRoot builds a mirror tree with two installations and returns
// the state root plus both keys, older first.
func legacyInstallStateRoot(t *testing.T) (string, string, string) {
	t.Helper()
	stateRoot := t.TempDir()
	at := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	older := writeInstallMirror(t, stateRoot, filepath.Join(t.TempDir(), "First Install"), InstallRecord{
		Mode: "adopted", State: "detected", Branch: "PC_150", Version: "0.9.0",
		VerifyCoverage: "none", CreatedAt: at,
	}, at.Add(-time.Hour))
	newer := writeInstallMirror(t, stateRoot, filepath.Join(t.TempDir(), "Second Install"), InstallRecord{
		Mode: "oddward", State: "ready", Branch: "PC_150", Version: "1.0.0",
		VerifyCoverage: "full", VerifiedTargets: 1, CreatedAt: at, PublishedAt: at.Add(30 * time.Minute),
		Targets: []InstallTarget{txnTarget("alpha.pak", "alpha")},
	}, at)
	return stateRoot, older, newer
}

// officialDigestOf computes the digest ReadInstall re-checks against the official
// files of root.
func officialDigestOf(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(adoptOfficialDir))
	config, err := os.ReadFile(filepath.Join(dir, "config.xml"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(dir, "ResList.xml"))
	if err != nil {
		t.Fatal(err)
	}
	return adoptDigest(append(append(config, 0), manifest...))
}

func TestInstallRecordRoundTripMatchesLegacyDocument(t *testing.T) {
	at := time.Date(2026, 11, 18, 9, 30, 0, 0, time.UTC)
	officialRoot, _ := adoptFixture(t)
	emptyRoot := filepath.Join(t.TempDir(), "Round Trip")
	if err := os.MkdirAll(emptyRoot, 0700); err != nil {
		t.Fatal(err)
	}
	build := func(t *testing.T, root, official string, record InstallRecord) InstallRecord {
		t.Helper()
		key, err := InstallKey(root)
		if err != nil {
			t.Fatal(err)
		}
		id, err := installRootFileID(root)
		if err != nil {
			t.Fatal(err)
		}
		record.Schema = 2
		record.InstallKey = key
		record.Root = root
		record.RootFileID = id
		record.OfficialDigest = official
		return record
	}
	// Projection compares targets positionally, so the record keeps its own order.
	published := build(t, officialRoot, officialDigestOf(t, officialRoot), InstallRecord{
		Mode: "oddward", State: "ready", Branch: "PC_150", Version: "1.2.3",
		ListHash:       fmt.Sprintf("%x", md5.Sum([]byte("list"))),
		ManifestDigest: fmt.Sprintf("%x", md5.Sum([]byte("manifest"))),
		TxnID:          "round-trip", BaseVersion: "1.2.2", VerifyCoverage: "full", VerifiedTargets: 2,
		CreatedAt: at, Targets: []InstallTarget{txnTarget("zeta.pak", "zeta"), txnTarget("alpha.pak", "alpha")},
	})
	// An adopted record leaves every optional column empty and a zero timestamp behind.
	detected := build(t, emptyRoot, "", InstallRecord{
		Mode: "adopted", State: "detected", Branch: "PC_150", Version: "1.0.0",
		VerifyCoverage: "none", CreatedAt: at, BaselineMismatch: true,
	})
	for _, tc := range []struct {
		name   string
		record InstallRecord
		mirror bool
	}{
		{"published", published, true},
		{"detected", detected, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := newStore(t, dbtest.Open(t), t.TempDir())
			if err := s.writeInstall(ctx, tc.record); err != nil {
				t.Fatal(err)
			}
			key := tc.record.InstallKey
			// The document the previous build reads is the record's own JSON.
			document, err := installJSON(tc.record)
			if err != nil {
				t.Fatal(err)
			}
			var legacy InstallRecord
			if err := json.Unmarshal(document, &legacy); err != nil {
				t.Fatal(err)
			}
			entry, found, err := readInstallEntry(ctx, s.db.Reader(), key)
			if err != nil || !found {
				t.Fatalf("stored record: found=%v err=%v", found, err)
			}
			if !sameInstallRecord(entry.record, legacy) {
				t.Fatalf("row differs from the legacy document:\n row  %+v\n file %+v", entry.record, legacy)
			}
			if len(entry.record.Targets) > 0 {
				if got, want := entry.record.Targets[0].Path, tc.record.Targets[0].Path; got != want {
					t.Fatalf("target order changed: %q want %q", got, want)
				}
			}
			// A restart against the same database reads the same record, and the
			// public path still validates it.
			restarted := newStore(t, s.db, s.stateRoot)
			again, err := restarted.ReadInstall(tc.record.Root)
			if err != nil {
				t.Fatal(err)
			}
			if !sameInstallRecord(again, entry.record) {
				t.Fatalf("record changed across restart: %+v", again)
			}
			if !tc.mirror {
				return
			}
			// The mirror holds the same document and is the tracked one.
			path := installMirrorPath(s.stateRoot, key)
			mirrored, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(mirrored) != string(document) {
				t.Fatalf("mirror differs from the committed document:\n%s", mirrored)
			}
			mirrors, err := s.db.Mirrors(ctx, InstallDomain)
			if err != nil || len(mirrors) != 1 || mirrors[0].Path != path || mirrors[0].Digest != database.Digest(document) {
				t.Fatalf("mirror bookkeeping: %+v %v", mirrors, err)
			}
		})
	}
}

func TestPreloadRecordRoundTripAcrossEveryState(t *testing.T) {
	input := preloadFixture()
	plan, err := StartPreload(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	base := plan.Record
	base.Reused = []PreloadTarget{base.Targets[0]}
	base.Patches = []gameres.Object{preloadID("patch-payload")}
	base.ObservedFormalVersions = []string{"1.2.3", "1.2.4"}
	base.Budget = PreloadBudget{DownloadBytes: 11, FullBytes: 12, PatchBytes: 13, ReusedBytes: 14, PeakAllocBytes: 15, ReserveBytes: 16}
	base.TargetVersionSeenAt = base.SavedAt
	states := []PreloadState{PreloadUnavailable, PreloadAvailable, PreloadPlanning, PreloadDownloading,
		PreloadPaused, PreloadVerifying, PreloadStaged, PreloadWaitingRelease, PreloadReconcile,
		PreloadApplicable, PreloadApplying, PreloadCompleted, PreloadStale, PreloadRecoveryRequired, PreloadFailed}
	for _, state := range states {
		t.Run(string(state), func(t *testing.T) {
			record := base
			record.State = state
			record.UpdatedAt = base.SavedAt.Add(time.Minute)
			s := preloadStateStore(t, true)
			if err := s.Save(context.Background(), record); err != nil {
				t.Fatal(err)
			}
			document, err := preloadJSON(record)
			if err != nil {
				t.Fatal(err)
			}
			var legacy PreloadRecord
			if err := json.Unmarshal(document, &legacy); err != nil {
				t.Fatal(err)
			}
			got, err := s.Load(context.Background(), record.Branch, record.Version)
			if err != nil {
				t.Fatal(err)
			}
			if !samePreloadRecord(got, legacy) {
				t.Fatalf("row differs from the legacy document:\n row  %+v\n file %+v", got, legacy)
			}
		})
	}
}

func TestInstallImportFixtureTreeAndRepeatedImport(t *testing.T) {
	ctx := context.Background()
	stateRoot, older, newer := legacyInstallStateRoot(t)
	s := newStore(t, dbtest.Open(t), stateRoot)
	source, detail, err := s.ImportLegacy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if source != installMirrorPath(stateRoot, newer) {
		t.Fatalf("tracked source = %q", source)
	}
	if detail != "2 installation records" {
		t.Fatalf("detail = %q", detail)
	}
	check := func(key string) InstallRecord {
		t.Helper()
		document, err := os.ReadFile(installMirrorPath(stateRoot, key))
		if err != nil {
			t.Fatal(err)
		}
		var want InstallRecord
		if err := json.Unmarshal(document, &want); err != nil {
			t.Fatal(err)
		}
		entry, found, err := readInstallEntry(ctx, s.db.Reader(), key)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatalf("%s was not imported", key)
		}
		if !sameInstallRecord(entry.record, want) {
			t.Fatalf("%s imported as %+v want %+v", key, entry.record, want)
		}
		return entry.record
	}
	first := check(older)
	check(newer)
	// Importing the same tree again leaves identical rows.
	if _, _, err := s.ImportLegacy(ctx); err != nil {
		t.Fatal(err)
	}
	if again := check(older); !sameInstallRecord(again, first) {
		t.Fatalf("repeated import changed the record: %+v", again)
	}
	if stale, err := s.db.MirrorStale(ctx, InstallDomain); err != nil || stale {
		t.Fatalf("fresh import reported a stale mirror: %v %v", stale, err)
	}
}

func TestInstallImportReconcilesChangedMirror(t *testing.T) {
	ctx := context.Background()
	stateRoot, _, newer := legacyInstallStateRoot(t)
	s := newStore(t, dbtest.Open(t), stateRoot)
	if _, _, err := s.ImportLegacy(ctx); err != nil {
		t.Fatal(err)
	}
	mirrors, err := s.db.Mirrors(ctx, InstallDomain)
	if err != nil || len(mirrors) != 1 || mirrors[0].Path != installMirrorPath(stateRoot, newer) {
		t.Fatalf("tracked mirror: %+v %v", mirrors, err)
	}
	// A rolled-back build rewrites the tracked mirror and exits.
	path := installMirrorPath(stateRoot, newer)
	document, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var changed InstallRecord
	if err := json.Unmarshal(document, &changed); err != nil {
		t.Fatal(err)
	}
	changed.Version = "1.0.1"
	if document, err = installJSON(changed); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, document, 0600); err != nil {
		t.Fatal(err)
	}
	modified := time.Now().UTC().Add(2 * time.Minute)
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
	if stale, err := s.db.MirrorStale(ctx, InstallDomain); err != nil || !stale {
		t.Fatalf("changed mirror not reported stale: %v %v", stale, err)
	}
	ran, err := s.db.RunImport(ctx, InstallDomain, InstallImportVersion, s.ImportLegacy)
	if err != nil || !ran {
		t.Fatalf("rollback reconcile: ran=%v err=%v", ran, err)
	}
	entry, found, err := readInstallEntry(ctx, s.db.Reader(), newer)
	if err != nil || !found {
		t.Fatalf("record after reconcile: %v", err)
	}
	if entry.record.Version != "1.0.1" {
		t.Fatalf("version = %q", entry.record.Version)
	}
	if stale, err := s.db.MirrorStale(ctx, InstallDomain); err != nil || stale {
		t.Fatalf("reconcile left the mirror stale: %v %v", stale, err)
	}
}

func TestInstallImportCorruptMirrorLeavesRowsUntouched(t *testing.T) {
	ctx := context.Background()
	stateRoot, _, newer := legacyInstallStateRoot(t)
	s := newStore(t, dbtest.Open(t), stateRoot)
	if _, _, err := s.ImportLegacy(ctx); err != nil {
		t.Fatal(err)
	}
	before, found, err := readInstallEntry(ctx, s.db.Reader(), newer)
	if err != nil || !found {
		t.Fatalf("record before the corrupt import: %v", err)
	}
	mirrors, err := s.db.Mirrors(ctx, InstallDomain)
	if err != nil || len(mirrors) != 1 {
		t.Fatalf("tracked mirror: %+v %v", mirrors, err)
	}
	path := mirrors[0].Path
	if err := os.WriteFile(path, []byte(`{"schema": 2,`), 0600); err != nil {
		t.Fatal(err)
	}
	modified := time.Now().UTC().Add(2 * time.Minute)
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
	ran, err := s.db.RunImport(ctx, InstallDomain, InstallImportVersion, s.ImportLegacy)
	if !ran || !errors.Is(err, ErrInvalidState) || !strings.Contains(err.Error(), path) {
		t.Fatalf("corrupt mirror: ran=%v err=%v", ran, err)
	}
	after, found, err := readInstallEntry(ctx, s.db.Reader(), newer)
	if err != nil || !found || !sameInstallRecord(after.record, before.record) {
		t.Fatalf("corrupt mirror changed the record: %+v %v", after.record, err)
	}
	if stale, err := s.db.MirrorStale(ctx, InstallDomain); err != nil || !stale {
		t.Fatalf("failed import settled the mirror: %v %v", stale, err)
	}
}

// writePreloadMirror leaves a preload.json record under stateRoot without a
// database behind it and returns its path.
func writePreloadMirror(t *testing.T, stateRoot string, record PreloadRecord, mod time.Time) string {
	t.Helper()
	document, err := preloadJSON(record)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(stateRoot, "preload", record.Branch, record.Version)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, preloadFileName)
	if err := os.WriteFile(path, document, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
	return path
}

func legacyPreloadRecord(t *testing.T) (string, PreloadRecord) {
	t.Helper()
	stateRoot := t.TempDir()
	plan, err := StartPreload(context.Background(), preloadFixture())
	if err != nil {
		t.Fatal(err)
	}
	record := plan.Record
	record.State = PreloadWaitingRelease
	record.UpdatedAt = record.SavedAt.Add(time.Minute)
	writePreloadMirror(t, stateRoot, record, time.Now().UTC())
	return stateRoot, record
}

func TestPreloadImportFixtureTreeAndReconcilesChangedMirror(t *testing.T) {
	ctx := context.Background()
	stateRoot, record := legacyPreloadRecord(t)
	s, err := NewPreloadStore(dbtest.Open(t), stateRoot, t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ImportLegacy(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(ctx, record.Branch, record.Version)
	if err != nil {
		t.Fatal(err)
	}
	if !samePreloadRecord(got, record) {
		t.Fatalf("imported record %+v want %+v", got, record)
	}
	// A rolled-back build changes the mirror; the next startup imports it again.
	changed := got
	changed.State = PreloadStale
	changed.UpdatedAt = got.UpdatedAt.Add(time.Minute)
	writePreloadMirror(t, stateRoot, changed, time.Now().UTC().Add(2*time.Minute))
	if stale, err := s.db.MirrorStale(ctx, PreloadDomain); err != nil || !stale {
		t.Fatalf("changed preload mirror not reported stale: %v %v", stale, err)
	}
	ran, err := s.db.RunImport(ctx, PreloadDomain, PreloadImportVersion, s.ImportLegacy)
	if err != nil || !ran {
		t.Fatalf("preload reconcile: ran=%v err=%v", ran, err)
	}
	got, err = s.Load(ctx, changed.Branch, changed.Version)
	if err != nil {
		t.Fatal(err)
	}
	if !samePreloadRecord(got, changed) {
		t.Fatalf("reconciled record %+v want %+v", got, changed)
	}
}

func TestPreloadImportCorruptMirrorLeavesRowsUntouched(t *testing.T) {
	ctx := context.Background()
	stateRoot, record := legacyPreloadRecord(t)
	s, err := NewPreloadStore(dbtest.Open(t), stateRoot, t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ImportLegacy(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := s.Load(ctx, record.Branch, record.Version)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateRoot, "preload", record.Branch, record.Version, preloadFileName)
	if err := os.WriteFile(path, []byte(`{"schema": 1,`), 0600); err != nil {
		t.Fatal(err)
	}
	modified := time.Now().UTC().Add(2 * time.Minute)
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
	ran, err := s.db.RunImport(ctx, PreloadDomain, PreloadImportVersion, s.ImportLegacy)
	if !ran || !errors.Is(err, ErrPreloadRecord) || !strings.Contains(err.Error(), path) {
		t.Fatalf("corrupt preload mirror: ran=%v err=%v", ran, err)
	}
	after, err := s.Load(ctx, record.Branch, record.Version)
	if err != nil {
		t.Fatal(err)
	}
	if !samePreloadRecord(after, before) {
		t.Fatalf("corrupt mirror changed the record: %+v", after)
	}
}

// TestTxnCommitFailureLeavesNoRecordAndRecoverableFiles covers the failure the
// database store introduced: the files are published but the record transaction
// does not commit. No row may be left behind, and txn_recover must still roll the
// published files back to the record that survived.
func TestTxnCommitFailureLeavesNoRecordAndRecoverableFiles(t *testing.T) {
	s, in, old := txnFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	guards := 0
	in.Guard = func(context.Context) error {
		guards++
		// Commit guards on entry, before the journal work and before publication.
		// The fourth call comes from the inline recovery, which is refused here so
		// the state that recovery has to repair stays observable.
		if guards > 3 {
			return errors.New("synthetic lease loss")
		}
		return nil
	}
	in.Checkpoint = func(point string) error {
		if point == "before-install-rename" {
			cancel()
		}
		return nil
	}
	if _, err := s.Commit(ctx, in); !errors.Is(err, context.Canceled) {
		t.Fatalf("failed record transaction: %v", err)
	}
	// The files are published.
	if got := txnRead(t, in.Root, "a.bin"); got != "new-a" {
		t.Fatalf("published a.bin = %q", got)
	}
	if _, err := os.Stat(filepath.Join(in.Root, filepath.FromSlash(txnName("c.bin")))); err != nil {
		t.Fatalf("published c.bin missing: %v", err)
	}
	// The record never became durable; a new store over the same database agrees.
	got, err := s.ReadInstall(in.Root)
	if err != nil || got.TxnID != old.TxnID || got.Version != old.Version || !txnSameTargets(got.Targets, old.Targets) {
		t.Fatalf("failed transaction published a record: %+v %v", got, err)
	}
	restarted := newStore(t, s.db, s.stateRoot)
	if again, err := restarted.ReadInstall(in.Root); err != nil || !sameInstallRecord(again, got) {
		t.Fatalf("record changed across restart: %+v %v", again, err)
	}
	// txn_recover restores the baseline the surviving record describes.
	status, err := restarted.Recover(context.Background(), in.Root, in.WorkRoot, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if status.Published || status.LaunchBlocked || status.Action != "rolled_back" {
		t.Fatalf("recovery status %+v", status)
	}
	txnAssertOld(t, in.Root)
	if _, err := restarted.ReadInstall(in.Root); err != nil {
		t.Fatalf("record after recovery: %v", err)
	}
}

func TestPreloadTransitionRejectsIllegalEdges(t *testing.T) {
	input := preloadFixture()
	plan, err := StartPreload(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	base := plan.Record
	for i := range base.Files {
		base.Files[i].State = PreloadFileStaged
	}
	s := preloadStateStore(t, true)
	for _, tc := range []struct{ from, next PreloadState }{
		{PreloadUnavailable, PreloadPlanning},
		{PreloadAvailable, PreloadCompleted},
		{PreloadPlanning, PreloadStaged},
		{PreloadDownloading, PreloadCompleted},
		{PreloadPaused, PreloadCompleted},
		{PreloadVerifying, PreloadCompleted},
		{PreloadStaged, PreloadDownloading},
		{PreloadApplicable, PreloadCompleted},
		{PreloadCompleted, PreloadApplying},
		{PreloadFailed, PreloadAvailable},
		{PreloadStale, PreloadCompleted},
	} {
		record := base
		record.State = tc.from
		record.UpdatedAt = input.Now
		if _, err := s.Transition(context.Background(), record, tc.next, input.Now.Add(time.Minute)); !errors.Is(err, ErrPreloadBlocked) {
			t.Fatalf("%s to %s: %v", tc.from, tc.next, err)
		}
	}
}

// Two installations can plan a preload for the same branch and version. Their
// target lists and blobs are not interchangeable, so each keeps its own row.
func TestPreloadRecordsKeepInstallationsApart(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	rootA, rootB := t.TempDir(), t.TempDir()
	a, err := NewPreloadStore(db, rootA, rootA, true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewPreloadStore(db, rootB, rootB, true)
	if err != nil {
		t.Fatal(err)
	}
	_, record := legacyPreloadRecord(t)
	first, second := record, record
	first.TaskID = record.TaskID + "-a"
	second.TaskID = record.TaskID + "-b"
	if err := a.Save(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(ctx, second); err != nil {
		t.Fatal(err)
	}
	gotA, err := a.Load(ctx, first.Branch, first.Version)
	if err != nil {
		t.Fatal(err)
	}
	gotB, err := b.Load(ctx, second.Branch, second.Version)
	if err != nil {
		t.Fatal(err)
	}
	if !samePreloadRecord(gotA, first) {
		t.Fatalf("installation A read %+v want %+v", gotA, first)
	}
	if !samePreloadRecord(gotB, second) {
		t.Fatalf("installation B read %+v want %+v", gotB, second)
	}
	var rows int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM preload_records").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("kept %d preload rows, want 2", rows)
	}
}

// The preload mirror directory is a rollback aid, so evidence has to come from the
// rows: a record whose mirror never landed, or that an older build removed, still
// has to be listed, and only this installation's records may appear.
func TestPreloadListReadsTheRowsNotTheMirrors(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	root := t.TempDir()
	store, err := NewPreloadStore(db, root, root, true)
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewPreloadStore(db, t.TempDir(), t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	_, record := legacyPreloadRecord(t)
	first, second := record, record
	first.TaskID = record.TaskID + "-a"
	second.TaskID = record.TaskID + "-b"
	second.Version = "9.9.9"
	if err := store.Save(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, second); err != nil {
		t.Fatal(err)
	}
	unrelated := record
	unrelated.TaskID = record.TaskID + "-other"
	if err := other.Save(ctx, unrelated); err != nil {
		t.Fatal(err)
	}

	// Remove the mirror of the second record: the row still stands.
	mirror := filepath.Join(root, "preload", second.Branch, second.Version, preloadFileName)
	if err := os.Remove(mirror); err != nil {
		t.Fatal(err)
	}
	listed, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("List = %d records, want 2", len(listed))
	}
	seen := map[string]bool{}
	for _, r := range listed {
		seen[r.TaskID] = true
	}
	if !seen[first.TaskID] || !seen[second.TaskID] || seen[unrelated.TaskID] {
		t.Fatalf("List returned %v", seen)
	}
	if listed[0].UpdatedAt.Before(listed[1].UpdatedAt) {
		t.Fatalf("List is not newest first: %v", listed)
	}
}
