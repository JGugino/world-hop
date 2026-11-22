//go:build linux || darwin

package detect

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestHelperHoldLock is run as a child process; it locks the file named by
// HOLD_LOCK the way Java does and waits for stdin to close.
func TestHelperHoldLock(t *testing.T) {
	path := os.Getenv("HOLD_LOCK")
	if path == "" {
		t.Skip("helper")
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	lk := syscall.Flock_t{Type: syscall.F_WRLCK}
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lk); err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("locked\n")
	bufio.NewReader(os.Stdin).ReadByte()
}

func TestPlatformLockProberSeesOtherProcess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, LockFile)
	os.WriteFile(path, []byte("x"), 0o644)
	p := PlatformLockProber{}
	if held, err := p.Held(path); err != nil || held {
		t.Fatalf("unlocked: %v %v", held, err)
	}
	if held, err := p.Held(filepath.Join(dir, "missing")); err != nil || held {
		t.Fatalf("missing: %v %v", held, err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestHelperHoldLock")
	cmd.Env = append(os.Environ(), "HOLD_LOCK="+path)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line := make(chan string, 1)
	go func() { s, _ := bufio.NewReader(out).ReadString('\n'); line <- s }()
	select {
	case s := <-line:
		if s != "locked\n" {
			t.Fatalf("helper said %q", s)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("helper timeout")
	}
	if held, err := p.Held(path); err != nil || !held {
		t.Fatalf("locked: %v %v", held, err)
	}
	in.Close()
	cmd.Wait()
	if held, _ := p.Held(path); held {
		t.Fatal("still held after helper exit")
	}
}
