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

type Store struct {
	c *httpx.Authed
	// APIBase overrides the Google API host (tests).
	APIBase string
}

var _ store.Store = (*Store)(nil)

func New(auth httpx.Auth, hc *http.Client) *Store {
	return &Store{c: &httpx.Authed{HTTP: hc, Auth: auth}, APIBase: DefaultAPIBase}
}

type file struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size string `json:"size"`
}

func esc(s string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s)
}

func packFile(name string) string        { return "packs/" + name }
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

func (s *Store) byName(ctx context.Context, name string) ([]file, error) {
	return s.list(ctx, "name='"+esc(name)+"'")
}

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
