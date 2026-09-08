package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Xm798/placard/internal/config"
)

// localClient stores objects as files under a single directory, for
// self-hosters who want no cloud dependency at all.
//
// The declared Content-Type is dropped: a plain filesystem has nowhere to keep
// it, and nothing reads it back — the render handler sets text/html itself and
// the avatar handler sniffs the bytes.
type localClient struct {
	dir string
}

// fallbackDir catches a LocalStorageConfig assembled in code rather than by
// config.Load, which always derives storage.local.dir from data_dir.
const fallbackDir = config.DefaultDataDir + "/objects"

// tempPrefix names the files PutObject stages writes through. SweepTemp
// reclaims them by this prefix, so the two MUST agree; validateKey cannot
// produce a key that starts with it, which is what keeps a real object out of
// the sweep's reach.
const tempPrefix = ".tmp-"

// NewLocalClient builds the local backend rooted at cfg.Dir, creating the
// directory if it does not exist. The path is resolved to an absolute one at
// construction, so a later working directory change cannot move the store.
func NewLocalClient(cfg config.LocalStorageConfig) (Client, error) {
	dir := cfg.Dir
	if dir == "" {
		dir = fallbackDir
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("storage local: resolve dir %q: %w", dir, err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("storage local: create dir %q: %w", abs, err)
	}
	return &localClient{dir: abs}, nil
}

func (l *localClient) path(key string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}
	return filepath.Join(l.dir, filepath.FromSlash(key)), nil
}

// PutObject writes the body to a temporary file in the destination directory
// and renames it into place, so a reader never observes a partially written
// object and a failed write leaves the previous one intact.
func (l *localClient) PutObject(_ context.Context, key string, data io.Reader, _ string) error {
	path, err := l.path(key)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("storage local put object %q: %w", key, err)
	}

	tmp, err := os.CreateTemp(dir, tempPrefix+"*")
	if err != nil {
		return fmt.Errorf("storage local put object %q: %w", key, err)
	}
	tmpName := tmp.Name()
	defer func() {
		// No-op once the rename below has succeeded.
		_ = os.Remove(tmpName)
	}()

	if data != nil {
		if _, err := io.Copy(tmp, data); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("storage local put object %q: %w", key, err)
		}
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("storage local put object %q: %w", key, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("storage local put object %q: %w", key, err)
	}
	return nil
}

// GetObject opens the file for streaming; the caller closes it. A missing file
// — or a key that names a directory, which is how a prefix of a real key looks
// on disk — is reported as ErrNotFound.
func (l *localClient) GetObject(_ context.Context, key string) (io.ReadCloser, error) {
	path, err := l.path(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("storage local get object %q: %w", key, ErrNotFound)
		}
		return nil, fmt.Errorf("storage local get object %q: %w", key, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("storage local get object %q: %w", key, err)
	}
	if info.IsDir() {
		_ = f.Close()
		return nil, fmt.Errorf("storage local get object %q: %w", key, ErrNotFound)
	}
	return f, nil
}

// DeleteObject removes the file. Idempotent: an already-gone object is success
// so cron reclaim never wedges on a missing key.
func (l *localClient) DeleteObject(_ context.Context, key string) error {
	path, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("storage local delete object %q: %w", key, err)
	}
	return nil
}

// SweepTemp reclaims staging files a killed process left behind. PutObject
// removes its own on every path it can still run, so anything this finds is the
// residue of a SIGKILL, an OOM kill or a power loss — unreferenced by any row,
// therefore invisible to the pending-delete queue that reclaims everything else.
//
// A walk is the only way to find them: they are not in the database by
// construction. The cost is one stat per object per round, which is why the
// cron runs it on a slow cadence rather than every round.
//
// Errors on individual entries are collected rather than returned, so one
// unreadable directory cannot stop the sweep from reclaiming the rest.
func (l *localClient) SweepTemp(ctx context.Context, olderThan time.Time) (int, error) {
	var (
		swept int
		errs  []error
	)
	err := filepath.WalkDir(l.dir, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			errs = append(errs, err)
			// A directory that cannot be read is skipped, not fatal.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !strings.HasPrefix(d.Name(), tempPrefix) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			errs = append(errs, err)
			return nil
		}
		if !info.ModTime().Before(olderThan) {
			return nil
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
			return nil
		}
		swept++
		return nil
	})
	if err != nil {
		errs = append(errs, err)
	}
	return swept, errors.Join(errs...)
}
