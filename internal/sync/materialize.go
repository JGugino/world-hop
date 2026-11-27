// materialize.go: the download path. Builds a stored version (a manifest) back into a
// folder of real files, verifying every byte on the way. It never touches the live
// world: Engine.apply points it at an empty staging folder and swaps the result in only
// if everything checked out.

package sync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	stdsync "sync"
	"time"

	"github.com/JGugino/world-hopper/internal/chunker"
	"github.com/JGugino/world-hopper/internal/manifest"
)

// placement says where one chunk goes: which file (index into the manifest's
// Files) and the byte offset inside it. A chunk that appears several times (same
// data in two places) has several placements.
type placement struct {
	file int   // index into the manifest's Files
	off  int64 // offset of the chunk inside that file
}

// materialize builds manifest m's world in dir (an empty directory).
//
// Every byte is verified against its chunk hash before it is written, and the
// finished files are checked against the manifest's sizes, so a world that
// does not match the manifest is never handed back as complete.
//
// Chunks this computer already has (localRoot's current files, described by
// local) are read from disk instead of downloaded, so pulling a small change
// downloads only the packs holding the new data. A local chunk whose hash does
// not match is ignored and fetched from the store instead. The remaining
// packs are downloaded concurrently, each exactly once.
func (e *Engine) materialize(ctx context.Context, m *manifest.Manifest, dir, localRoot string, local *scanResult) error {
	// 1. Lay out the files and work out where every chunk goes.
	places := map[string][]placement{}
	made := map[string]bool{}
	for i, f := range m.Files {
		target := filepath.Join(dir, filepath.FromSlash(f.Path))
		if d := filepath.Dir(target); !made[d] {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return err
			}
			made[d] = true
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(f.Mode&0o777)|0o600)
		if err != nil {
			return err
		}
		if f.Size > 0 {
			err = out.Truncate(f.Size)
		}
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		var off int64
		for _, h := range f.Chunks {
			places[h] = append(places[h], placement{i, off})
			off += m.Chunks[h].Length
		}
	}

	var (
		mu      stdsync.Mutex
		written = map[string]bool{} // chunk hashes fully placed
	)
	// put verifies data against hash and writes it at every place it goes.
	put := func(hash string, data []byte) error {
		if chunker.Hash(data) != hash {
			return fmt.Errorf("%w: chunk %s", ErrIntegrity, hash)
		}
		for _, p := range places[hash] {
			target := filepath.Join(dir, filepath.FromSlash(m.Files[p.file].Path))
			f, err := os.OpenFile(target, os.O_WRONLY, 0)
			if err != nil {
				return err
			}
			_, err = f.WriteAt(data, p.off)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		}
		mu.Lock()
		written[hash] = true
		mu.Unlock()
		return nil
	}

	// 2. Reuse what is already on this computer.
	if local != nil && localRoot != "" {
		type src struct {
			hash     string
			off, len int64
		}
		bySource := map[string][]src{}
		claimed := map[string]bool{}
		rels := make([]string, 0, len(local.Local))
		for rel := range local.Local {
			rels = append(rels, rel)
		}
		sort.Strings(rels)
		for _, rel := range rels {
			lf := local.Local[rel]
			var off int64
			for i, h := range lf.Chunks {
				if _, need := places[h]; need && !claimed[h] && lf.Lens[i] == m.Chunks[h].Length {
					claimed[h] = true
					bySource[rel] = append(bySource[rel], src{h, off, lf.Lens[i]})
				}
				off += lf.Lens[i]
			}
		}
		srcs := make([]string, 0, len(bySource))
		for rel := range bySource {
			srcs = append(srcs, rel)
		}
		sort.Strings(srcs)
		err := parallel(ctx, len(srcs), scanWorkers(), func(ctx context.Context, i int) error {
			f, err := os.Open(filepath.Join(localRoot, filepath.FromSlash(srcs[i])))
			if err != nil {
				return nil // fetched from the store below
			}
			defer f.Close()
			for _, c := range bySource[srcs[i]] {
				buf := make([]byte, c.len)
				if _, err := f.ReadAt(buf, c.off); err != nil || chunker.Hash(buf) != c.hash {
					continue // changed since it was scanned: fetched from the store below
				}
				if err := put(c.hash, buf); err != nil {
					return err
				}
			}
			return ctx.Err()
		})
		if err != nil {
			return err
		}
	}

	// 3. Download the packs that hold everything still missing, once each.
	needed := map[string][]string{} // pack -> chunk hashes
	for h := range places {
		if written[h] {
			continue
		}
		loc, ok := m.Chunks[h]
		if !ok {
			return fmt.Errorf("%w: chunk %s has no location", ErrIntegrity, h)
		}
		needed[loc.Pack] = append(needed[loc.Pack], h)
	}
	packs := make([]string, 0, len(needed))
	for p := range needed {
		packs = append(packs, p)
	}
	sort.Strings(packs)
	err := parallel(ctx, len(packs), transferWorkers, func(ctx context.Context, i int) error {
		name := packs[i]
		b, err := e.Store.GetPack(ctx, name)
		if err != nil {
			return err
		}
		if chunker.Hash(b) != name {
			return fmt.Errorf("%w: pack %s", ErrIntegrity, name)
		}
		for _, h := range needed[name] {
			loc := m.Chunks[h]
			if loc.Offset+loc.Length > int64(len(b)) {
				return fmt.Errorf("%w: chunk outside pack", ErrIntegrity)
			}
			if err := put(h, b[loc.Offset:loc.Offset+loc.Length]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	// 4. Every chunk placed, every file the size the manifest says.
	for h := range places {
		if !written[h] {
			return fmt.Errorf("%w: chunk %s was not written", ErrIntegrity, h)
		}
	}
	for _, f := range m.Files {
		target := filepath.Join(dir, filepath.FromSlash(f.Path))
		st, err := os.Stat(target)
		if err != nil {
			return err
		}
		if st.Size() != f.Size {
			return fmt.Errorf("%w: %s is %d bytes, want %d", ErrIntegrity, f.Path, st.Size(), f.Size)
		}
		if f.MTime > 0 {
			t := time.Unix(0, f.MTime)
			os.Chtimes(target, t, t)
		}
	}
	return nil
}

// cacheFromManifest describes the files of a freshly built world dir in stat
// cache form, so the first scan after a pull is a stat-only pass. Files whose
// modification time is too recent to trust (see racyWindow) are left out and will
// simply be hashed next time.
func cacheFromManifest(m *manifest.Manifest, dir string) map[string]localFile {
	out := make(map[string]localFile, len(m.Files))
	cutoff := time.Now().Add(-racyWindow)
	for _, f := range m.Files {
		st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f.Path)))
		if err != nil || st.Size() != f.Size || st.ModTime().After(cutoff) {
			continue
		}
		lf := localFile{Size: f.Size, MTime: st.ModTime().UnixNano(), Mode: uint32(st.Mode().Perm()), Chunks: f.Chunks}
		for _, h := range f.Chunks {
			lf.Lens = append(lf.Lens, m.Chunks[h].Length)
		}
		out[f.Path] = lf
	}
	return out
}
