package sync

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/JGugino/world-hopper/internal/chunker"
	"github.com/JGugino/world-hopper/internal/manifest"
)

// scanTree walks dir, chunks every regular file (never session.lock) and
// returns the file list. onChunk, if non-nil, receives every chunk's hash and
// data (data is only valid during the call).
func scanTree(dir string, onChunk func(hash string, data []byte) error) ([]manifest.File, error) {
	var files []manifest.File
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil // symlinks, devices etc. are not synced
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !manifest.SafePath(rel) {
			return nil // session.lock and anything odd
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return err
		}
		entry := manifest.File{Path: rel, Mode: uint32(st.Mode().Perm()), MTime: st.ModTime().UnixNano()}
		err = chunker.Split(f, func(data []byte) error {
			h := chunker.Hash(data)
			entry.Chunks = append(entry.Chunks, h)
			entry.Size += int64(len(data))
			if onChunk != nil {
				return onChunk(h, data)
			}
			return nil
		})
		if err != nil {
			return err
		}
		files = append(files, entry)
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, err
}

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
// session.lock.
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
