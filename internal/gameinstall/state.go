// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"errors"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/gamelocate"
)

const (
	Detected = "detected"
	Adopted  = "adopted"
	Verified = "verified"
)

var (
	ErrInvalidState = errors.New("invalid installation state")
	ErrStaleState   = errors.New("stale installation state")
)

type InstallTarget struct {
	Path string `json:"path"`
	MD5  string `json:"md5"`
	Size int64  `json:"size"`
}
type InstallRecord struct {
	Schema           int             `json:"schema"`
	InstallKey       string          `json:"installKey"`
	Root             string          `json:"root"`
	Mode             string          `json:"mode"`
	State            string          `json:"state"`
	Branch           string          `json:"branch"`
	Version          string          `json:"version"`
	ListHash         string          `json:"listHash"`
	ManifestDigest   string          `json:"manifestDigest"`
	TxnID            string          `json:"txnID"`
	BaseVersion      string          `json:"baseVersion"`
	VerifyCoverage   string          `json:"verifyCoverage"`
	VerifiedTargets  int             `json:"verifiedTargets"`
	PublishedAt      time.Time       `json:"publishedAt"`
	CreatedAt        time.Time       `json:"createdAt"`
	Targets          []InstallTarget `json:"targets,omitempty"`
	RootFileID       string          `json:"rootFileID,omitempty"`
	OfficialDigest   string          `json:"officialDigest,omitempty"`
	BaselineMismatch bool            `json:"baselineMismatch,omitempty"`
}

// Store owns one installation's record. Records live in the database; stateRoot
// keeps the transaction journals, the verification cache and the legacy mirror a
// rolled-back build reads.
type Store struct {
	db        *database.DB
	stateRoot string
}
type AdoptMetadata struct {
	ConfigXML, Manifest []byte
	Branch              string
}
type Mismatch struct{ Path, Reason string }
type AdoptResult struct {
	Installation         gamelocate.Installation
	Record               InstallRecord
	State                string
	Files                int
	Mismatches, TagDrift []Mismatch
	Warnings             []string
}
type VerifyReport struct {
	Path                  string
	FilesDone, FilesTotal int
	BytesDone, BytesTotal int64
	HashedBytes           int64
	Reused                bool
}
type VerifyResult struct {
	Record                  InstallRecord
	Files, Verified, Reused int
	Mismatches              []Mismatch
}
