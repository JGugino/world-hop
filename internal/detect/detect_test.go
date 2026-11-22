package detect

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeProcs struct {
	procs []Proc
	err   error
	calls int
}

func (f *fakeProcs) List() ([]Proc, error) { f.calls++; return f.procs, f.err }

type fakeLock struct {
	held bool
	err  error
}

func (f fakeLock) Held(string) (bool, error) { return f.held, f.err }

func worldWithLock(t *testing.T) (dir string) {
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, LockFile), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func old(t *testing.T, dir string) {
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dir, LockFile), past, past); err != nil {
		t.Fatal(err)
	}
}

var mcProc = Proc{PID: 1, Cmdline: "/usr/lib/jvm/java-21/bin/java -Xmx4G -cp a.jar net.minecraft.client.main.Main --username x"}

func TestIsMinecraftProcess(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{mcProc.Cmdline, true},
		{`C:\Program Files\Java\bin\javaw.exe -Xmx2G -cp x net.fabricmc.loader.impl.launch.knot.KnotClient`, true},
		{`"C:\Users\a b\runtime\bin\javaw.exe" -Dminecraft.launcher.brand=minecraft-launcher -cp x`, true},
		{"/Applications/Minecraft Launcher.app/runtime/bin/java -cp x cpw.mods.bootstraplauncher.BootstrapLauncher", true},
		{"java -jar server.jar net.minecraft.server.Main nogui", true},
		{"java -jar gradle-wrapper.jar build", false},
		{"/usr/bin/python3 net.minecraft.client.main.Main", false},
		{"/usr/bin/minecraft-launcher", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsMinecraftProcess(Proc{Cmdline: c.cmd}); got != c.want {
			t.Errorf("%q: got %v want %v", c.cmd, got, c.want)
		}
	}
}

func TestParsers(t *testing.T) {
	ps := ParsePS([]byte("  1 /sbin/init\n 4242 java -cp x net.minecraft.client.main.Main\nbad line\n"))
	if len(ps) != 2 || ps[1].PID != 4242 || !IsMinecraftProcess(ps[1]) {
		t.Fatalf("ps: %+v", ps)
	}
	w := ParseWindows([]byte("77\tC:\\j\\javaw.exe -Dminecraft.x=1\r\n88\t\r\nnope\n"))
	if len(w) != 1 || w[0].PID != 77 || !IsMinecraftProcess(w[0]) {
		t.Fatalf("win: %+v", w)
	}
}

func TestExecLister(t *testing.T) {
	var gotName string
	l := &ExecLister{GOOS: "linux", Run: func(name string, args ...string) ([]byte, error) {
		gotName = name
		return []byte("5 java net.minecraft.client.main.Main\n"), nil
	}}
	p, err := l.List()
	if err != nil || gotName != "ps" || len(p) != 1 {
		t.Fatalf("%v %v %v", p, err, gotName)
	}
	l.GOOS = "windows"
	l.Run = func(name string, args ...string) ([]byte, error) {
		gotName = name
		return []byte("9\tjavaw.exe -Dminecraft.a\n"), nil
	}
	p, _ = l.List()
	if gotName != "powershell" || len(p) != 1 || p[0].PID != 9 {
		t.Fatalf("%v %v", p, gotName)
	}
	l.Run = func(string, ...string) ([]byte, error) { return nil, errors.New("boom") }
	if _, err := l.List(); err == nil {
		t.Fatal("want error")
	}
}

func TestWorldOpen(t *testing.T) {
	running := &fakeProcs{procs: []Proc{mcProc}}
	idle := &fakeProcs{}
	t.Run("no lock file", func(t *testing.T) {
		d := &Detector{Procs: running, Lock: fakeLock{held: true}}
		if d.WorldOpen(t.TempDir()) {
			t.Fatal("open")
		}
	})
	t.Run("lock held", func(t *testing.T) {
		w := worldWithLock(t)
		d := &Detector{Procs: idle, Lock: fakeLock{held: true}}
		if !d.WorldOpen(w) {
			t.Fatal("closed")
		}
	})
	t.Run("stale lock, game running elsewhere", func(t *testing.T) {
		w := worldWithLock(t)
		old(t, w)
		d := &Detector{Procs: running, Lock: fakeLock{}, FreshLock: 10 * time.Second}
		if d.WorldOpen(w) {
			t.Fatal("stale lock counted as open")
		}
	})
	t.Run("fresh lock, game running", func(t *testing.T) {
		d := &Detector{Procs: running, Lock: fakeLock{}, FreshLock: time.Hour}
		if !d.WorldOpen(worldWithLock(t)) {
			t.Fatal("closed")
		}
	})
	t.Run("fresh lock, game not running", func(t *testing.T) {
		d := &Detector{Procs: idle, Lock: fakeLock{}, FreshLock: time.Hour}
		if d.WorldOpen(worldWithLock(t)) {
			t.Fatal("open")
		}
	})
	t.Run("unsupported probe falls back to process", func(t *testing.T) {
		w := worldWithLock(t)
		old(t, w)
		if !(&Detector{Procs: running, Lock: fakeLock{err: ErrUnsupported}}).WorldOpen(w) {
			t.Fatal("running game should count as open")
		}
		if (&Detector{Procs: idle, Lock: fakeLock{err: ErrUnsupported}}).WorldOpen(w) {
			t.Fatal("no game should count as closed")
		}
	})
	t.Run("unknown means open", func(t *testing.T) {
		w := worldWithLock(t)
		d := &Detector{Procs: &fakeProcs{err: errors.New("x")}, Lock: fakeLock{err: ErrUnsupported}}
		if !d.WorldOpen(w) {
			t.Fatal("must be conservative")
		}
	})
}

func TestProcessCache(t *testing.T) {
	now := time.Unix(1000, 0)
	f := &fakeProcs{procs: []Proc{mcProc}}
	d := &Detector{Procs: f, CacheTTL: 2 * time.Second, Now: func() time.Time { return now }}
	d.GameRunning()
	d.GameRunning()
	if f.calls != 1 {
		t.Fatalf("calls=%d", f.calls)
	}
	now = now.Add(3 * time.Second)
	d.GameRunning()
	if f.calls != 2 {
		t.Fatalf("calls=%d", f.calls)
	}
}
