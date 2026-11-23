package sync

// A realistic end-to-end timing harness for the sync engine. It is skipped
// unless WORLDHOP_BENCH is set, because it writes a few hundred MB:
//
//	WORLDHOP_BENCH=1 go test ./internal/sync -run TestBenchSync -v -timeout 30m
//
// It builds a Minecraft-like world (large region files, many small files),
// then times the real Runner (the code the tray, scheduler and Force Sync use)
// for the cases users hit: first upload, nothing changed, one chunk changed,
// pulling that change on a second computer, and a first download. The stores
// are the plain directory store, and the Google Drive and S3 stores talking to
// in-process fake servers with simulated latency and bandwidth.
//
// WORLDHOP_BENCH_MB scales the region data (default 300).

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	stdsync "sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JGugino/world-hopper/internal/httpx"
	"github.com/JGugino/world-hopper/internal/store"
	"github.com/JGugino/world-hopper/internal/store/gdrive"
	"github.com/JGugino/world-hopper/internal/store/localdir"
	"github.com/JGugino/world-hopper/internal/store/s3"
	"github.com/JGugino/world-hopper/internal/store/storetest"
	"github.com/JGugino/world-hopper/internal/worlds"
)

// ---- simulated network ----

// link models a shared internet connection: a global bandwidth cap per
// direction plus a fixed round trip per request.
type link struct {
	rtt       time.Duration
	upBPS     float64
	downBPS   float64
	mu        stdsync.Mutex
	upFree    time.Time
	downFree  time.Time
	requests  atomic.Int64
	upBytes   atomic.Int64
	downBytes atomic.Int64
}

func (l *link) wait(free *time.Time, n int, bps float64) {
	if bps <= 0 || n <= 0 {
		return
	}
	d := time.Duration(float64(n) / bps * float64(time.Second))
	l.mu.Lock()
	now := time.Now()
	if free.Before(now) {
		*free = now
	}
	*free = free.Add(d)
	until := *free
	l.mu.Unlock()
	time.Sleep(time.Until(until))
}

type throttled struct {
	r  io.ReadCloser
	fn func(int)
}

func (t *throttled) Read(p []byte) (int, error) {
	if len(p) > 256<<10 {
		p = p[:256<<10]
	}
	n, err := t.r.Read(p)
	if n > 0 {
		t.fn(n)
	}
	return n, err
}
func (t *throttled) Close() error { return t.r.Close() }

type simTransport struct {
	l    *link
	base http.RoundTripper
}

func (s *simTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.l.requests.Add(1)
	time.Sleep(s.l.rtt)
	if req.Body != nil && req.Body != http.NoBody {
		req.Body = &throttled{req.Body, func(n int) {
			s.l.upBytes.Add(int64(n))
			s.l.wait(&s.l.upFree, n, s.l.upBPS)
		}}
	}
	resp, err := s.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = &throttled{resp.Body, func(n int) {
		s.l.downBytes.Add(int64(n))
		s.l.wait(&s.l.downFree, n, s.l.downBPS)
	}}
	return resp, nil
}

// ---- world fixture ----

func fill(path string, size int, seed int64) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		panic(err)
	}
	f, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	r := rand.New(rand.NewSource(seed))
	buf := make([]byte, 1<<20)
	for left := size; left > 0; {
		n := min(left, len(buf))
		r.Read(buf[:n])
		f.Write(buf[:n])
		left -= n
	}
}

// buildWorld writes a world shaped like a long-lived survival save: compressed
// (so incompressible) region files, plus hundreds of small files.
func buildWorld(root string, regionMB int) (files int, bytes int64) {
	seed := int64(1)
	add := func(rel string, size int) {
		fill(filepath.Join(root, filepath.FromSlash(rel)), size, seed)
		seed++
		files++
		bytes += int64(size)
	}
	add("level.dat", 3000)
	add("level.dat_old", 3000)
	os.WriteFile(filepath.Join(root, "session.lock"), []byte("lock"), 0o644)
	nRegion := max(regionMB/25, 1)
	for i := 0; i < nRegion; i++ {
		add(fmt.Sprintf("region/r.%d.%d.mca", i%4, i/4), 25<<20)
	}
	for i := 0; i < 6; i++ {
		add(fmt.Sprintf("entities/r.%d.0.mca", i), 2<<20)
		add(fmt.Sprintf("poi/r.%d.0.mca", i), 300<<10)
	}
	for i := 0; i < 4; i++ {
		add(fmt.Sprintf("DIM-1/region/r.%d.0.mca", i), 4<<20)
		add(fmt.Sprintf("DIM1/region/r.%d.0.mca", i), 2<<20)
	}
	for i := 0; i < 40; i++ { // 40 players
		add(fmt.Sprintf("playerdata/%08x-aaaa-bbbb-cccc-%012x.dat", i, i), 4000)
		add(fmt.Sprintf("advancements/%08x-aaaa-bbbb-cccc-%012x.json", i, i), 30000)
		add(fmt.Sprintf("stats/%08x-aaaa-bbbb-cccc-%012x.json", i, i), 6000)
	}
	for i := 0; i < 150; i++ {
		add(fmt.Sprintf("data/map_%d.dat", i), 16000)
	}
	for i := 0; i < 20; i++ {
		add(fmt.Sprintf("datapacks/pack/data/f%d.json", i), 1200)
	}
	return files, bytes
}

// touchChunk overwrites 8 KiB inside a region file, like the game saving one
// changed chunk, and bumps the modification time.
func touchChunk(root string) {
	p := filepath.Join(root, "region", "r.1.1.mca")
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		p = filepath.Join(root, "region", "r.0.0.mca")
		f, err = os.OpenFile(p, os.O_RDWR, 0)
		if err != nil {
			panic(err)
		}
	}
	buf := make([]byte, 8192)
	rand.New(rand.NewSource(time.Now().UnixNano())).Read(buf)
	f.WriteAt(buf, 11<<20+4096)
	f.Close()
	t := time.Now().Add(-10 * time.Second) // modified before the game closed
	os.Chtimes(p, t, t)
	// the player file changes too
	pp := filepath.Join(root, "playerdata", fmt.Sprintf("%08x-aaaa-bbbb-cccc-%012x.dat", 0, 0))
	os.WriteFile(pp, buf[:4000], 0o644)
	os.Chtimes(pp, t, t)
}

// ---- backends ----

type backend struct {
	name string
	mk   func(t *testing.T) func() (store.Store, *link) // returns a factory: one store per "computer"
}

func netLink() *link {
	return &link{rtt: 50 * time.Millisecond, upBPS: 25e6, downBPS: 50e6}
}

func backends() []backend {
	return []backend{
		{"localdir", func(t *testing.T) func() (store.Store, *link) {
			dir := t.TempDir()
			return func() (store.Store, *link) { return localdir.New(dir), &link{} }
		}},
		{"drive", func(t *testing.T) func() (store.Store, *link) {
			httpx.BaseDelay = time.Millisecond
			fake := storetest.NewFakeDrive()
			t.Cleanup(fake.Close)
			return func() (store.Store, *link) {
				l := netLink()
				hc := &http.Client{Transport: &simTransport{l, http.DefaultTransport}}
				s := gdrive.New(&storetest.Auth{Fresh: "tok"}, hc)
				s.APIBase = fake.URL
				return s, l
			}
		}},
		{"s3", func(t *testing.T) func() (store.Store, *link) {
			httpx.BaseDelay = time.Millisecond
			f := storetest.NewFakeS3("bkt", "AKIAEXAMPLEEXAMPLE", "secret", "us-east-1")
			t.Cleanup(f.Close)
			return func() (store.Store, *link) {
				l := netLink()
				hc := &http.Client{Transport: &simTransport{l, http.DefaultTransport}}
				s, err := s3.New(s3.Config{Endpoint: f.URL, Region: "us-east-1", Bucket: "bkt", PathStyle: true,
					AccessKey: "AKIAEXAMPLEEXAMPLE", SecretKey: "secret"}, hc)
				if err != nil {
					t.Fatal(err)
				}
				return s, l
			}
		}},
	}
}

func TestBenchSync(t *testing.T) {
	if os.Getenv("WORLDHOP_BENCH") == "" {
		t.Skip("set WORLDHOP_BENCH=1 to run the timing harness")
	}
	mb := 300
	if v, err := strconv.Atoi(os.Getenv("WORLDHOP_BENCH_MB")); err == nil && v > 0 {
		mb = v
	}
	only := os.Getenv("WORLDHOP_BENCH_STORE")
	for _, be := range backends() {
		if only != "" && only != be.name {
			continue
		}
		t.Run(be.name, func(t *testing.T) { benchBackend(t, be, mb) })
	}
}

func benchBackend(t *testing.T, be backend, mb int) {
	ctx := context.Background()
	factory := be.mk(t)

	// Computer A holds the world.
	baseA, baseB := t.TempDir(), t.TempDir()
	worldA := filepath.Join(baseA, "saves", "Survival")
	nfiles, nbytes := buildWorld(worldA, mb)
	t.Logf("world: %d files, %.0f MB", nfiles, float64(nbytes)/1e6)

	mkDevice := func(base string) (*Runner, *Engine, *link, worlds.World) {
		st, l := factory()
		e, err := New(st, filepath.Join(base, "state"))
		if err != nil {
			t.Fatal(err)
		}
		w := worlds.World{ID: "Survival", Name: "Survival", Path: filepath.Join(base, "saves", "Survival")}
		r := NewRunner("bench", e, func() []worlds.World { return []worlds.World{w} })
		return r, e, l, w
	}
	rA, _, lA, wA := mkDevice(baseA)
	rB, eB, lB, wB := mkDevice(baseB)

	step := func(label string, r *Runner, l *link, f func() string) {
		var req0, up0, down0 int64
		req0, up0, down0 = l.requests.Load(), l.upBytes.Load(), l.downBytes.Load()
		start := time.Now()
		res := f()
		d := time.Since(start)
		t.Logf("%-34s %8.2fs  reqs=%-4d up=%6.1fMB down=%6.1fMB  %s", label, d.Seconds(),
			l.requests.Load()-req0, float64(l.upBytes.Load()-up0)/1e6, float64(l.downBytes.Load()-down0)/1e6, res)
	}
	run := func(r *Runner, w worlds.World) func() string {
		return func() string { return r.SyncWorlds(ctx, []worlds.World{w}).LastResult }
	}

	step("A: first upload", rA, lA, run(rA, wA))
	step("A: sync, nothing changed", rA, lA, run(rA, wA))
	touchChunk(worldA)
	step("A: sync, one chunk changed", rA, lA, run(rA, wA))
	step("A: sync, nothing changed", rA, lA, run(rA, wA))

	// Computer B has no copy: download (cloud-only world).
	savesB := filepath.Join(baseB, "saves")
	os.MkdirAll(savesB, 0o755)
	step("B: first download", rB, lB, func() string {
		res, err := eB.Download(ctx, "Survival", savesB)
		if err != nil {
			t.Fatal(err)
		}
		return res.Manifest
	})
	step("B: sync, nothing changed", rB, lB, run(rB, wB))

	touchChunk(worldA)
	step("A: push 2nd change", rA, lA, run(rA, wA))
	step("B: pull 2nd change", rB, lB, run(rB, wB))
	step("B: sync, nothing changed", rB, lB, run(rB, wB))

	// Grow history: many small pushes, then measure the steady state again.
	for i := 0; i < 20; i++ {
		touchChunk(worldA)
		rA.SyncWorlds(ctx, []worlds.World{wA})
	}
	step("A: sync, nothing (20 versions)", rA, lA, run(rA, wA))
	step("B: pull after 20 versions", rB, lB, run(rB, wB))
}
