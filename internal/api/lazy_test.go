package api

import (
	"net/http"
	"testing"
	"testing/fstest"
	"time"

	wsync "github.com/JGugino/world-hopper/internal/sync"
)

func TestLazyStartsOnDemandAndStopsIdle(t *testing.T) {
	l := NewLazy("127.0.0.1:0", wsync.NewRunner("t", nil, nil), fstest.MapFS{"index.html": {Data: []byte("hi")}}, 200*time.Millisecond)
	if l.Running() {
		t.Fatal("server running before Ensure")
	}
	url, err := l.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if u2, _ := l.Ensure(); u2 != url {
		t.Fatalf("second Ensure gave %s, want %s", u2, url)
	}
	resp, err := http.Get(url + "api/status")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("status: %v %v", resp, err)
	}
	resp.Body.Close()
	deadline := time.Now().Add(3 * time.Second)
	for l.Running() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if l.Running() {
		t.Fatal("server did not stop after idle")
	}
	if _, err := http.Get(url + "api/status"); err == nil {
		t.Fatal("port still serving after idle stop")
	}
	// Can be started again afterwards.
	if _, err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	l.Close()
	if l.Running() {
		t.Fatal("still running after Close")
	}
}
