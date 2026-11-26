// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nerv-base/Oddward/internal/gamelocate"
)

type verifyEntry struct {
	Path       string    `json:"path"`
	MD5        string    `json:"md5"`
	Size       int64     `json:"size"`
	Mtime      time.Time `json:"mtime"`
	FileID     string    `json:"fileID"`
	VerifiedAt time.Time `json:"verifiedAt"`
	Source     string    `json:"source"`
}
type verifyDocument struct {
	Schema         int           `json:"schema"`
	InstallKey     string        `json:"installKey"`
	ManifestDigest string        `json:"manifestDigest"`
	Entries        []verifyEntry `json:"entries"`
}

func (s *Store) verifyRead(record InstallRecord) (verifyDocument, error) {
	empty := verifyDocument{Schema: 1, InstallKey: record.InstallKey, ManifestDigest: record.ManifestDigest}
	r, err := os.OpenRoot(s.stateRoot)
	if err != nil {
		return empty, err
	}
	defer r.Close()
	b, err := stateReadBounded(r, record.InstallKey+"/verify.json")
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return empty, err
	}
	var doc verifyDocument
	if err := stateDecodeJSON(b, &doc); err != nil {
		return empty, fmt.Errorf("%w: verification record", ErrInvalidState)
	}
	if doc.Schema != 1 || doc.InstallKey != record.InstallKey || len(doc.Entries) > 65536 {
		return empty, ErrInvalidState
	}
	if doc.ManifestDigest != record.ManifestDigest {
		return empty, nil
	}
	seen := map[string]bool{}
	for _, e := range doc.Entries {
		if !stateValidMD5(e.MD5) || e.Size <= 0 || e.VerifiedAt.IsZero() || e.Mtime.IsZero() || e.FileID == "" || e.Source != "verify" || seen[e.Path] {
			return empty, ErrInvalidState
		}
		seen[e.Path] = true
	}
	return doc, nil
}
func verifyFingerprint(f *os.File) (int64, time.Time, string, error) {
	st, err := f.Stat()
	if err != nil {
		return 0, time.Time{}, "", err
	}
	if !st.Mode().IsRegular() {
		return 0, time.Time{}, "", ErrInvalidState
	}
	id, err := verifyFileID(f)
	return st.Size(), st.ModTime().UTC(), id, err
}
func verifyMatches(r *os.Root, t InstallTarget, e verifyEntry) bool {
	if e.Path != t.Path || e.MD5 != t.MD5 || e.Size != t.Size {
		return false
	}
	f, err := r.Open(t.Path)
	if err != nil {
		return false
	}
	defer f.Close()
	size, mtime, id, err := verifyFingerprint(f)
	return err == nil && size == e.Size && mtime.Equal(e.Mtime) && id == e.FileID
}
func verifyCheckOfficial(r *os.Root, record InstallRecord) error {
	if record.OfficialDigest == "" {
		return nil
	}
	c, err := stateReadBounded(r, adoptOfficialDir+"config.xml")
	if err != nil {
		return fmt.Errorf("%w: official config changed", ErrStaleState)
	}
	m, err := stateReadBounded(r, adoptOfficialDir+"ResList.xml")
	if adoptOfficialDigest(c, m, err) != record.OfficialDigest {
		return ErrStaleState
	}
	return nil
}

// TrustedTargets checks persisted full-file verification against current size,
// mtime and file ID. A published full baseline without a matching fingerprint
// entry is rehashed for read-only planning; this method never writes state.
// Neither path grants destructive commit permission.
func (s *Store) TrustedTargets(ctx context.Context, root string) ([]InstallTarget, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	record, err := s.ReadInstall(root)
	if err != nil {
		return nil, err
	}
	if record.BaselineMismatch {
		return nil, ErrStaleState
	}
	r, err := os.OpenRoot(record.Root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	if err := verifyCheckOfficial(r, record); err != nil {
		return nil, err
	}
	doc, err := s.verifyRead(record)
	if err != nil {
		return nil, err
	}
	entries := map[string]verifyEntry{}
	for _, e := range doc.Entries {
		entries[e.Path] = e
	}
	trusted := make([]bool, len(record.Targets))
	var toHash []int
	canFallback := record.State == "ready" && record.VerifyCoverage == "full" && record.VerifiedTargets == len(record.Targets)
	for i, t := range record.Targets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entry, exists := entries[t.Path]
		if verifyMatches(r, t, entry) {
			trusted[i] = true
			continue
		}
		if !exists && canFallback {
			toHash = append(toHash, i)
		}
	}
	if len(toHash) > 0 {
		if err := txnRunParallel(ctx, len(toHash), func(pctx context.Context, k int) error {
			idx := toHash[k]
			_, mismatch, herr := verifyHashFile(pctx, r, record.Targets[idx], nil)
			if herr != nil {
				return herr
			}
			if mismatch == "" {
				trusted[idx] = true
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	out := make([]InstallTarget, 0, len(record.Targets))
	for i, t := range record.Targets {
		if trusted[i] {
			out = append(out, t)
		}
	}
	return out, nil
}

// VerifyInstallation persists each completed file before reporting progress.
// Cancelation resumes at file boundaries; a partial file never enters the cache.
func (s *Store) VerifyInstallation(ctx context.Context, selected string, report func(VerifyReport)) (VerifyResult, error) {
	if err := ctx.Err(); err != nil {
		return VerifyResult{}, err
	}
	record, err := s.ReadInstall(selected)
	if err != nil || record.Mode == "adopted" {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return VerifyResult{}, err
		}
		installation, inspectErr := gamelocate.Inspect(ctx, selected)
		if inspectErr != nil {
			return VerifyResult{}, inspectErr
		}
		record, err = s.ReadInstall(installation.Root)
		if err != nil {
			return VerifyResult{}, err
		}
		if record.Branch != installation.Branch {
			return VerifyResult{}, ErrStaleState
		}
	}
	result := VerifyResult{Record: record, Files: len(record.Targets)}
	if len(record.Targets) == 0 {
		return result, fmt.Errorf("%w: no resource baseline", ErrStaleState)
	}
	if record.BaselineMismatch {
		return result, ErrStaleState
	}
	r, err := os.OpenRoot(record.Root)
	if err != nil {
		return result, err
	}
	defer r.Close()
	if err := verifyCheckOfficial(r, record); err != nil {
		return result, err
	}
	doc, err := s.verifyRead(record)
	if err != nil {
		return result, err
	}
	previous := map[string]verifyEntry{}
	for _, entry := range doc.Entries {
		previous[entry.Path] = entry
	}
	// An unfinished audit does not revoke an existing transaction publication.
	// Nonpublished adoption claims are downgraded until the run completes.
	published := record.State == "ready"
	if !published {
		record.State = Adopted
		record.VerifyCoverage = "metadata"
		record.VerifiedTargets = 0
		if err := s.writeInstall(ctx, record); err != nil {
			return result, err
		}
	}
	result.Record = record
	var total int64
	for _, t := range record.Targets {
		total += t.Size
	}
	persistVerify := func() error {
		// Keep only current baseline paths, including completed files from prior runs.
		doc.Entries = nil
		for _, t := range record.Targets {
			if e, ok := previous[t.Path]; ok {
				doc.Entries = append(doc.Entries, e)
			}
		}
		return s.stateWrite(record.InstallKey+"/verify.json", doc, nil)
	}
	var reportMu sync.Mutex
	lastReport := time.Now()
	var lastReportedBytes, lastReportedHashed int64
	var lastReportedFiles int
	emitReport := func(path string, filesDone int, bytesDone, hashedBytes int64, reused, force bool) {
		if report == nil {
			return
		}
		reportMu.Lock()
		defer reportMu.Unlock()
		if ctx.Err() != nil {
			return
		}
		now := time.Now()
		if !force && now.Sub(lastReport) < 125*time.Millisecond {
			return
		}
		if bytesDone > total {
			bytesDone = total
		}
		if bytesDone < lastReportedBytes {
			bytesDone = lastReportedBytes
		}
		if hashedBytes < lastReportedHashed {
			hashedBytes = lastReportedHashed
		}
		if filesDone < lastReportedFiles {
			filesDone = lastReportedFiles
		}
		lastReport = now
		lastReportedBytes = bytesDone
		lastReportedHashed = hashedBytes
		lastReportedFiles = filesDone
		report(VerifyReport{
			Path:        path,
			FilesDone:   filesDone,
			FilesTotal:  len(record.Targets),
			BytesDone:   bytesDone,
			BytesTotal:  total,
			HashedBytes: hashedBytes,
			Reused:      reused,
		})
	}

	var done int64
	var filesDone int
	type hashTask struct {
		index  int
		target InstallTarget
	}
	var toHash []hashTask
	for i, target := range record.Targets {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		entry := previous[target.Path]
		if verifyMatches(r, target, entry) {
			result.Reused++
			result.Verified++
			filesDone++
			done += target.Size
			emitReport(target.Path, filesDone, done, 0, true, true)
			continue
		}
		toHash = append(toHash, hashTask{index: i, target: target})
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	mismatchesByIdx := make([]string, len(record.Targets))
	if len(toHash) > 0 {
		wctx, cancelWorkers := context.WithCancel(ctx)
		defer cancelWorkers()
		var progressBytes atomic.Int64
		progressBytes.Store(done)
		var hashedTotal atomic.Int64
		var completedFiles atomic.Int64
		completedFiles.Store(int64(filesDone))

		type verifyDone struct {
			index    int
			target   InstallTarget
			entry    verifyEntry
			mismatch string
			read     int64
			err      error
		}
		workers := txnWorkerCount(len(toHash))
		jobs := make(chan hashTask)
		completed := make(chan verifyDone, len(toHash))
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for task := range jobs {
					if wctx.Err() != nil {
						return
					}
					var fileRead int64
					entry, mismatch, herr := verifyHashFile(wctx, r, task.target, func(n int64) {
						fileRead += n
						h := hashedTotal.Add(n)
						b := progressBytes.Add(n)
						emitReport(task.target.Path, int(completedFiles.Load()), b, h, false, false)
					})
					res := verifyDone{
						index:    task.index,
						target:   task.target,
						entry:    entry,
						mismatch: mismatch,
						read:     fileRead,
						err:      herr,
					}
					select {
					case completed <- res:
					case <-wctx.Done():
						return
					}
				}
			}()
		}
		go func() {
			defer close(jobs)
			for _, task := range toHash {
				select {
				case <-wctx.Done():
					return
				case jobs <- task:
				}
			}
		}()

		var loopErr error
		for received := 0; received < len(toHash); received++ {
			if err := ctx.Err(); err != nil {
				loopErr = err
				break
			}
			var res verifyDone
			select {
			case <-ctx.Done():
				loopErr = ctx.Err()
			case res = <-completed:
			}
			if loopErr != nil {
				break
			}
			if err := ctx.Err(); err != nil {
				loopErr = err
				break
			}
			if res.err != nil {
				loopErr = res.err
				break
			}
			if res.mismatch != "" {
				delete(previous, res.target.Path)
				mismatchesByIdx[res.index] = res.mismatch
			} else {
				previous[res.target.Path] = res.entry
				result.Verified++
			}
			if err := persistVerify(); err != nil {
				loopErr = err
				break
			}
			if rem := res.target.Size - res.read; rem > 0 {
				progressBytes.Add(rem)
			}
			done += res.target.Size
			filesDone++
			completedFiles.Store(int64(filesDone))
			emitReport(res.target.Path, filesDone, max(done, progressBytes.Load()), hashedTotal.Load(), false, true)
		}
		cancelWorkers()
		wg.Wait()
		if loopErr != nil {
			return result, loopErr
		}
		for i, reason := range mismatchesByIdx {
			if reason != "" {
				result.Mismatches = append(result.Mismatches, Mismatch{record.Targets[i].Path, reason})
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := verifyCheckOfficial(r, record); err != nil {
		return result, err
	}
	// Recheck all fingerprints: an early file may change while later files hash.
	for _, t := range record.Targets {
		if !verifyMatches(r, t, previous[t.Path]) && !verifyHasMismatch(result.Mismatches, t.Path) {
			result.Mismatches = append(result.Mismatches, Mismatch{t.Path, "changed_during_verification"})
			result.Verified--
		}
	}
	record.VerifiedTargets = result.Verified
	if len(result.Mismatches) == 0 {
		if published {
			record.State = "ready"
		} else {
			record.State = Verified
		}
		record.VerifyCoverage = "full"
	} else {
		record.State = Detected
	}
	if err := s.writeInstall(ctx, record); err != nil {
		return result, err
	}
	result.Record = record
	return result, nil
}
func verifyHasMismatch(m []Mismatch, path string) bool {
	for _, v := range m {
		if v.Path == path {
			return true
		}
	}
	return false
}
func verifyHashFile(ctx context.Context, r *os.Root, target InstallTarget, onRead func(int64)) (verifyEntry, string, error) {
	f, err := r.Open(target.Path)
	if errors.Is(err, os.ErrNotExist) {
		return verifyEntry{}, "missing", nil
	}
	if err != nil {
		return verifyEntry{}, "", err
	}
	defer f.Close()
	size, mtime, id, err := verifyFingerprint(f)
	if errors.Is(err, ErrInvalidState) {
		return verifyEntry{}, "not_regular", nil
	}
	if err != nil {
		return verifyEntry{}, "", err
	}
	if size != target.Size {
		return verifyEntry{}, "size", nil
	}
	h := md5.New()
	ptr := txnBufPool.Get().(*[]byte)
	buf := *ptr
	defer txnBufPool.Put(ptr)
	for {
		if err := ctx.Err(); err != nil {
			return verifyEntry{}, "", err
		}
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			if onRead != nil {
				onRead(int64(n))
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return verifyEntry{}, "", err
		}
	}
	afterSize, afterTime, afterID, err := verifyFingerprint(f)
	if err != nil {
		return verifyEntry{}, "", err
	}
	if afterSize != size || !afterTime.Equal(mtime) || afterID != id {
		return verifyEntry{}, "changed_during_verification", nil
	}
	if fmt.Sprintf("%x", h.Sum(nil)) != target.MD5 {
		return verifyEntry{}, "md5", nil
	}
	entry := verifyEntry{Path: target.Path, MD5: target.MD5, Size: size, Mtime: mtime, FileID: id, VerifiedAt: time.Now().UTC(), Source: "verify"}
	if !verifyMatches(r, target, entry) {
		return verifyEntry{}, "changed_during_verification", nil
	}
	return entry, "", nil
}

// TargetFingerprintMatches reports whether target currently matches a persisted
// full-MD5 verification entry (size, mtime, and non-unknown NTFS file ID).
func (s *Store) TargetFingerprintMatches(root string, target InstallTarget) bool {
	if s == nil || root == "" {
		return false
	}
	record, err := s.ReadInstall(root)
	if err != nil || record.BaselineMismatch {
		return false
	}
	r, err := os.OpenRoot(record.Root)
	if err != nil {
		return false
	}
	defer r.Close()
	if err := verifyCheckOfficial(r, record); err != nil {
		return false
	}
	doc, err := s.verifyRead(record)
	if err != nil {
		return false
	}
	for _, e := range doc.Entries {
		if e.Path == target.Path {
			return e.FileID != "unknown" && verifyMatches(r, target, e)
		}
	}
	return false
}

func (s *Store) txnSeedVerifyLedger(install *os.Root, record InstallRecord) *txnLedger {
	ledger := newTxnLedger()
	s.txnSeedVerifyInto(install, record, ledger)
	return ledger
}

func (s *Store) txnSeedVerifyInto(install *os.Root, record InstallRecord, ledger *txnLedger) {
	if ledger == nil || record.InstallKey == "" || record.ManifestDigest == "" {
		return
	}
	doc, err := s.verifyRead(record)
	if err != nil || doc.ManifestDigest != record.ManifestDigest || len(doc.Entries) == 0 {
		return
	}
	for _, e := range doc.Entries {
		if e.FileID == "" || e.FileID == "unknown" {
			continue
		}
		st, err := txnStatStamp(install, e.Path)
		if err != nil || !st.valid() {
			continue
		}
		if st.Size == e.Size && st.MtimeUnix == e.Mtime.UTC().UnixNano() && st.fileID() == e.FileID {
			ledger.put(st, e.MD5)
		}
	}
}

func (s *Store) txnWriteVerify(install *os.Root, record InstallRecord) error {
	if record.InstallKey == "" || record.ManifestDigest == "" || len(record.Targets) == 0 || len(record.Targets) > 65536 {
		return nil
	}
	now := time.Now().UTC()
	doc := verifyDocument{
		Schema:         1,
		InstallKey:     record.InstallKey,
		ManifestDigest: record.ManifestDigest,
		Entries:        make([]verifyEntry, 0, len(record.Targets)),
	}
	for _, t := range record.Targets {
		f, err := install.Open(t.Path)
		if err != nil {
			return err
		}
		size, mtime, id, ferr := verifyFingerprint(f)
		f.Close()
		if ferr != nil || size != t.Size || mtime.IsZero() || id == "" {
			continue
		}
		doc.Entries = append(doc.Entries, verifyEntry{
			Path:       t.Path,
			MD5:        t.MD5,
			Size:       size,
			Mtime:      mtime,
			FileID:     id,
			VerifiedAt: now,
			Source:     "verify",
		})
	}
	if len(doc.Entries) == 0 {
		return nil
	}
	return s.stateWrite(record.InstallKey+"/verify.json", doc, nil)
}
