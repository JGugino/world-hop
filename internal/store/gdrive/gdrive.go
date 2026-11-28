// Package gdrive is a store.Store on Google Drive's hidden per-app folder
// (the drive.appdata scope). Drive has no real paths, so objects are flat files
// named "packs/<hash>" and "worlds/<id>/<manifest>", with world ids and kinds
// in appProperties for listing.
package gdrive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"sort"
	"strings"

	"github.com/JGugino/world-hopper/internal/httpx"
	"github.com/JGugino/world-hopper/internal/manifest"
	"github.com/JGugino/world-hopper/internal/store"
)

// Scope is the only OAuth scope the app requests.
const Scope = "https://www.googleapis.com/auth/drive.appdata"

const (
	DefaultAPIBase    = "https://www.googleapis.com"
	multipartMaxBytes = 5 << 20 // larger uploads use the resumable protocol
)

// Store keeps worlds in the hidden app-data folder of the user's Google Drive. It
// uses an authenticated HTTP client (httpx.Authed) that refreshes the OAuth token.
type Store struct {
	c *httpx.Authed
	// APIBase overrides the Google API host (tests).
	APIBase string
}

var _ store.Store = (*Store)(nil)

// New returns a Drive store that signs requests with auth. hc is the HTTP client
// (nil means the default).
func New(auth httpx.Auth, hc *http.Client) *Store {
	return &Store{c: &httpx.Authed{HTTP: hc, Auth: auth}, APIBase: DefaultAPIBase}
}

// file is the part of a Drive file resource this package needs: its id, its flat
// name ("packs/<hash>" or "worlds/<id>/<manifest>") and its size in bytes.
type file struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size string `json:"size"`
}

// esc escapes a string for use inside a single-quoted Drive query value.
func esc(s string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s)
}

// packFile is the Drive file name of a pack.
func packFile(name string) string { return "packs/" + name }

// manifestFile is the Drive file name of a manifest. Drive has no folders in the
// app-data space we use, so the "path" is just part of the name.
func manifestFile(w, name string) string { return "worlds/" + w + "/" + name }

// list runs a files.list query in the appDataFolder, following pages.
func (s *Store) list(ctx context.Context, q string) ([]file, error) {
	var out []file
	token := ""
	for {
		v := url.Values{}
		v.Set("spaces", "appDataFolder")
		v.Set("q", q+" and trashed=false")
		v.Set("fields", "nextPageToken,files(id,name,size)")
		v.Set("pageSize", "1000")
		if token != "" {
			v.Set("pageToken", token)
		}
		resp, err := s.c.Do(ctx, func() (*http.Request, error) {
			return http.NewRequest("GET", s.APIBase+"/drive/v3/files?"+v.Encode(), nil)
		})
		if err != nil {
			return nil, err
		}
		if err := httpx.Check(resp); err != nil {
			return nil, err
		}
		var page struct {
			NextPageToken string `json:"nextPageToken"`
			Files         []file `json:"files"`
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, page.Files...)
		if page.NextPageToken == "" {
			return out, nil
		}
		token = page.NextPageToken
	}
}

// byName finds files by exact name. Drive allows duplicates, so it returns all of them.
func (s *Store) byName(ctx context.Context, name string) ([]file, error) {
	return s.list(ctx, "name='"+esc(name)+"'")
}

// download fetches a file's content (store.ErrNotFound if it is gone).
func (s *Store) download(ctx context.Context, f file) ([]byte, error) {
	resp, err := s.c.Do(ctx, func() (*http.Request, error) {
		return http.NewRequest("GET", s.APIBase+"/drive/v3/files/"+url.PathEscape(f.ID)+"?alt=media", nil)
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, store.ErrNotFound
	}
	if err := httpx.Check(resp); err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// delete removes a Drive file. Callers are PutPack (clearing a junk file whose size
// doesn't match its content-addressed name) and cloud cleanup (DeletePack and
// DeleteManifest).
func (s *Store) delete(ctx context.Context, id string) error {
	resp, err := s.c.Do(ctx, func() (*http.Request, error) {
		return http.NewRequest("DELETE", s.APIBase+"/drive/v3/files/"+url.PathEscape(id), nil)
	})
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil
	}
	if err := httpx.Check(resp); err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// create uploads a new file with the given name and appProperties (which hold the
// world id and kind so listing can filter on them). Small files go in one multipart
// request, larger ones use the resumable protocol.
func (s *Store) create(ctx context.Context, name string, props map[string]string, data []byte) error {
	meta, _ := json.Marshal(map[string]any{
		"name":          name,
		"parents":       []string{"appDataFolder"},
		"appProperties": props,
	})
	if len(data) <= multipartMaxBytes {
		return s.createMultipart(ctx, meta, data)
	}
	return s.createResumable(ctx, meta, data)
}

// createMultipart uploads metadata and content in a single request (up to 5 MiB).
func (s *Store) createMultipart(ctx context.Context, meta, data []byte) error {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := textproto.MIMEHeader{"Content-Type": {"application/json; charset=UTF-8"}}
	pw, _ := mw.CreatePart(h)
	pw.Write(meta)
	pw, _ = mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"application/octet-stream"}})
	pw.Write(data)
	mw.Close()
	ct := "multipart/related; boundary=" + mw.Boundary()
	resp, err := s.c.Do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequest("POST", s.APIBase+"/upload/drive/v3/files?uploadType=multipart&fields=id", bytes.NewReader(body.Bytes()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", ct)
		return req, nil
	})
	if err != nil {
		return err
	}
	if err := httpx.Check(resp); err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// createResumable uploads in two steps: ask Drive for an upload session URL, then
// send the content to it. Used for packs over 5 MiB. The session URL carries its own
// credential, so the second request has no bearer token.
func (s *Store) createResumable(ctx context.Context, meta, data []byte) error {
	resp, err := s.c.Do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequest("POST", s.APIBase+"/upload/drive/v3/files?uploadType=resumable&fields=id", bytes.NewReader(meta))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json; charset=UTF-8")
		req.Header.Set("X-Upload-Content-Type", "application/octet-stream")
		req.Header.Set("X-Upload-Content-Length", fmt.Sprint(len(data)))
		return req, nil
	})
	if err != nil {
		return err
	}
	if err := httpx.Check(resp); err != nil {
		return err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if loc == "" {
		return fmt.Errorf("gdrive: resumable upload gave no session URL")
	}
	// The session URL carries its own credential, so no bearer token here.
	resp, err = httpx.Do(ctx, s.c.HTTP, func() (*http.Request, error) {
		req, err := http.NewRequest("PUT", loc, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		return req, nil
	})
	if err != nil {
		return err
	}
	if err := httpx.Check(resp); err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// PutPack stores a pack. If a file of that name and size is already there it is
// left alone (content-addressed names mean same size is the same pack in practice;
// the engine verifies hashes on download). A file with that name but a different size
// can only be junk from an interrupted upload, and is removed first.
func (s *Store) PutPack(ctx context.Context, name string, data []byte) error {
	if !store.ValidPackName(name) {
		return fmt.Errorf("gdrive: bad pack name %q", name)
	}
	existing, err := s.byName(ctx, packFile(name))
	if err != nil {
		return err
	}
	ok := false
	for _, f := range existing {
		if f.Size == fmt.Sprint(len(data)) {
			ok = true
			continue
		}
		// A different size under a content-addressed name can only be junk.
		if err := s.delete(ctx, f.ID); err != nil {
			return err
		}
	}
	if ok {
		return nil
	}
	return s.create(ctx, packFile(name), map[string]string{"kind": "pack"}, data)
}

// GetPack downloads a pack, or store.ErrNotFound.
func (s *Store) GetPack(ctx context.Context, name string) ([]byte, error) {
	if !store.ValidPackName(name) {
		return nil, fmt.Errorf("gdrive: bad pack name %q", name)
	}
	fs, err := s.byName(ctx, packFile(name))
	if err != nil {
		return nil, err
	}
	if len(fs) == 0 {
		return nil, store.ErrNotFound
	}
	return s.download(ctx, fs[0])
}

// PutManifest checks for an existing file first. Drive can't enforce unique
// names, but manifest names embed the generation and device id, so two writers
// colliding on one name is not a realistic case.
func (s *Store) PutManifest(ctx context.Context, world, name string, data []byte) error {
	if !manifest.ValidID(world) || !store.ValidManifestName(name) {
		return fmt.Errorf("gdrive: bad manifest name %q/%q", world, name)
	}
	existing, err := s.byName(ctx, manifestFile(world, name))
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return store.ErrExists
	}
	return s.create(ctx, manifestFile(world, name), map[string]string{"kind": "manifest", "world": world}, data)
}

// GetManifest downloads a manifest, or store.ErrNotFound.
func (s *Store) GetManifest(ctx context.Context, world, name string) ([]byte, error) {
	if !manifest.ValidID(world) || !store.ValidManifestName(name) {
		return nil, fmt.Errorf("gdrive: bad manifest name %q/%q", world, name)
	}
	fs, err := s.byName(ctx, manifestFile(world, name))
	if err != nil {
		return nil, err
	}
	if len(fs) == 0 {
		return nil, store.ErrNotFound
	}
	return s.download(ctx, fs[0])
}

var _ store.Remover = (*Store)(nil)

// DeletePack removes every Drive file named for the pack (Drive allows duplicates).
func (s *Store) DeletePack(ctx context.Context, name string) error {
	if !store.ValidPackName(name) {
		return fmt.Errorf("gdrive: bad pack name %q", name)
	}
	return s.deleteNamed(ctx, packFile(name))
}

// DeleteManifest removes every Drive file named for the manifest.
func (s *Store) DeleteManifest(ctx context.Context, world, name string) error {
	if !manifest.ValidID(world) || !store.ValidManifestName(name) {
		return fmt.Errorf("gdrive: bad manifest name %q/%q", world, name)
	}
	return s.deleteNamed(ctx, manifestFile(world, name))
}

// deleteNamed deletes all files with an exact name; none is fine.
func (s *Store) deleteNamed(ctx context.Context, name string) error {
	fs, err := s.byName(ctx, name)
	if err != nil {
		return err
	}
	for _, f := range fs {
		if err := s.delete(ctx, f.ID); err != nil {
			return err
		}
	}
	return nil
}

// ListWorlds finds every manifest in the app folder and groups them by world.
// MissingPacks implements store.PackChecker with one listing of every pack file, then a comparison in memory (cheaper than a request per pack).
func (s *Store) MissingPacks(ctx context.Context, names []string) ([]string, error) {
	fs, err := s.list(ctx, "appProperties has { key='kind' and value='pack' }")
	if err != nil {
		return nil, err
	}
	have := make(map[string]bool, len(fs))
	for _, f := range fs {
		if n, ok := strings.CutPrefix(f.Name, "packs/"); ok {
			have[n] = true
		}
	}
	return store.Missing(names, have), nil
}

// ListWorlds lists the ids of worlds that have at least one manifest, from the
// manifest files' appProperties and names.
func (s *Store) ListWorlds(ctx context.Context) ([]string, error) {
	fs, err := s.list(ctx, "appProperties has { key='kind' and value='manifest' }")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range fs {
		rest, ok := strings.CutPrefix(f.Name, "worlds/")
		if !ok {
			continue
		}
		id, name, ok := strings.Cut(rest, "/")
		if ok && manifest.ValidID(id) && store.ValidManifestName(name) && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ListManifests lists a world's manifest names, sorted.
func (s *Store) ListManifests(ctx context.Context, world string) ([]string, error) {
	if !manifest.ValidID(world) {
		return nil, fmt.Errorf("gdrive: bad world id %q", world)
	}
	fs, err := s.list(ctx, "appProperties has { key='world' and value='"+esc(world)+"' }")
	if err != nil {
		return nil, err
	}
	prefix := "worlds/" + world + "/"
	seen := map[string]bool{}
	var out []string
	for _, f := range fs {
		n := strings.TrimPrefix(f.Name, prefix)
		if n != f.Name && store.ValidManifestName(n) && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}
