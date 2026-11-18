// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTxnRepairAcceptsActualVerificationMismatchRecord(t *testing.T) {
	for _, kind := range []string{"repair", "update", "foreign-version", "foreign-list", "foreign-target", "baseline-mismatch"} {
		t.Run(kind, func(t *testing.T) {
			root, _ := adoptFixture(t)
			s := adoptStore(t)
			if _, e := s.AdoptQuick(context.Background(), root, nil); e != nil {
				t.Fatal(e)
			}
			adoptPut(t, root, txnPrefix+"HT/Content/Paks/a.pak", []byte("corrupt first"))
			report, e := s.VerifyInstallation(context.Background(), root, nil)
			if e != nil || report.Record.State != Detected || len(report.Mismatches) != 1 {
				t.Fatalf("real verification mismatch: %+v %v", report, e)
			}
			work := filepath.Join(root, ".oddward")
			txnPut(t, work, "cache/repair", "correct first")
			in := TxnInput{Root: root, WorkRoot: work, TxnID: "repair-from-verification", Candidate: report.Record, Repair: true, Guard: func(context.Context) error { return nil }, FreeBytes: func(context.Context) (int64, error) { return 1 << 20, nil }}
			for i, target := range in.Candidate.Targets {
				source := ""
				if i == 0 {
					source = "cache/repair"
				}
				in.Files = append(in.Files, TxnFile{Path: target.Path, MD5: target.MD5, Size: target.Size, Source: source})
			}
			switch kind {
			case "update":
				in.Repair = false
			case "foreign-version":
				in.Candidate.Version = "9.9"
			case "foreign-list":
				in.Candidate.ListHash = in.Files[0].MD5
			case "foreign-target":
				in.Candidate.Targets = append([]InstallTarget{}, in.Candidate.Targets...)
				in.Candidate.Targets[0].MD5 = in.Candidate.Targets[1].MD5
				in.Files[0].MD5 = in.Candidate.Targets[0].MD5
			case "baseline-mismatch":
				record := report.Record
				record.BaselineMismatch = true
				if e := s.writeInstall(record); e != nil {
					t.Fatal(e)
				}
			}
			_, e = s.Commit(context.Background(), in)
			if kind != "repair" {
				if e == nil {
					t.Fatal("unsafe relaxed baseline accepted")
				}
				data, e := os.ReadFile(filepath.Join(root, filepath.FromSlash(txnPrefix+"HT/Content/Paks/a.pak")))
				if e != nil || string(data) != "corrupt first" {
					t.Fatalf("rejected repair mutated game: %s %v", data, e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			record, e := s.ReadInstall(root)
			if e != nil || record.State != "ready" || record.TxnID != in.TxnID || record.VerifyCoverage != "full" {
				t.Fatalf("repair publication: %+v %v", record, e)
			}
			if txnRead(t, root, txnPrefix+"HT/Content/Paks/a.pak") != "correct first" {
				t.Fatal("repair did not replace corrupt file")
			}
		})
	}
}
