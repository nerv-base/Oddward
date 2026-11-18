// SPDX-License-Identifier: GPL-3.0-or-later
package gamepatch

import (
	"context"
	"io"
	"os"
)

// IOStats records concrete file bytes read and written during rebuild, including
// whole-file identity verification and buffered patch reads. It excludes metadata
// operations, filesystem allocation, and publication links.
type IOStats struct {
	ReadBytes, WrittenBytes int64
	SourceVerifiedBytes     int64 // Whole source reads for successfully verified chain inputs.
}

// ApplyChainMeasured has the ApplyChain ownership contract and additionally
// returns actual I/O performed, including bytes transferred before any failure.
func ApplyChainMeasured(ctx context.Context, steps []Step, workdir string, report func(step int, written, total int64)) (output string, stats IOStats, result error) {
	output, result = applyChain(ctx, steps, workdir, report, &stats)
	return output, stats, result
}

type readableFile interface {
	io.ReaderAt
	Stat() (os.FileInfo, error)
}

type measuredFile struct {
	*os.File
	stats *IOStats
}

func (f measuredFile) ReadAt(p []byte, offset int64) (int, error) {
	n, err := f.File.ReadAt(p, offset)
	if f.stats != nil {
		f.stats.ReadBytes += int64(n)
	}
	return n, err
}
func (f measuredFile) Write(p []byte) (int, error) {
	n, err := f.File.Write(p)
	if f.stats != nil {
		f.stats.WrittenBytes += int64(n)
	}
	return n, err
}
