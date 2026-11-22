package detect

import (
	"bufio"
	"bytes"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

var javaExe = regexp.MustCompile(`(?i)(^|[\\/ "])javaw?(\.exe)?"?$`)

// Substrings (lower case) that identify a Minecraft client or server JVM:
// the vanilla main classes and the common mod loaders and launchers.
var minecraftMarkers = []string{
	"net.minecraft.client.main",
	"net.minecraft.launchwrapper",
	"net.minecraft.server",
	"net.minecraftforge",
	"net.neoforged",
	"net.fabricmc",
	"org.quiltmc",
	"cpw.mods.modlauncher",
	"cpw.mods.bootstraplauncher",
	"-dminecraft.",
}

// IsMinecraftProcess reports whether p is a Java process running Minecraft
// (client, integrated server or dedicated server). The launcher app itself
// does not count: it does not hold world locks.
func IsMinecraftProcess(p Proc) bool {
	cmd := strings.TrimSpace(p.Cmdline)
	exe := cmd
	if i := strings.Index(cmd, " -"); i >= 0 {
		exe = cmd[:i]
	}
	if !javaExe.MatchString(strings.TrimSpace(exe)) {
		return false
	}
	low := strings.ToLower(cmd)
	for _, m := range minecraftMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

// ExecLister lists processes by running an OS command.
type ExecLister struct {
	GOOS string
	// Run runs a command and returns its stdout. Defaults to os/exec.
	Run func(name string, args ...string) ([]byte, error)
}

// NewExecLister returns a lister for the current platform.
func NewExecLister() *ExecLister { return &ExecLister{GOOS: runtime.GOOS} }

func (l *ExecLister) run(name string, args ...string) ([]byte, error) {
	if l.Run != nil {
		return l.Run(name, args...)
	}
	return exec.Command(name, args...).Output()
}

const winScript = `Get-CimInstance Win32_Process -Filter "Name like 'java%'" | ForEach-Object { "$($_.ProcessId)` + "`t" + `$($_.CommandLine)" }`

// List implements ProcessLister.
func (l *ExecLister) List() ([]Proc, error) {
	if l.GOOS == "windows" {
		out, err := l.run("powershell", "-NoProfile", "-NonInteractive", "-Command", winScript)
		if err != nil {
			return nil, err
		}
		return ParseWindows(out), nil
	}
	out, err := l.run("ps", "-axww", "-o", "pid=,args=")
	if err != nil {
		return nil, err
	}
	return ParsePS(out), nil
}

// ParsePS parses `ps -axww -o pid=,args=` output.
func ParsePS(out []byte) []Proc {
	var procs []Proc
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		i := strings.IndexAny(line, " \t")
		if i < 0 {
			continue
		}
		pid, err := strconv.Atoi(line[:i])
		if err != nil {
			continue
		}
		procs = append(procs, Proc{PID: pid, Cmdline: strings.TrimSpace(line[i:])})
	}
	return procs
}

// ParseWindows parses "pid<TAB>commandline" lines.
func ParseWindows(out []byte) []Proc {
	var procs []Proc
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		pidStr, cmd, ok := strings.Cut(strings.TrimSpace(sc.Text()), "\t")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(pidStr))
		if err != nil {
			continue
		}
		procs = append(procs, Proc{PID: pid, Cmdline: strings.TrimSpace(cmd)})
	}
	return procs
}
