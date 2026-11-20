// SPDX-License-Identifier: GPL-3.0-or-later

package accounts

import (
	"fmt"
	"testing"
)

// BenchmarkAccountSaveRefresh measures the login-refresh path: the same channel
// identity is sealed again with a fresh data key and written through the row and
// its mirror. New accounts are capped at MaxAccounts, so the loop refreshes one.
func BenchmarkAccountSaveRefresh(b *testing.B) {
	s, _, _ := newTestStore(b)
	sec := sampleSecret(1)
	saved := mustSave(b, s, "主账号", sec)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := s.Save(Account{ID: saved.ID, Label: saved.Label}, sec); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkAccountUnlock measures reading one record, unwrapping its data key and
// opening the AES-GCM payload.
func BenchmarkAccountUnlock(b *testing.B) {
	s, _, _ := newTestStore(b)
	saved := mustSave(b, s, "主账号", sampleSecret(1))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		got, err := s.Unlock(saved.ID)
		if err != nil {
			b.Fatal(err)
		}
		if got.ChannelUID != "uid-1-7788" {
			b.Fatalf("unlocked %q", got.ChannelUID)
		}
	}
}

// BenchmarkAccountList measures the projection the account picker needs: eight
// stored accounts with their labels and Tajiduo state.
func BenchmarkAccountList(b *testing.B) {
	s, _, _ := newTestStore(b)
	for i := 1; i <= 8; i++ {
		mustSave(b, s, fmt.Sprintf("账号 %d", i), sampleSecret(i))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		list, corrupt, err := s.List()
		if err != nil {
			b.Fatal(err)
		}
		if len(list) != 8 || corrupt != 0 {
			b.Fatalf("List = %d accounts, %d corrupt", len(list), corrupt)
		}
	}
}
