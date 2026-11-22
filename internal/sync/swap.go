package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	stagingPrefix = ".worldhopper-staging-"
	oldPrefix     = ".worldhopper-old-"
)

func stagingPath(world string) string {
	return filepath.Join(filepath.Dir(world), stagingPrefix+filepath.Base(world))
}
func oldPath(world string) string {
	return filepath.Join(filepath.Dir(world), oldPrefix+filepath.Base(world))
}

// rename is os.Rename, retried briefly on Windows where antivirus scanners,
// the search indexer or Explorer often hold a handle inside a folder for a
// moment and make renaming it fail with a sharing violation.
func rename(from, to string) error {
	err := os.Rename(from, to)
	for i := 1; err != nil && runtime.GOOS == "windows" && i <= 5; i++ {
		time.Sleep(time.Duration(i) * 150 * time.Millisecond)
		err = os.Rename(from, to)
	}
	return err
}

// swapIn atomically replaces world with the fully built staging directory.
// At every instant either the old or the new world is present at world; a
// crash in the middle is repaired by Recover.
func swapIn(world, staging string) error {
	old := oldPath(world)
	if err := os.RemoveAll(old); err != nil {
		return err
	}
	_, err := os.Stat(world)
	hadOld := err == nil
	if hadOld {
		if err := rename(world, old); err != nil {
			return fmt.Errorf("swap: move old world aside: %w", err)
		}
	}
	if err := rename(staging, world); err != nil {
		if hadOld {
			rename(old, world) // roll back
		}
		return fmt.Errorf("swap: move new world into place: %w", err)
	}
	return os.RemoveAll(old)
}

// Recover repairs an interrupted swap for the world at path: if the world is
// missing but the moved-aside copy exists, it is put back. Leftover staging
// directories are removed. Call it at startup before syncing.
func Recover(world string) error {
	old, stg := oldPath(world), stagingPath(world)
	if _, err := os.Stat(world); os.IsNotExist(err) {
		if _, err := os.Stat(old); err == nil {
			if err := rename(old, world); err != nil {
				return err
			}
		}
	}
	if err := os.RemoveAll(stg); err != nil {
		return err
	}
	if _, err := os.Stat(world); err == nil {
		return os.RemoveAll(old)
	}
	return nil
}

// IsInternalName reports whether a folder name is one of our temp dirs.
func IsInternalName(name string) bool {
	return strings.HasPrefix(name, stagingPrefix) || strings.HasPrefix(name, oldPrefix)
}
