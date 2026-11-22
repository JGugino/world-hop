package storetest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// FakeDropbox is a minimal Dropbox API server (upload with mode "add",
// download, list_folder and its continuation, paged two at a time). One server
// plays both api.dropboxapi.com and content.dropboxapi.com.
type FakeDropbox struct {
	*httptest.Server
	mu      sync.Mutex
	files   map[string][]byte // full path -> data
	Token   string
	Fail429 int // every Nth request answers 429 with Retry-After: 0
	reqs    int
}

func NewFakeDropbox() *FakeDropbox {
	d := &FakeDropbox{files: map[string][]byte{}, Token: "tok"}
	d.Server = httptest.NewServer(http.HandlerFunc(d.serve))
	return d
}

// Paths returns all stored paths, sorted.
func (d *FakeDropbox) Paths() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for p := range d.files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func apiErr(w http.ResponseWriter, summary string) {
	w.WriteHeader(http.StatusConflict)
	json.NewEncoder(w).Encode(map[string]string{"error_summary": summary})
}

func (d *FakeDropbox) serve(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reqs++
	if d.Fail429 > 0 && d.reqs%d.Fail429 == 0 {
		w.Header().Set("Retry-After", "0")
		http.Error(w, "too_many_requests", http.StatusTooManyRequests)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+d.Token {
		http.Error(w, `{"error_summary":"expired_access_token/"}`, http.StatusUnauthorized)
		return
	}
	var arg struct {
		Path       string `json:"path"`
		Mode       string `json:"mode"`
		Autorename bool   `json:"autorename"`
		Cursor     string `json:"cursor"`
	}
	if h := r.Header.Get("Dropbox-API-Arg"); h != "" {
		json.Unmarshal([]byte(h), &arg)
	}
	switch r.URL.Path {
	case "/2/files/upload":
		if arg.Mode != "add" || arg.Autorename {
			http.Error(w, "must use mode add without autorename", 400)
			return
		}
		if _, ok := d.files[arg.Path]; ok {
			apiErr(w, "path/conflict/file/..")
			return
		}
		b, _ := io.ReadAll(r.Body)
		d.files[arg.Path] = b
		w.Write([]byte(`{}`))
	case "/2/files/download":
		b, ok := d.files[arg.Path]
		if !ok {
			apiErr(w, "path/not_found/..")
			return
		}
		w.Write(b)
	case "/2/files/list_folder", "/2/files/list_folder/continue":
		var body struct{ Path, Cursor string }
		json.NewDecoder(r.Body).Decode(&body)
		folder, start := body.Path, 0
		if r.URL.Path == "/2/files/list_folder/continue" {
			i := strings.LastIndex(body.Cursor, "|")
			folder, start = body.Cursor[:i], 0
			start, _ = strconv.Atoi(body.Cursor[i+1:])
		}
		var names []string
		for p := range d.files {
			if rest, ok := strings.CutPrefix(p, folder+"/"); ok && !strings.Contains(rest, "/") {
				names = append(names, rest)
			}
		}
		if len(names) == 0 {
			apiErr(w, "path/not_found/..")
			return
		}
		sort.Strings(names)
		end := min(start+2, len(names))
		type entry struct {
			Tag  string `json:".tag"`
			Name string `json:"name"`
		}
		out := struct {
			Entries []entry `json:"entries"`
			Cursor  string  `json:"cursor"`
			HasMore bool    `json:"has_more"`
		}{Cursor: folder + "|" + strconv.Itoa(end), HasMore: end < len(names)}
		for _, n := range names[start:end] {
			out.Entries = append(out.Entries, entry{"file", n})
		}
		json.NewEncoder(w).Encode(out)
	default:
		http.Error(w, "unhandled "+r.URL.Path, http.StatusNotImplemented)
	}
}
