package sync

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	stdsync "sync"
	"time"

	"github.com/JGugino/world-hopper/internal/chunker"
	"github.com/JGugino/world-hopper/internal/manifest"
)

// racyWindow: a file modified this recently before a scan started is not
// trusted by the stat cache next time. Coarse file systems (FAT: 2 s) and a
// write landing in the same tick as the scan could otherwise leave a changed
// file with an unchanged (size, mtime) pair.
const racyWindow = 3 * time.Second

// scanWorkers bounds parallel hashing. Hashing is CPU bound, and on spinning
// disks more than a few readers only adds seeks.
func scanWorkers() int { return min(max(runtime.NumCPU(), 2), 8) }

// localFile is what a scan knows about one file: its chunks, in order, and
// each chunk's length (so a chunk can be read back by offset).
type localFile struct {
	Size   int64    `json:"s"`
	MTime  int64    `json:"t"`
	Mode   uint32   `json:"m"`
	Chunks []string `json:"c,omitempty"`
	Lens   []int64  `json:"l,omitempty"`
}

// statCache remembers the chunk list of every file as of the last scan, keyed
// by relative path and valid while the file's size and modification time are
// unchanged. It turns "hash the whole world" into "stat every file" for the
// common case where little changed.
//
// It is only an accelerator: a missing or unreadable cache file just means a
// full scan, and nothing read from it is ever written to the store or used to
// build a world without the data being hashed again first.
type statCache struct {
	path    string
	mu      stdsync.Mutex
	entries map[string]localFile
	dirty   bool
}

const statCacheVersion = 1

type statCacheFile struct {
	Version int                  `json:"version"`
	Files   map[string]localFile `json:"files"`
}

// loadStatCache reads the cache at path ("" gives a cache that is never saved).
func loadStatCache(path string) *statCache {
	c := &statCache{path: path, entries: map[string]localFile{}}
	if path == "" {
		return c
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	var f statCacheFile
	if json.Unmarshal(b, &f) == nil && f.Version == statCacheVersion && f.Files != nil {
		c.entries = f.Files
	}
	return c
}

func (c *statCache) get(rel string) (localFile, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.entries[rel]
	return f, ok
}

func (c *statCache) put(rel string, f localFile) {
	c.mu.Lock()
	c.entries[rel] = f
	c.dirty = true
	c.mu.Unlock()
}

// retain drops entries for files that no longer exist.
func (c *statCache) retain(present map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		if !present[k] {
			delete(c.entries, k)
			c.dirty = true
		}
	}
}

// save writes the cache if it changed. Errors are ignored on purpose.
func (c *statCache) save() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path == "" || !c.dirty {
		return
	}
	b, err := json.Marshal(statCacheFile{Version: statCacheVersion, Files: c.entries})
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(c.path), 0o755) != nil {
		return
	}
	tmp := c.path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) != nil {
		return
	}
	if os.Rename(tmp, c.path) != nil {
		os.Remove(tmp)
		return
	}
	c.dirty = false
}

// scanResult is a scanned world: the manifest file list plus, per file, the
// chunk lengths (so chunks can be located inside local files).
type scanResult struct {
	Files []manifest.File
	Local map[string]localFile // by relative path
}

type candidate struct {
	rel, abs string
	size     int64
	mtime    int64
	mode     uint32
}

// listFiles walks dir for syncable regular files (never session.lock).
func listFiles(dir string) ([]candidate, error) {
	var out []candidate
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
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, candidate{rel, p, info.Size(), info.ModTime().UnixNano(), uint32(info.Mode().Perm())})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, err
}

// hashFile chunks and hashes one file, without keeping any data. It reports
// whether the file looked stable (same size and mtime before and after).
func hashFile(c candidate) (localFile, bool, error) {
	f, err := os.Open(c.abs)
	if err != nil {
		return localFile{}, false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return localFile{}, false, err
	}
	lf := localFile{MTime: st.ModTime().UnixNano(), Mode: uint32(st.Mode().Perm())}
	err = chunker.Split(f, func(data []byte) error {
		lf.Chunks = append(lf.Chunks, chunker.Hash(data))
		lf.Lens = append(lf.Lens, int64(len(data)))
		lf.Size += int64(len(data))
		return nil
	})
	if err != nil {
		return localFile{}, false, err
	}
	st2, err := f.Stat()
	stable := err == nil && st2.ModTime().UnixNano() == lf.MTime && st2.Size() == lf.Size
	return lf, stable, nil
}

// scan lists dir and returns every file's chunk list. Files whose size and
// mtime match the cache are not read at all; the rest are hashed by a bounded
// pool of workers. cache may be nil.
func scan(dir string, cache *statCache) (*scanResult, error) {
	cands, err := listFiles(dir)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	res := &scanResult{Files: make([]manifest.File, len(cands)), Local: make(map[string]localFile, len(cands))}
	var (
		mu       stdsync.Mutex
		firstErr error
		wg       stdsync.WaitGroup
		jobs     = make(chan int)
	)
	do := func(i int) error {
		c := cands[i]
		lf, ok := localFile{}, false
		if cache != nil {
			if hit, found := cache.get(c.rel); found && hit.Size == c.size && hit.MTime == c.mtime &&
				len(hit.Chunks) == len(hit.Lens) {
				lf, ok = hit, true
			}
		}
		if !ok {
			var stable bool
			var err error
			if lf, stable, err = hashFile(c); err != nil {
				return err
			}
			if cache != nil && stable && started.Sub(time.Unix(0, lf.MTime)) > racyWindow {
				cache.put(c.rel, lf)
			}
		}
		mu.Lock()
		res.Local[c.rel] = lf
		mu.Unlock()
		res.Files[i] = manifest.File{Path: c.rel, Size: lf.Size, Mode: lf.Mode, MTime: lf.MTime, Chunks: lf.Chunks}
		return nil
	}
	workers := min(scanWorkers(), max(len(cands), 1))
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if err := do(i); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
				}
			}
		}()
	}
	for i := range cands {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if cache != nil {
		present := make(map[string]bool, len(cands))
		for _, c := range cands {
			present[c.rel] = true
		}
		cache.retain(present)
		cache.save()
	}
	return res, nil
}

// feedChunks reads file rel again and hands every chunk that need approves
// to onChunk, returning the file's fresh chunk list. It is the upload side of
// a scan: only files holding chunks the store lacks are read a second time.
func feedChunks(path string, onChunk func(hash string, data []byte) error) (localFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return localFile{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return localFile{}, err
	}
	lf := localFile{MTime: st.ModTime().UnixNano(), Mode: uint32(st.Mode().Perm())}
	err = chunker.Split(f, func(data []byte) error {
		h := chunker.Hash(data)
		lf.Chunks = append(lf.Chunks, h)
		lf.Lens = append(lf.Lens, int64(len(data)))
		lf.Size += int64(len(data))
		return onChunk(h, data)
	})
	return lf, err
}
