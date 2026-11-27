// swap.go: replacing a world folder on disk safely. A world is never half-replaced: the
// new version is built beside it and swapped in with renames, and a crash mid-swap is
// repaired by Recover.

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
	// stagingPrefix names the folder a new version is built in, next to the world
	// (".worldhopper-staging-<name>"). It exists only while a pull is running.
	stagingPrefix = ".worldhopper-staging-"
	// oldPrefix names the folder the replaced world is moved to during a swap
	// (".worldhopper-old-<name>"). It exists only for the instant of the swap, or until
	// the caller turns it into a snapshot.
	oldPrefix = ".worldhopper-old-"
)

// stagingPath is where a new version of the world at path is built before swapping.
func stagingPath(world string) string {
	return filepath.Join(filepath.Dir(world), stagingPrefix+filepath.Base(world))
}

// oldPath is where the replaced world at path is parked during a swap.
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
	if _, err := swapKeepOld(world, staging); err != nil {
		return err
	}
	return os.RemoveAll(oldPath(world))
}

// swapKeepOld is swapIn without the final delete: the replaced world is left
// at oldPath(world) for the caller to keep or remove. hadOld says whether
// there was one.
func swapKeepOld(world, staging string) (hadOld bool, err error) {
	old := oldPath(world)
	if err := os.RemoveAll(old); err != nil {
		return false, err
	}
	_, err = os.Stat(world)
	hadOld = err == nil
	if hadOld {
		if err := rename(world, old); err != nil {
			return false, fmt.Errorf("swap: move old world aside: %w", err)
		}
	}
	if err := rename(staging, world); err != nil {
		if hadOld {
			rename(old, world) // roll back
		}
		return false, fmt.Errorf("swap: move new world into place: %w", err)
	}
	return hadOld, nil
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

// IsInternalName reports whether a folder name is one of our temp dirs. World
// discovery uses it so a leftover staging folder is never mistaken for a world.
func IsInternalName(name string) bool {
	return strings.HasPrefix(name, stagingPrefix) || strings.HasPrefix(name, oldPrefix)
}
