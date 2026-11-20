// SPDX-License-Identifier: GPL-3.0-or-later

package brandingcache

import (
	"context"
	"fmt"
	"os"
)

// openRegular opens a cache file after proving it is a plain file and not a link,
// so a name inside the cache directory cannot address anything else.
func openRegular(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("brandingcache: cache file is not a regular file")
	}
	return os.Open(path)
}

// stageFile writes data beside its destination and returns the temporary name.
// Callers publish it through replaceFile, which makes the new bytes visible only
// when they are complete and flushed.
func stageFile(dir, prefix string, data []byte, ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, prefix+"*")
	if err != nil {
		return "", err
	}
	name := file.Name()
	fail := func(err error) (string, error) {
		file.Close()
		_ = os.Remove(name)
		return "", err
	}
	if _, err := file.Write(data); err != nil {
		return fail(err)
	}
	if err := file.Sync(); err != nil {
		return fail(err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}
