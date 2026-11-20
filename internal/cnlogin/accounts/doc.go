// SPDX-License-Identifier: GPL-3.0-or-later

// Package accounts stores saved China-server login accounts so a user can log
// in again without another SMS code.
//
// Only the channel identity is kept: a random device id, the channel uid and
// the channel token. Phone numbers (other than a masked label), captchas and
// game tokens are never stored. Every launch still runs the full OneLogin and
// anti-addiction checks.
//
// # Storage
//
// The accounts live in the launcher database (internal/database): the accounts
// table holds one row per saved login with the sealed record in `record`, the
// account_tajiduo table holds the encrypted Tajiduo session of the accounts that
// linked one, and the single account_state row holds the device state. The
// database is authoritative; nothing outside it is read as one.
//
// # Mirrors
//
// The stub can roll the launcher back to a build that does not know the
// database, so every change also rewrites the legacy files that build reads, in
// one directory owned by a single process at a time:
//
//	<id>.acct         one account; id is 32 lowercase hex chars (128 random bits)
//	<id>.tjd          the Tajiduo session of that account
//	subject-key.bin   per-store HMAC key for the identity keys, protector-wrapped
//	last-login.bin    the remembered account id, or empty for "none"
//	notice.bin        the acknowledged login notice revision
//	mirrors.json      digests of the files above, so a change made by another
//	                  build is noticed
//	.pending-<hex>.tmp  a save in flight; never read as an account
//
// The digest of mirrors.json is recorded in compat_mirrors, and ImportLegacy
// re-imports the files when they no longer match it. The directory is created
// with a protected private DACL: full control for the current user and SYSTEM
// only, no inherited access control entries. Every mirror is created and read
// through a handle that re-applies that DACL, refuses reparse points and refuses
// to follow a swapped path. Paths above the directory that are reparse points are
// rejected with ErrUnsafePath, as is a directory that has been replaced after
// Open.
//
// # Records
//
// A record is JSON, at most MaxRecordSize bytes:
//
//	{"version":1,"protection":"DPAPI-CurrentUser-AES256GCM-v1","id":"<id>",
//	 "label":"138****0000","savedAt":"<RFC 3339 nano, UTC>",
//	 "identityKey":"<64 upper hex or empty>","secret":"<base64>"}
//
// secret is u16be(len(wrappedKey)) ‖ wrappedKey ‖ nonce(12) ‖ AES-256-GCM
// ciphertext+tag of the JSON-encoded Secret. Each record has its own random
// 32-byte data key, wrapped by a KeyProtector (DPAPI CurrentUser on Windows);
// the key is unwrapped and compared before the record is stored. The additional
// authenticated data binds a context string, the protection kind, the version,
// the id, the label, savedAt, an empty expiry and the identity key, so editing
// any public field makes Unlock fail with ErrUnlock. The stored bytes are the
// canonical bytes the file store wrote: the ciphertext is only verifiable if it
// is byte-identical, so it is never re-encoded.
//
// identityKey is HMAC-SHA256(subject-key, "oddward/cnlogin/accounts/identity|uid:"
// ‖ uid), uppercase hex. It is the accounts.identity_key column, whose unique
// index is what makes a save replace the account with the same channel uid
// without decrypting anything. A record written before the subject key existed
// has no identity key and is decrypted to compare, so a lost key file cannot
// create a duplicate account.
//
// # Writes
//
// One transaction per operation: the rows and the digest of the mirrored bytes
// are written together, and the mirror files follow the commit. A save is
// staged, not written: Prepare seals a durable candidate, the caller delivers the
// credential, then Commit publishes it; Rollback (or a delivery failure) discards
// the candidate, so a new account never survives a refused admission. The subject
// key a staged save needs is held in memory until that commit, so a discarded
// save leaves no state behind either. Commit deletes the accounts a refresh
// supersedes. Records that fail validation are skipped by List and counted as
// corrupt.
//
// # Protected state
//
// subject-key.bin, last-login.bin and notice.bin share one envelope: JSON of
// {"version":1,"purpose":"<subject-key|last-login|login-notice>",...} wrapped by
// the KeyProtector, stored in account_state exactly as the files hold it. They
// are unreadable without the current user's key, so another Windows user sees no
// account id and no notice acknowledgement. "Never chosen" and "explicitly none"
// stay distinct: account_state.last_account is NULL for the first and a marker
// value for the second, so Last only falls back to a sole account when no choice
// was ever recorded.
package accounts
