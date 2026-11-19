// SPDX-License-Identifier: GPL-3.0-or-later

package state

// MaxHomeAccounts bounds the compact account row on the home screen. It is the
// same limit the login window uses, so the two views never disagree.
const MaxHomeAccounts = 16

// SavedAccount is the public face of one remembered account: never a phone
// number, only the label the account store already masks.
type SavedAccount struct {
	ID    string
	Label string
}

// Home account events keep the compact account row independent from the game
// login window: the launcher only lists, and chooses which saved account the
// next login should propose.
const (
	HomeAccountsLoad EventKind = 400 + iota
	HomeAccountsLoaded
	HomeAccountsToggle
	HomeAccountsClose
	HomeAccountUse
)

type HomeAccountsState struct {
	Accounts [MaxHomeAccounts]SavedAccount
	Count    uint8
	Current  string
	Open     bool
	Error    string
}

func reduceHomeAccounts(m Model, e Event) (Model, []Effect, bool) {
	switch e.Kind {
	case HomeAccountsLoad:
		return m, []Effect{{Kind: LoadHomeAccounts}}, true
	case HomeAccountsLoaded:
		n := len(e.Accounts)
		if n > MaxHomeAccounts {
			n = MaxHomeAccounts
		}
		m.HomeAccounts.Count, m.HomeAccounts.Current, m.HomeAccounts.Error = uint8(n), e.Path, e.Message
		for i := 0; i < n; i++ {
			m.HomeAccounts.Accounts[i] = e.Accounts[i]
		}
		for i := n; i < MaxHomeAccounts; i++ {
			m.HomeAccounts.Accounts[i] = SavedAccount{}
		}
		return m, nil, true
	case HomeAccountsToggle:
		if m.Page == Home && m.Overlay == NoOverlay {
			m.HomeAccounts.Open = !m.HomeAccounts.Open
		}
		return m, nil, true
	case HomeAccountsClose:
		m.HomeAccounts.Open = false
		return m, nil, true
	case HomeAccountUse:
		if int(e.Index) >= int(m.HomeAccounts.Count) {
			return m, nil, true
		}
		account := m.HomeAccounts.Accounts[e.Index]
		m.HomeAccounts.Current, m.HomeAccounts.Open = account.ID, false
		return m, []Effect{{Kind: RememberHomeAccount, Path: account.ID}}, true
	}
	return m, nil, false
}
