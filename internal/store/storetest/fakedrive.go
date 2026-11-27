// fakedrive.go: a fake Google Drive for tests, so the Drive store and the sync engine
// can be tested end to end with no network and no Google account.

package storetest

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// FakeDrive is a minimal Google Drive v3 appDataFolder server. It speaks only
// what package gdrive uses: list with name/appProperties queries (paged two at
// a time to exercise paging), media download, delete, and multipart and
// resumable uploads.
type FakeDrive struct {
	*httptest.Server
	mu       sync.Mutex
	files    map[string]*driveFile
	order    []string
	sessions map[string]string // upload session -> metadata json
	next     int
	// Token is the bearer token the server accepts.
	Token string
	// Fail503 makes every Nth request answer 503 (Retry-After: 0).
	Fail503 int
	reqs    int
	Uploads int // number of file creations
}

// driveFile is one stored file: id, name, appProperties (world id and kind) and
// content.
type driveFile struct {
	ID, Name string
	Props    map[string]string
	Data     []byte
}

// NewFakeDrive returns an empty fake Drive. Serve it with httptest and point a
// gdrive.Store's APIBase at it.
func NewFakeDrive() *FakeDrive {
	d := &FakeDrive{files: map[string]*driveFile{}, sessions: map[string]string{}, Token: "tok"}
	d.Server = httptest.NewServer(http.HandlerFunc(d.serve))
	return d
}

// Names returns all stored file names, sorted.
func (d *FakeDrive) Names() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, f := range d.files {
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

var (
	nameQ = regexp.MustCompile(`^name='((?:[^'\\]|\\.)*)' and trashed=false$`)
	kindQ = regexp.MustCompile(`^appProperties has \{ key='kind' and value='((?:[^'\\]|\\.)*)' \} and trashed=false$`)
	propQ = regexp.MustCompile(`^appProperties has \{ key='world' and value='((?:[^'\\]|\\.)*)' \} and trashed=false$`)
)

// unesc reverses the single-quote escaping gdrive applies to query values.
func unesc(s string) string { return strings.NewReplacer(`\'`, `'`, `\\`, `\`).Replace(s) }

// serve routes a request to the Drive endpoint it imitates: list, media download,
// delete, multipart upload, or the two-step resumable upload.
func (d *FakeDrive) serve(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reqs++
	if d.Fail503 > 0 && d.reqs%d.Fail503 == 0 {
		w.Header().Set("Retry-After", "0")
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	// Resumable upload sessions are authorised by their URL.
	if !strings.HasPrefix(r.URL.Path, "/session/") && r.Header.Get("Authorization") != "Bearer "+d.Token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	p := r.URL.Path
	switch {
	case r.Method == "GET" && p == "/drive/v3/files":
		d.list(w, r)
	case r.Method == "GET" && strings.HasPrefix(p, "/drive/v3/files/"):
		f := d.files[strings.TrimPrefix(p, "/drive/v3/files/")]
		if f == nil {
			http.Error(w, "nf", http.StatusNotFound)
			return
		}
		w.Write(f.Data)
	case r.Method == "DELETE" && strings.HasPrefix(p, "/drive/v3/files/"):
		id := strings.TrimPrefix(p, "/drive/v3/files/")
		if d.files[id] == nil {
			http.Error(w, "nf", http.StatusNotFound)
			return
		}
		delete(d.files, id)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == "POST" && p == "/upload/drive/v3/files":
		switch r.URL.Query().Get("uploadType") {
		case "multipart":
			d.multipart(w, r)
		case "resumable":
			meta, _ := io.ReadAll(r.Body)
			d.next++
			sid := fmt.Sprint("s", d.next)
			d.sessions[sid] = string(meta)
			w.Header().Set("Location", "http://"+r.Host+"/session/"+sid)
		default:
			http.Error(w, "bad type", http.StatusBadRequest)
		}
	case r.Method == "PUT" && strings.HasPrefix(p, "/session/"):
		meta := d.sessions[strings.TrimPrefix(p, "/session/")]
		if meta == "" {
			http.Error(w, "no session", http.StatusNotFound)
			return
		}
		data, _ := io.ReadAll(r.Body)
		d.create([]byte(meta), data)
		w.Write([]byte(`{"id":"x"}`))
	default:
		http.Error(w, "unhandled "+r.Method+" "+p, http.StatusNotImplemented)
	}
}

// create stores a new file from an upload's metadata and content.
func (d *FakeDrive) create(meta, data []byte) {
	var m struct {
		Name          string
		Parents       []string
		AppProperties map[string]string
	}
	json.Unmarshal(meta, &m)
	d.next++
	id := fmt.Sprint("f", d.next)
	d.files[id] = &driveFile{ID: id, Name: m.Name, Props: m.AppProperties, Data: append([]byte(nil), data...)}
	d.order = append(d.order, id)
	d.Uploads++
}

// multipart handles a one-request upload (metadata part, then content part).
func (d *FakeDrive) multipart(w http.ResponseWriter, r *http.Request) {
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	var parts [][]byte
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(part)
		parts = append(parts, b)
	}
	if len(parts) != 2 {
		http.Error(w, "need 2 parts", 400)
		return
	}
	d.create(parts[0], parts[1])
	w.Write([]byte(`{"id":"x"}`))
}

// list answers a files.list query for a name or an appProperties key and value,
// returning two files per page so paging is exercised.
func (d *FakeDrive) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("spaces") != "appDataFolder" {
		http.Error(w, "spaces", 400)
		return
	}
	var match func(*driveFile) bool
	if m := nameQ.FindStringSubmatch(q.Get("q")); m != nil {
		name := unesc(m[1])
		match = func(f *driveFile) bool { return f.Name == name }
	} else if m := kindQ.FindStringSubmatch(q.Get("q")); m != nil {
		kind := unesc(m[1])
		match = func(f *driveFile) bool { return f.Props["kind"] == kind }
	} else if m := propQ.FindStringSubmatch(q.Get("q")); m != nil {
		world := unesc(m[1])
		match = func(f *driveFile) bool { return f.Props["world"] == world }
	} else {
		http.Error(w, "unsupported q: "+q.Get("q"), 400)
		return
	}
	var all []*driveFile
	for _, id := range d.order {
		if f := d.files[id]; f != nil && match(f) {
			all = append(all, f)
		}
	}
	start, _ := strconv.Atoi(q.Get("pageToken"))
	end := min(start+2, len(all))
	type item struct {
		ID, Name, Size string
	}
	var out struct {
		NextPageToken string              `json:"nextPageToken,omitempty"`
		Files         []map[string]string `json:"files"`
	}
	for _, f := range all[start:end] {
		out.Files = append(out.Files, map[string]string{"id": f.ID, "name": f.Name, "size": fmt.Sprint(len(f.Data))})
	}
	if end < len(all) {
		out.NextPageToken = fmt.Sprint(end)
	}
	json.NewEncoder(w).Encode(out)
}

// Seed adds a file directly (kind/world become appProperties).
func (d *FakeDrive) Seed(name, kind, world string, data []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.next++
	id := fmt.Sprint("f", d.next)
	props := map[string]string{"kind": kind}
	if world != "" {
		props["world"] = world
	}
	d.files[id] = &driveFile{ID: id, Name: name, Props: props, Data: data}
	d.order = append(d.order, id)
}
