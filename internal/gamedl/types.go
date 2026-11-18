// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl

import (
	"context"
	"net/http"
	"time"

	"github.com/nerv-base/Oddward/internal/gameres"
)

// ResolveURLs is a trust boundary. Return only configured origins, in mirror
// preference order. The engine accepts HTTPS and never persists these URLs.
type ResolveURLs func(context.Context, gameres.Source) ([]string, error)

type Parameters struct {
	ChunkSize                         int64
	ConnectionsPerFile, FilesInFlight int
	RateLimitKiB                      int64
}

type Options struct {
	WorkRoot, StateRoot string
	Client              *http.Client
	Resolve             ResolveURLs
	Parameters          Parameters
	UserAgent           string
	// Lease lets an orchestrator hold one installation lock across download,
	// rebuild and commit. Close the engine before releasing the caller's lease.
	Lease *Lease
}

type Transfer struct {
	Object    gameres.Object `json:"object"`
	Source    gameres.Source `json:"source"`
	ChunkSize int64          `json:"chunkSize"`
	BytesDone int64          `json:"bytesDone"`
	State     TransferState  `json:"state"`
	// Blocks are trusted manifest hashes for resources. Patch block semantics
	// require explicit caller admission; otherwise only the whole blob is trusted.
	Blocks             []gameres.Block `json:"blocks,omitempty"`
	PatchBlocksTrusted bool            `json:"patchBlocksTrusted,omitempty"`
	Marks              []ObjectMark    `json:"marks,omitempty"`
}

type ObjectMark struct {
	Host         string    `json:"host"`
	URLKey       string    `json:"urlKey"` // SHA-256 of URL; avoids persisting user data.
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"lastModified,omitempty"`
	ObservedAt   time.Time `json:"observedAt"`
}

type TransferState uint8

const (
	TransferPending TransferState = iota + 1
	TransferDownloading
	TransferVerified
	TransferFailed
)

type TaskState uint8

const (
	StatePlanning TaskState = iota + 1
	StateRunning
	StatePaused
	StateVerifying
	StateStaged
	StateDone
	StateFailed
	StateStale
)

type TaskKind uint8

const (
	TaskInstallFiles TaskKind = iota + 1
	TaskUpdateFiles
	TaskPreloadFiles
	TaskVerifyOnly
)

type RebuildState uint8

const (
	RebuildPending RebuildState = iota + 1
	RebuildRunning
	RebuildProduced
	RebuildVerified
	RebuildRejected
	RebuildFailed
)

type TargetState uint8

const (
	TargetPending TargetState = iota + 1
	TargetStaged
	TargetPlaced
	TargetSkipped
	TargetFailed
)

type PatchStep struct {
	Blob, Old, New gameres.Object
	Source         gameres.Source
}
type Rebuild struct {
	Target gameres.Object
	Steps  []PatchStep
	Output string
	State  RebuildState
}
type InstallTarget struct {
	Path   string
	Object gameres.Object
	Action gameres.ActionKind
	State  TargetState
}

type Task struct {
	ID            string          `json:"taskID"`
	InstallKey    string          `json:"installKey"`
	Purpose       string          `json:"purpose"`
	CacheRoot     string          `json:"cacheRoot,omitempty"` // Explicit fresh-install cache selection.
	Branch        string          `json:"branch"`
	TargetVersion string          `json:"targetVersion"`
	ListHash      string          `json:"listHash"`
	Kind          TaskKind        `json:"kind"`
	Transfers     []Transfer      `json:"objects"`
	Rebuilds      []Rebuild       `json:"rebuilds,omitempty"`
	Targets       []InstallTarget `json:"targets,omitempty"`
	State         TaskState       `json:"phase"`
	Err           string          `json:"error,omitempty"`
	Counters      Counters        `json:"counters"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}
type Counters struct{ NetworkBytes, WrittenBytes, ReusedBytes, CachedBytes int64 }

type Progress struct {
	TaskID, Stage                                                           string
	Downloaded, Total, NetworkBytes, WrittenBytes, ReusedBytes, CachedBytes int64
	BytesPerSecond                                                          float64
	ETA                                                                     time.Duration
	ETAKnown                                                                bool
	// NetworkActiveDuration is cumulative wall time with at least one active
	// HTTP request. Concurrent requests are counted once; queue and final
	// verification time are excluded. Rate waits within active requests count.
	NetworkActiveDuration time.Duration
}
