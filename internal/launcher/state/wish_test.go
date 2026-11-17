// SPDX-License-Identifier: GPL-3.0-or-later

package state

import "testing"

func wishModel(phase WishPhase) Model {
	m := Initial()
	m.Page = WishAnalysis
	m.Wish = WishState{RoleID: "1001", Phase: phase, Token: 8, Pool: "limited"}
	return m
}

func wishEffect(t *testing.T, before, after Model, fx []Effect, want Effect) {
	t.Helper()
	want.Token = before.Wish.Token + 1
	if len(fx) != 1 || fx[0] != want {
		t.Fatalf("effects=%+v, want %+v", fx, want)
	}
	wantFailure := before.Wish.Failure
	switch want.Kind {
	case WishList, WishLoad, WishDelete:
		wantFailure = WishFailureNone
	}
	if after.Wish.Token != want.Token || after.Wish.Failure != wantFailure || !after.Wish.Busy {
		t.Fatalf("operation did not preserve failure contract, increment token and become busy: %+v", after.Wish)
	}
	if after.Token != before.Token || after.Page != before.Page || after.Overlay != before.Overlay || after.Wish.Pool != before.Wish.Pool {
		t.Fatal("wish operation changed unrelated state")
	}
}

func TestWishImportReportBelongsToSelectedRole(t *testing.T) {
	for _, count := range []int{0, 9} {
		m := wishModel(WishReady)
		n, fx := Reduce(m, Event{Kind: WishImported, Token: m.Wish.Token, WishRoleID: m.Wish.RoleID, WishNewCount: count, WishOutside: 3, WishConflicts: 1})
		if !n.Wish.ImportReported || n.Wish.NewCount != count || len(fx) != 0 {
			t.Fatalf("successful import must report even zero additions: %+v", n.Wish)
		}
		for _, e := range []Event{{Kind: WishSelectRole, WishRoleID: "2002"}, {Kind: WishRequestList}, {Kind: WishListed, Token: n.Wish.Token, WishRoleID: "2002", WishCount: 1}, {Kind: WishDeleted, Token: n.Wish.Token}} {
			after, _ := Reduce(n, e)
			if after.Wish.ImportReported || after.Wish.NewCount != 0 || after.Wish.Outside != 0 || after.Wish.Conflicts != 0 {
				t.Fatalf("event %v retained report for old archive: %+v", e.Kind, after.Wish)
			}
		}
		deleting := n
		deleting.Wish.Confirm = WishConfirmation{Kind: WishConfirmDelete, RoleID: n.Wish.RoleID, Count: 9}
		deleting, _ = Reduce(deleting, Event{Kind: WishAcceptConfirm})
		if deleting.Wish.ImportReported {
			t.Fatal("confirmed deletion must clear import report")
		}
		stale, _ := Reduce(n, Event{Kind: WishImported, Token: n.Wish.Token - 1, WishNewCount: 99})
		if stale != n {
			t.Fatal("stale import must preserve current report")
		}
	}
}

func TestWishListAndLoad(t *testing.T) {
	m := wishModel(WishFailed)
	m.Wish.Failure = WishFailureIO
	n, fx := Reduce(m, Event{Kind: WishRequestList})
	wishEffect(t, m, n, fx, Effect{Kind: WishList})
	if n.Wish.Phase != WishLoading {
		t.Fatal("list must load")
	}
	m = n
	n, fx = Reduce(m, Event{Kind: WishListed, Token: m.Wish.Token, WishRoleID: "2002", WishCount: 2})
	wishEffect(t, m, n, fx, Effect{Kind: WishLoad, RoleID: "2002"})
	if n.Wish.RoleID != "2002" || n.Wish.Phase != WishLoading {
		t.Fatalf("listed=%+v", n.Wish)
	}
	m = n
	n, fx = Reduce(m, Event{Kind: WishLoaded, Token: m.Wish.Token})
	if n.Wish.Phase != WishReady || n.Wish.RoleID != "2002" || len(fx) != 0 || n.Wish.Token != m.Wish.Token {
		t.Fatalf("loaded=%+v fx=%+v", n.Wish, fx)
	}
	m = wishModel(WishLoading)
	n, fx = Reduce(m, Event{Kind: WishListed, Token: m.Wish.Token, WishCount: 0})
	if n.Wish.RoleID != "" || n.Wish.Phase != WishReady || len(fx) != 0 || n.Wish.Token != m.Wish.Token {
		t.Fatalf("empty=%+v fx=%+v", n.Wish, fx)
	}
}

func TestWishRoleSelection(t *testing.T) {
	m := wishModel(WishFailed)
	m.Wish.Failure = WishFailureCorrupt
	n, fx := Reduce(m, Event{Kind: WishSelectRole, WishRoleID: "2002"})
	wishEffect(t, m, n, fx, Effect{Kind: WishLoad, RoleID: "2002"})
	if n.Wish.Phase != WishLoading || n.Wish.RoleID != "2002" {
		t.Fatalf("selection=%+v", n.Wish)
	}
}

func TestWishIntentGuards(t *testing.T) {
	intents := []EventKind{WishRequestList, WishSelectRole, WishRequestDelete, WishRequestImport, WishRequestExport}
	for _, guard := range []string{"overlay", "page", "confirm", "closed"} {
		for _, kind := range intents {
			m := wishModel(WishReady)
			switch guard {
			case "overlay":
				m.Overlay = Settings
			case "page":
				m.Page = CheckIn
			case "confirm":
				m.Wish.Confirm = WishConfirmation{Kind: WishConfirmDelete, RoleID: "1001", Count: 2}
			case "closed":
				m.Closed = true
			}
			n, fx := Reduce(m, Event{Kind: kind, WishRoleID: "2002", WishCount: 3, WishFormat: WishNTEUID})
			if n != m || len(fx) != 0 {
				t.Fatalf("%s admitted %v: %+v %+v", guard, kind, n.Wish, fx)
			}
		}
	}
	for _, kind := range []EventKind{WishAcceptConfirm, WishCancelConfirm} {
		m := wishModel(WishReady)
		n, fx := Reduce(m, Event{Kind: kind})
		if n != m || len(fx) != 0 {
			t.Fatalf("confirmation without prompt admitted %v", kind)
		}
	}
}

func wishResults() []EventKind {
	return []EventKind{WishListed, WishLoaded, WishDeleted, WishImportPicked, WishImported, WishExportPicked, WishExported, WishOperationFailed, WishImportNeedsConfirmation}
}

func TestWishResultsOnlyUseToken(t *testing.T) {
	for _, kind := range wishResults() {
		m := wishModel(WishLoading)
		m.Wish.Busy = true
		m.Page, m.Overlay = Home, Settings
		e := Event{Kind: kind, Token: m.Wish.Token - 1, WishRoleID: "2002", WishCount: 2, Path: "archive.json", WishFailure: WishFailureFormat}
		n, fx := Reduce(m, e)
		if n != m || len(fx) != 0 {
			t.Fatalf("stale %v changed model", kind)
		}
		e.Token = m.Wish.Token
		n, fx = Reduce(m, e)
		if n == m && len(fx) == 0 {
			t.Fatalf("current %v ignored away from page with overlay", kind)
		}
		if n.Page != Home || n.Overlay != Settings {
			t.Fatal("result changed navigation")
		}
		m.Closed = true
		n, fx = Reduce(m, e)
		if n != m || len(fx) != 0 {
			t.Fatalf("closed accepted %v", kind)
		}
	}
}

func TestWishDeleteConfirmation(t *testing.T) {
	m := wishModel(WishReady)
	n, fx := Reduce(m, Event{Kind: WishRequestDelete, WishCount: 37})
	if n.Wish.Confirm != (WishConfirmation{Kind: WishConfirmDelete, RoleID: "1001", Count: 37}) || len(fx) != 0 || n.Wish.Token != m.Wish.Token {
		t.Fatalf("delete prompt=%+v fx=%+v", n.Wish, fx)
	}
	m = n
	n, fx = Reduce(m, Event{Kind: WishAcceptConfirm})
	wishEffect(t, m, n, fx, Effect{Kind: WishDelete, RoleID: "1001"})
	if n.Wish.Phase != WishLoading || n.Wish.Confirm != (WishConfirmation{}) {
		t.Fatalf("delete acceptance=%+v", n.Wish)
	}
	m = n
	n, fx = Reduce(m, Event{Kind: WishDeleted, Token: m.Wish.Token})
	wishEffect(t, m, n, fx, Effect{Kind: WishList})
	if n.Wish.Phase != WishLoading {
		t.Fatal("delete must relist")
	}
}

func TestWishEmptyRoleCannotDeleteOrExport(t *testing.T) {
	for _, kind := range []EventKind{WishRequestDelete, WishRequestExport} {
		for _, format := range []WishFormat{WishOddward, WishNTEUID} {
			m := wishModel(WishIdle)
			m.Wish.RoleID = ""
			n, fx := Reduce(m, Event{Kind: kind, WishCount: 2, WishFormat: format})
			if n != m || len(fx) != 0 {
				t.Fatalf("empty role admitted %v", kind)
			}
		}
	}
}

func TestWishImportConfirmationAndReport(t *testing.T) {
	for _, role := range []string{"", "1001"} {
		m := wishModel(WishReady)
		m.Wish.RoleID = role
		n, fx := Reduce(m, Event{Kind: WishRequestImport})
		wishEffect(t, m, n, fx, Effect{Kind: WishPickImport})
		m = n
		n, fx = Reduce(m, Event{Kind: WishImportPicked, Token: m.Wish.Token, Path: "archive.json"})
		wishEffect(t, m, n, fx, Effect{Kind: WishImport, RoleID: role, Path: "archive.json"})
		m = n
		n, fx = Reduce(m, Event{Kind: WishImportNeedsConfirmation, Token: m.Wish.Token, WishRoleID: "2002", WishCount: 9, Path: "archive.json"})
		if n.Wish.Confirm != (WishConfirmation{Kind: WishConfirmImport, RoleID: "2002", Count: 9, Path: "archive.json"}) || n.Wish.Phase != WishReady || len(fx) != 0 {
			t.Fatalf("import prompt=%+v fx=%+v", n.Wish, fx)
		}
		m = n
		n, fx = Reduce(m, Event{Kind: WishAcceptConfirm})
		wishEffect(t, m, n, fx, Effect{Kind: WishImport, RoleID: "2002", Path: "archive.json", Confirmed: true})
		if n.Wish.Confirm != (WishConfirmation{}) {
			t.Fatal("import confirmation retained")
		}
		m = n
		n, fx = Reduce(m, Event{Kind: WishImported, Token: m.Wish.Token, WishRoleID: "2002", WishNewCount: 9, WishOutside: 3, WishConflicts: 1})
		if n.Wish.RoleID != "2002" || n.Wish.Phase != WishReady || n.Wish.NewCount != 9 || n.Wish.Outside != 3 || n.Wish.Conflicts != 1 || len(fx) != 0 {
			t.Fatalf("import result=%+v fx=%+v", n.Wish, fx)
		}
	}
}

func TestWishExportFlows(t *testing.T) {
	for _, format := range []WishFormat{WishOddward, WishNTEUID} {
		m := wishModel(WishReady)
		n, fx := Reduce(m, Event{Kind: WishRequestExport, WishFormat: format})
		if format == WishNTEUID {
			if n.Wish.Confirm != (WishConfirmation{Kind: WishConfirmLossy, RoleID: "1001"}) || len(fx) != 0 || n.Wish.Token != m.Wish.Token {
				t.Fatalf("lossy prompt=%+v fx=%+v", n.Wish, fx)
			}
			m = n
			n, fx = Reduce(m, Event{Kind: WishAcceptConfirm})
			if n.Wish.Confirm != (WishConfirmation{}) {
				t.Fatal("lossy confirmation retained")
			}
		}
		wishEffect(t, m, n, fx, Effect{Kind: WishPickExport, RoleID: "1001", WishFormat: format})
		m = n
		n, fx = Reduce(m, Event{Kind: WishExportPicked, Token: m.Wish.Token, Path: "export.json", WishFormat: format})
		wishEffect(t, m, n, fx, Effect{Kind: WishExport, RoleID: "1001", Path: "export.json", WishFormat: format})
		m = n
		n, fx = Reduce(m, Event{Kind: WishExported, Token: m.Wish.Token})
		if n.Wish.Phase != WishReady || len(fx) != 0 {
			t.Fatalf("exported=%+v fx=%+v", n.Wish, fx)
		}
	}
}

func TestWishCancelledDialogsPreserveState(t *testing.T) {
	for _, phase := range []WishPhase{WishIdle, WishReady, WishFailed} {
		for _, request := range []EventKind{WishRequestImport, WishRequestExport} {
			if request == WishRequestExport && phase != WishReady {
				continue
			}
			m := wishModel(phase)
			m.Wish.Failure = WishFailureIO
			n, fx := Reduce(m, Event{Kind: request, WishFormat: WishOddward})
			if len(fx) != 1 {
				t.Fatalf("dialog did not start: %+v", fx)
			}
			picked := WishImportPicked
			if request == WishRequestExport {
				picked = WishExportPicked
			}
			n, fx = Reduce(n, Event{Kind: picked, Token: n.Wish.Token})
			want := m
			want.Wish.Token++
			if n != want || len(fx) != 0 {
				t.Fatalf("cancelled dialog=%+v, want %+v fx=%+v", n.Wish, want.Wish, fx)
			}
		}
	}
}

func TestWishCancelledConfirmationsRestorePhase(t *testing.T) {
	for _, phase := range []WishPhase{WishIdle, WishReady, WishFailed} {
		for _, kind := range []WishConfirmationKind{WishConfirmDelete, WishConfirmImport, WishConfirmLossy} {
			m := wishModel(phase)
			m.Wish.Confirm = WishConfirmation{Kind: kind, RoleID: "1001", Count: 7, Path: "archive.json"}
			m.Wish.Failure = WishFailureCorrupt
			n, fx := Reduce(m, Event{Kind: WishCancelConfirm})
			want := m
			want.Wish.Confirm = WishConfirmation{}
			if n != want || len(fx) != 0 {
				t.Fatalf("cancelled confirmation=%+v fx=%+v", n.Wish, fx)
			}
		}
	}
}

func TestWishFailures(t *testing.T) {
	for _, failure := range []WishFailureKind{WishFailureIO, WishFailureCorrupt, WishFailureRoleMismatch, WishFailureFormat} {
		m := wishModel(WishLoading)
		n, fx := Reduce(m, Event{Kind: WishOperationFailed, Token: m.Wish.Token, WishFailure: failure, Message: "backend detail"})
		if n.Wish.Phase != WishFailed || n.Wish.Failure != failure || len(fx) != 0 {
			t.Fatalf("failure=%+v fx=%+v", n.Wish, fx)
		}
	}
}

func TestWishReducerHandledBoundary(t *testing.T) {
	m := wishModel(WishReady)
	for kind := WishRequestList; kind <= WishImportNeedsConfirmation; kind++ {
		guarded := m
		guarded.Page, guarded.Overlay = Home, Settings
		_, _, handled := ReduceWish(guarded, Event{Kind: kind, Token: m.Wish.Token - 1})
		if !handled {
			t.Fatalf("wish reducer did not consume %v", kind)
		}
		guarded.Closed = true
		n, fx, handled := ReduceWish(guarded, Event{Kind: kind, Token: m.Wish.Token})
		if !handled || n != guarded || len(fx) != 0 {
			t.Fatalf("closed wish reducer admitted %v", kind)
		}
	}
	for _, kind := range []EventKind{Primary, DetailFailed, StartFeature, LoginRequested, OpenNewsBanner, ManagedLoad, UpdateCheck, NetworkApply, EventKind(255)} {
		n, fx, handled := ReduceWish(m, Event{Kind: kind})
		if handled || n != m || len(fx) != 0 {
			t.Fatalf("wish reducer swallowed unrelated %v", kind)
		}
	}
}

func TestWishLoadingAndBusyBlockNewOperations(t *testing.T) {
	for _, phase := range []WishPhase{WishIdle, WishLoading, WishReady, WishFailed} {
		for _, kind := range []EventKind{WishRequestList, WishSelectRole, WishRequestDelete, WishRequestImport, WishRequestExport, WishAcceptConfirm} {
			m := wishModel(phase)
			m.Wish.Busy = true
			if kind == WishAcceptConfirm {
				m.Wish.Confirm = WishConfirmation{Kind: WishConfirmDelete, RoleID: "1001"}
			}
			n, fx := Reduce(m, Event{Kind: kind, WishRoleID: "2002"})
			if n != m || len(fx) != 0 {
				t.Fatalf("busy phase %v accepted %v: %+v fx=%+v", phase, kind, n.Wish, fx)
			}
		}
	}
	for _, phase := range []WishPhase{WishIdle, WishLoading, WishFailed} {
		for _, kind := range []EventKind{WishRequestDelete, WishRequestExport, WishRequestImport} {
			if kind == WishRequestImport && phase != WishLoading {
				continue
			}
			m := wishModel(phase)
			n, fx := Reduce(m, Event{Kind: kind})
			if n != m || len(fx) != 0 {
				t.Fatalf("phase %v accepted %v: %+v fx=%+v", phase, kind, n.Wish, fx)
			}
		}
	}
}

func TestWishConfirmationsRequireVisiblePage(t *testing.T) {
	for _, confirm := range []WishConfirmationKind{WishConfirmDelete, WishConfirmImport, WishConfirmLossy} {
		for _, kind := range []EventKind{WishAcceptConfirm, WishCancelConfirm} {
			for _, overlay := range []Overlay{NoOverlay, Settings} {
				for _, page := range []Page{Home, WishAnalysis, CheckIn} {
					if page == WishAnalysis && overlay == NoOverlay {
						continue
					}
					m := wishModel(WishReady)
					m.Page, m.Overlay = page, overlay
					m.Wish.Confirm = WishConfirmation{Kind: confirm, RoleID: "1001", Path: "import.json"}
					n, fx := Reduce(m, Event{Kind: kind})
					if n != m || len(fx) != 0 {
						t.Fatalf("hidden confirmation %v accepted %v", confirm, kind)
					}
				}
			}
		}
	}
}

func TestWishTerminalResultsReleaseBusy(t *testing.T) {
	for _, kind := range []EventKind{WishLoaded, WishImported, WishExported, WishOperationFailed, WishImportNeedsConfirmation, WishListed, WishImportPicked, WishExportPicked} {
		m := wishModel(WishReady)
		m.Wish.Busy = true
		n, fx := Reduce(m, Event{Kind: kind, Token: m.Wish.Token, WishRoleID: "2002", WishFailure: WishFailureIO})
		if n.Wish.Busy || len(fx) != 0 || n.Wish.Token != m.Wish.Token {
			t.Fatalf("terminal %v retained busy or advanced token: %+v fx=%+v", kind, n.Wish, fx)
		}
	}
}

func TestWishImportPreservesFailureUntilSuccess(t *testing.T) {
	m := wishModel(WishFailed)
	m.Wish.Failure = WishFailureCorrupt
	n, fx := Reduce(m, Event{Kind: WishRequestImport})
	wishEffect(t, m, n, fx, Effect{Kind: WishPickImport})
	if n.Wish.Phase != WishFailed {
		t.Fatal("picker changed failed phase")
	}
	m = n
	n, fx = Reduce(m, Event{Kind: WishImportPicked, Token: m.Wish.Token, Path: "import.json"})
	wishEffect(t, m, n, fx, Effect{Kind: WishImport, RoleID: m.Wish.RoleID, Path: "import.json"})
	if n.Wish.Phase != WishFailed {
		t.Fatal("import changed failed phase")
	}
	m = n
	n, fx = Reduce(m, Event{Kind: WishImportNeedsConfirmation, Token: m.Wish.Token, WishRoleID: "2002", Path: "import.json"})
	if n.Wish.Phase != WishFailed || n.Wish.Failure != WishFailureCorrupt || n.Wish.Busy || len(fx) != 0 || n.Wish.Token != m.Wish.Token {
		t.Fatalf("confirmation lost failed state: %+v", n.Wish)
	}
	m = n
	n, fx = Reduce(m, Event{Kind: WishAcceptConfirm})
	wishEffect(t, m, n, fx, Effect{Kind: WishImport, RoleID: "2002", Path: "import.json", Confirmed: true})
	if n.Wish.Phase != WishFailed {
		t.Fatal("confirmed import changed failed phase")
	}
	m = n
	n, fx = Reduce(m, Event{Kind: WishImported, Token: m.Wish.Token, WishRoleID: "2002", WishNewCount: 3})
	if n.Wish.Phase != WishReady || n.Wish.Failure != WishFailureNone || n.Wish.Busy || n.Wish.RoleID != "2002" || n.Wish.NewCount != 3 || len(fx) != 0 {
		t.Fatalf("import did not recover failure: %+v fx=%+v", n.Wish, fx)
	}
}

func TestWishCorruptArchiveCanBeDeleted(t *testing.T) {
	m := wishModel(WishFailed)
	m.Wish.Failure = WishFailureCorrupt
	n, fx := Reduce(m, Event{Kind: WishRequestDelete, WishCount: 0})
	if n.Wish.Confirm != (WishConfirmation{Kind: WishConfirmDelete, RoleID: "1001"}) || n.Wish.Phase != WishFailed || n.Wish.Failure != WishFailureCorrupt || n.Wish.Busy || len(fx) != 0 {
		t.Fatalf("corrupt delete confirmation=%+v fx=%+v", n.Wish, fx)
	}
	m = n
	n, fx = Reduce(m, Event{Kind: WishAcceptConfirm})
	wishEffect(t, m, n, fx, Effect{Kind: WishDelete, RoleID: "1001"})
	if n.Wish.Phase != WishLoading {
		t.Fatal("confirmed corrupt delete did not load")
	}
	m = wishModel(WishFailed)
	m.Wish.Failure = WishFailureIO
	n, fx = Reduce(m, Event{Kind: WishRequestDelete})
	if n != m || len(fx) != 0 {
		t.Fatal("I/O failure allowed archive deletion")
	}
}

func TestWishExportFailureKeepsArchiveSelection(t *testing.T) {
	m := wishModel(WishReady)
	m.Wish.NewCount, m.Wish.Outside, m.Wish.Conflicts = 3, 4, 1
	m, _ = Reduce(m, Event{Kind: WishRequestExport})
	m, _ = Reduce(m, Event{Kind: WishExportPicked, Token: m.Wish.Token, Path: "export.json"})
	n, fx := Reduce(m, Event{Kind: WishOperationFailed, Token: m.Wish.Token, WishFailure: WishFailureIO})
	want := m
	want.Wish.Phase, want.Wish.Failure, want.Wish.Busy = WishFailed, WishFailureIO, false
	if n != want || len(fx) != 0 {
		t.Fatalf("export failure discarded archive state: %+v fx=%+v", n.Wish, fx)
	}
}

func TestWishModelRemainsComparable(t *testing.T) {
	m := wishModel(WishReady)
	n := m
	if n != m {
		t.Fatal("copied model differs")
	}
	n.Wish.Confirm.Path = "archive.json"
	if n == m {
		t.Fatal("wish data omitted from model equality")
	}
}

func TestFeaturePreviewOnlyControlsCheckIn(t *testing.T) {
	m := wishModel(WishIdle)
	for _, kind := range []EventKind{StartFeature, CompleteFeature, FailFeature, ResetFeature} {
		n, fx := Reduce(m, Event{Kind: kind, Page: WishAnalysis})
		if n != m || len(fx) != 0 {
			t.Fatalf("legacy preview changed wish for %v", kind)
		}
	}
	m.Page = CheckIn
	for _, tc := range []struct {
		kind  EventKind
		phase FeatureState
	}{
		{StartFeature, FeatureLoading}, {CompleteFeature, FeatureReady}, {ResetFeature, FeatureEmpty},
		{StartFeature, FeatureLoading}, {FailFeature, FeatureFailed}, {ResetFeature, FeatureEmpty},
	} {
		n, fx := Reduce(m, Event{Kind: tc.kind, Page: CheckIn})
		if n.CheckIn != tc.phase || n.Wish != m.Wish || len(fx) != 0 {
			t.Fatalf("check-in=%+v fx=%+v", n, fx)
		}
		m = n
	}
}
