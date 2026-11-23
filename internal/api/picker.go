package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// errNoPicker means this machine has no native folder dialog we can drive.
var errNoPicker = errors.New("no folder picker is available on this system")

// pickFolder opens the operating system's own folder dialog and returns the
// chosen path, or "" if the user cancelled. It uses tools that ship with the OS
// (PowerShell, osascript) or are near-universal on Linux (zenity, kdialog), so
// no extra dependency is needed.
func pickFolder(ctx context.Context, title string) (string, error) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		script := `Add-Type -AssemblyName System.Windows.Forms; ` +
			`$d = New-Object System.Windows.Forms.FolderBrowserDialog; ` +
			`$d.Description = '` + strings.ReplaceAll(title, "'", "''") + `'; ` +
			`$d.ShowNewFolderButton = $false; ` +
			`if ($d.ShowDialog() -eq 'OK') { [Console]::Out.Write($d.SelectedPath) }`
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-STA", "-Command", script)
	case "darwin":
		script := `try
	set p to POSIX path of (choose folder with prompt "` + strings.ReplaceAll(title, `"`, "") + `")
	return p
on error
	return ""
end try`
		cmd = exec.CommandContext(ctx, "osascript", "-e", script)
	default:
		if p, err := exec.LookPath("zenity"); err == nil {
			cmd = exec.CommandContext(ctx, p, "--file-selection", "--directory", "--title="+title)
		} else if p, err := exec.LookPath("kdialog"); err == nil {
			cmd = exec.CommandContext(ctx, p, "--getexistingdirectory", ".", "--title", title)
		} else {
			return "", errNoPicker
		}
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		// zenity and kdialog exit non-zero when the user cancels.
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", nil
		}
		return "", errNoPicker
	}
	p := strings.TrimSpace(out.String())
	if len(p) > 1 {
		p = strings.TrimRight(p, "/\\")
	}
	return p, nil
}

func (s *Server) registerPicker(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/pick-folder", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		path, err := s.pickFn()(ctx, "Choose a folder for WorldHop")
		if err != nil {
			writeErr(w, http.StatusNotImplemented, err)
			return
		}
		writeJSON(w, map[string]string{"path": path})
	})
}

func (s *Server) pickFn() func(context.Context, string) (string, error) {
	if s.Pick != nil {
		return s.Pick
	}
	return pickFolder
}
