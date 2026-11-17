// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package accounts

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var dpapiEntropy = []byte("Oddward.CNLogin.Accounts.DataKey.v1")

const dpapiDescription = "Oddward saved account key"

// DPAPI wraps data keys with CryptProtectData in the CurrentUser scope (never
// CRYPTPROTECT_LOCAL_MACHINE), with fixed application entropy and
// CRYPTPROTECT_UI_FORBIDDEN.
type DPAPI struct{}

// Protect wraps plain for the current Windows user.
func (DPAPI) Protect(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, errors.New("accounts: dpapi: empty input")
	}
	desc, err := windows.UTF16PtrFromString(dpapiDescription)
	if err != nil {
		return nil, err
	}
	in, entropy := dataBlob(plain), dataBlob(dpapiEntropy)
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, desc, &entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, fmt.Errorf("accounts: CryptProtectData: %w", err)
	}
	return takeBlob(&out, false), nil
}

// Unprotect unwraps a blob made by Protect for the current Windows user.
func (DPAPI) Unprotect(wrapped []byte) ([]byte, error) {
	if len(wrapped) == 0 {
		return nil, errors.New("accounts: dpapi: empty input")
	}
	in, entropy := dataBlob(wrapped), dataBlob(dpapiEntropy)
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, &entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, fmt.Errorf("accounts: CryptUnprotectData: %w", err)
	}
	return takeBlob(&out, true), nil
}

func dataBlob(b []byte) windows.DataBlob {
	return windows.DataBlob{Size: uint32(len(b)), Data: unsafe.SliceData(b)}
}

// takeBlob copies a DPAPI output buffer into Go memory and frees it, zeroing
// it first when it holds plaintext.
func takeBlob(b *windows.DataBlob, wipe bool) []byte {
	if b.Data == nil {
		return nil
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(b.Data)))
	src := unsafe.Slice(b.Data, b.Size)
	out := make([]byte, len(src))
	copy(out, src)
	if wipe {
		clear(src)
	}
	return out
}
