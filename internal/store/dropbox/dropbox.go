// Package dropbox is a store.Store on a Dropbox App Folder (the app can only
// see Apps/<name>). Layout: /packs/<hash> and /worlds/<id>/<manifest>.
package dropbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/JGugino/world-hopper/internal/httpx"
	"github.com/JGugino/world-hopper/internal/manifest"
	"github.com/JGugino/world-hopper/internal/store"
)

const (
	DefaultAPIBase     = "https://api.dropboxapi.com"
	DefaultContentBase = "https://content.dropboxapi.com"
)

type Store struct {
	c *httpx.Authed
	// APIBase and ContentBase override the Dropbox hosts (tests).
	APIBase, ContentBase string
}

var _ store.Store = (*Store)(nil)

func New(auth httpx.Auth, hc *http.Client) *Store {
	return &Store{c: &httpx.Authed{HTTP: hc, Auth: auth}, APIBase: DefaultAPIBase, ContentBase: DefaultContentBase}
}

// apiError is a 409 response; summary looks like "path/conflict/file/..".
type apiError struct{ summary string }

func (e *apiError) Error() string { return "dropbox: " + e.summary }

func (e *apiError) is(prefix string) bool { return strings.HasPrefix(e.summary, prefix) }

// check converts non-2xx responses, decoding Dropbox's 409 error summaries.
func check(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	defer resp.Body.Close()
	body := httpx.ReadErrBody(resp)
	if resp.StatusCode == http.StatusConflict {
		var e struct {
			Summary string `json:"error_summary"`
		}
		if json.Unmarshal([]byte(body), &e) == nil && e.Summary != "" {
			return &apiError{summary: e.Summary}
		}
	}
	return &httpx.StatusError{Code: resp.StatusCode, Body: body}
}

func (s *Store) upload(ctx context.Context, path string, data []byte) error {
	arg, _ := json.Marshal(map[string]any{
		"path": path, "mode": "add", "autorename": false, "mute": true,
	})
	resp, err := s.c.Do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequest("POST", s.ContentBase+"/2/files/upload", bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Dropbox-API-Arg", string(arg))
		return req, nil
	})
	if err != nil {
		return err
	}
	if err := check(resp); err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (s *Store) download(ctx context.Context, path string) ([]byte, error) {
	arg, _ := json.Marshal(map[string]string{"path": path})
	resp, err := s.c.Do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequest("POST", s.ContentBase+"/2/files/download", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Dropbox-API-Arg", string(arg))
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	if err := check(resp); err != nil {
		if ae, ok := err.(*apiError); ok && ae.is("path/not_found") {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (s *Store) PutPack(ctx context.Context, name string, data []byte) error {
	if !store.ValidPackName(name) {
		return fmt.Errorf("dropbox: bad pack name %q", name)
	}
	err := s.upload(ctx, "/packs/"+name, data)
	if ae, ok := err.(*apiError); ok && ae.is("path/conflict") {
		return nil // content-addressed and write-once: already stored
	}
	return err
}

func (s *Store) GetPack(ctx context.Context, name string) ([]byte, error) {
	if !store.ValidPackName(name) {
		return nil, fmt.Errorf("dropbox: bad pack name %q", name)
	}
	return s.download(ctx, "/packs/"+name)
}

func (s *Store) PutManifest(ctx context.Context, world, name string, data []byte) error {
	if !manifest.ValidID(world) || !store.ValidManifestName(name) {
		return fmt.Errorf("dropbox: bad manifest name %q/%q", world, name)
	}
	// mode "add" without autorename fails on an existing path: write-once.
	err := s.upload(ctx, "/worlds/"+world+"/"+name, data)
	if ae, ok := err.(*apiError); ok && ae.is("path/conflict") {
		return store.ErrExists
	}
	return err
}

func (s *Store) GetManifest(ctx context.Context, world, name string) ([]byte, error) {
	if !manifest.ValidID(world) || !store.ValidManifestName(name) {
		return nil, fmt.Errorf("dropbox: bad manifest name %q/%q", world, name)
	}
	return s.download(ctx, "/worlds/"+world+"/"+name)
}

func (s *Store) rpc(ctx context.Context, endpoint string, arg any, out any) error {
	body, _ := json.Marshal(arg)
	resp, err := s.c.Do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequest("POST", s.APIBase+endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
	if err != nil {
		return err
	}
	if err := check(resp); err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

func (s *Store) ListManifests(ctx context.Context, world string) ([]string, error) {
	if !manifest.ValidID(world) {
		return nil, fmt.Errorf("dropbox: bad world id %q", world)
	}
	type page struct {
		Entries []struct {
			Tag  string `json:".tag"`
			Name string `json:"name"`
		} `json:"entries"`
		Cursor  string `json:"cursor"`
		HasMore bool   `json:"has_more"`
	}
	var out []string
	var p page
	err := s.rpc(ctx, "/2/files/list_folder", map[string]any{"path": "/worlds/" + world, "limit": 2000}, &p)
	for {
		if ae, ok := err.(*apiError); ok && ae.is("path/not_found") {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		for _, e := range p.Entries {
			if e.Tag == "file" && store.ValidManifestName(e.Name) {
				out = append(out, e.Name)
			}
		}
		if !p.HasMore {
			break
		}
		cursor := p.Cursor
		p = page{}
		err = s.rpc(ctx, "/2/files/list_folder/continue", map[string]string{"cursor": cursor}, &p)
	}
	sort.Strings(out)
	return out, nil
}
