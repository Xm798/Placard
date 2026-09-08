//go:build windows

package updater

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeFileAtomic replaces path on Windows, where a file with an open handle
// (such as the running placard.exe) cannot be overwritten. NTFS does allow
// RENAMING such a file, so the existing binary is moved aside to <path>.old
// first and the new one is renamed into the freed name.
//
// Known, accepted side effect: after an update the directory keeps a
// placard.exe.old until the NEXT update removes it. The running process
// cannot delete its own image, so do not try to clean it up in this run.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	// Same directory as the target, never os.TempDir(): os.Rename cannot cross
	// volumes on Windows either.
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("setting permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}

	if _, err := os.Stat(path); err == nil {
		oldPath := path + ".old"
		// Left over from a previous update; removable now that nothing holds it.
		_ = os.Remove(oldPath)
		if err := os.Rename(path, oldPath); err != nil {
			return fmt.Errorf("moving %s aside to %s: %w", path, oldPath, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspecting %s: %w", path, err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("renaming temp file into place: %w", err)
	}

	tmpPath = ""
	return nil
}
