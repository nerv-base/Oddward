// SPDX-License-Identifier: GPL-3.0-or-later

// Package accounts stores saved China-server login accounts so a user can log
// in again without another SMS code.
//
// Only the channel identity is kept: a random device id, the channel uid and
// the channel token. Phone numbers (other than a masked label), captchas and
// game tokens are never stored. Every launch still runs the full OneLogin and
// anti-addiction checks.
//
// # Layout
//
// One directory, owned by a single process at a time:
//
//	<id>.acct         one account; id is 32 lowercase hex chars (128 random bits)
//	subject-key.bin   per-store HMAC key for the identity keys, protector-wrapped
//	last-login.bin    the remembered account id, or empty for "none"
//	notice.bin        the acknowledged login notice revision
//	.pending-<hex>.tmp  a save in flight; never read as an account
//
// The directory is created with a protected private DACL: full control for the
// current user and SYSTEM only, no inherited access control entries. Every file
// is created and read through a handle that re-applies that DACL, refuses
// reparse points and refuses to follow a swapped path. Paths above the store
// that are reparse points are rejected with ErrUnsafePath, as is a store
// directory that has been replaced after Open.
//
// # Records
//
// A record file is JSON, at most MaxRecordSize bytes:
//
//	{"version":1,"protection":"DPAPI-CurrentUser-AES256GCM-v1","id":"<id>",
//	 "label":"138****0000","savedAt":"<RFC 3339 nano, UTC>",
//	 "identityKey":"<64 upper hex or empty>","secret":"<base64>"}
//
// secret is u16be(len(wrappedKey)) ‖ wrappedKey ‖ nonce(12) ‖ AES-256-GCM
// ciphertext+tag of the JSON-encoded Secret. Each record has its own random
// 32-byte data key, wrapped by a KeyProtector (DPAPI CurrentUser on Windows);
// the key is unwrapped and compared before the record is written. The
// additional authenticated data binds a context string, the protection kind,
// the version, the id, the label, savedAt, an empty expiry and the identity
// key, so editing any public field on disk makes Unlock fail with ErrUnlock.
//
// identityKey is HMAC-SHA256(subject-key, "oddward/cnlogin/accounts/identity|uid:"
// ‖ uid), uppercase hex. It lets a save recognise the same channel uid without
// decrypting every record. A record written before the subject key existed has
// no identity key and is decrypted to compare, so a lost key file cannot create
// a duplicate account.
//
// # Writes
//
// Writes go to a private temp file that is fsynced and then renamed over the
// target. A save is a transaction: Prepare encrypts and writes a durable
// candidate, the caller delivers the credential, then Commit publishes it;
// Rollback (or a delivery failure) discards the candidate, so a new account
// never survives a refused admission. Commit drops the records a refresh
// supersedes. Records that fail validation are skipped by List and counted as
// corrupt, and one scan visits at most 64 candidate files.
//
// # Protected state
//
// subject-key.bin, last-login.bin and notice.bin share one envelope: JSON of
// {"version":1,"purpose":"<subject-key|last-login|login-notice>",...} wrapped by
// the KeyProtector. They are unreadable without the current user's key, so
// another Windows user (or a copied directory) sees no account id and no notice
// acknowledgement. An unreadable state file is treated as "explicitly none", not
// as "never set": Last does not fall back to a sole account in that case.
package accounts
