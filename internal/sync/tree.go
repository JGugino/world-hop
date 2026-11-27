// tree.go: small helpers for comparing and copying local world trees.

package sync

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/JGugino/world-hopper/internal/manifest"
)

// newestMTime returns the most recent modification time (unix nanoseconds) among
// files. KeepNewest uses it to decide whether the local world is newer than the
// stored version.
func newestMTime(files []manifest.File) int64 {
	var m int64
	for _, f := range files {
		if f.MTime > m {
			m = f.MTime
		}
	}
	return m
}

// copyTree copies regular files and directories from src to dst, skipping
// session.lock. KeepBoth uses it to preserve the user's local world in a
// "(conflict copy ...)" folder before the cloud version replaces it. It never
// overwrites: dst files are created exclusively, so an existing file is an error.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() || d.Name() == manifest.LockFile {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		st, err := in.Stat()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, st.Mode().Perm()|0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}
