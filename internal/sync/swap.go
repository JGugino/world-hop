package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
		if err := os.Rename(world, old); err != nil {
			return fmt.Errorf("swap: move old world aside: %w", err)
		}
	}
	if err := os.Rename(staging, world); err != nil {
		if hadOld {
			os.Rename(old, world) // roll back
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
			if err := os.Rename(old, world); err != nil {
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
