// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTxnStatusBeforeFirstCommit(t *testing.T) {
	s := adoptStore(t)
	root := t.TempDir()
	work := filepath.Join(root, ".oddward")
	if e := os.Mkdir(work, 0700); e != nil {
		t.Fatal(e)
	}
	st, e := s.TransactionStatus(context.Background(), root, work)
	if e != nil || st.State != "absent" || st.LaunchBlocked || st.Published || st.Resume {
		t.Fatalf("status before first commit %+v %v", st, e)
	}
}
