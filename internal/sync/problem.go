package sync

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"runtime"
	"strings"
	"syscall"

	"github.com/JGugino/world-hopper/internal/httpx"
	"github.com/JGugino/world-hopper/internal/license"
	"github.com/JGugino/world-hopper/internal/oauth"
	"github.com/JGugino/world-hopper/internal/store"
	"github.com/JGugino/world-hopper/internal/store/s3"
)

// Problem kinds.
const (
	ProblemWorldOpen    = "world-open"
	ProblemConflict     = "conflict"
	ProblemOffline      = "offline"
	ProblemAuth         = "auth"
	ProblemNotLinked    = "not-linked"
	ProblemQuota        = "quota"
	ProblemBusy         = "busy"
	ProblemServer       = "server"
	ProblemLimit        = "license-limit"
	ProblemPro          = "pro-required"
	ProblemLocked       = "encryption-locked"
	ProblemKey          = "encryption-key"
	ProblemDamaged      = "damaged"
	ProblemDisk         = "disk-full"
	ProblemPermission   = "permission"
	ProblemStorageSetup = "storage-setup"
	ProblemCancelled    = "cancelled"
	ProblemUnknown      = "unknown"
)

// Problem levels.
const (
	LevelInfo  = "info"  // nothing is wrong; WorldHop is waiting
	LevelWarn  = "warn"  // needs a choice from the user
	LevelError = "error" // the sync failed
)

// Problem is something that kept a sync from finishing, in words a player can
// act on. Detail keeps the raw technical message for the "Details" fold-out.
type Problem struct {
	Kind   string `json:"kind"`
	Level  string `json:"level"`
	Title  string `json:"title"`
	Action string `json:"action,omitempty"`
	// Fix is the UI page where the user can resolve it, e.g. "/cloud".
	Fix    string   `json:"fix,omitempty"`
	Detail string   `json:"detail,omitempty"`
	Worlds []string `json:"worlds,omitempty"`
}

// Explain turns an error from a sync into a Problem. It never returns an empty
// Problem for a non-nil error; an error it does not recognise becomes
// ProblemUnknown with the raw message in Detail.
func Explain(err error) Problem {
	if err == nil {
		return Problem{}
	}
	p := explain(err)
	if p.Detail == "" && p.Kind != ProblemWorldOpen {
		p.Detail = err.Error()
	}
	return p
}

func explain(err error) Problem {
	var ce *ConflictError
	var he *httpx.StatusError
	var s3e *s3.Error
	switch {
	case errors.Is(err, ErrWorldOpen):
		return Problem{Kind: ProblemWorldOpen, Level: LevelInfo,
			Title:  "Minecraft has this world open",
			Action: "Close the world and WorldHop will sync it for you."}
	case errors.As(err, &ce):
		return Problem{Kind: ProblemConflict, Level: LevelWarn,
			Title:  "This world changed on two computers",
			Action: "Open the world on the Worlds tab and choose which version to keep. Nothing was overwritten.",
			Fix:    "/"}
	case errors.Is(err, license.ErrWorldLimit):
		return Problem{Kind: ProblemLimit, Level: LevelWarn,
			Title:  "The free version syncs one world",
			Action: "Upgrade to Pro to sync more, or choose which world is the free one.",
			Fix:    "/license"}
	case errors.Is(err, license.ErrProRequired):
		return Problem{Kind: ProblemPro, Level: LevelWarn,
			Title:  "This needs WorldHop Pro",
			Action: "Activate your Pro license, or switch back to Google Drive.",
			Fix:    "/license"}
	case errors.Is(err, store.ErrNotLinked), errors.Is(err, ErrNoStore):
		return Problem{Kind: ProblemNotLinked, Level: LevelWarn,
			Title:  "No cloud account is connected yet",
			Action: "Connect Google Drive so WorldHop has somewhere to keep your worlds.",
			Fix:    "/cloud"}
	case errors.Is(err, oauth.ErrReauth), errors.Is(err, oauth.ErrNoToken):
		return Problem{Kind: ProblemAuth, Level: LevelError,
			Title:  "Your cloud sign-in expired",
			Action: "Reconnect your account on the Cloud account tab. Your worlds are safe.",
			Fix:    "/cloud"}
	case errors.Is(err, store.ErrEncryptionLocked):
		return Problem{Kind: ProblemLocked, Level: LevelWarn,
			Title:  "Encryption is locked",
			Action: "Enter your passphrase on the Encryption tab to resume syncing.",
			Fix:    "/encryption"}
	case errors.Is(err, store.ErrWrongPassphrase), errors.Is(err, store.ErrTampered),
		errors.Is(err, store.ErrNoKey), errors.Is(err, store.ErrBadHeader):
		return Problem{Kind: ProblemKey, Level: LevelError,
			Title:  "Your encrypted worlds couldn't be read",
			Action: "Check that this computer uses the same passphrase as your other computers.",
			Fix:    "/encryption"}
	case errors.Is(err, ErrIntegrity):
		return Problem{Kind: ProblemDamaged, Level: LevelError,
			Title:  "Downloaded data was damaged on the way",
			Action: "Nothing was changed. Try Force sync again."}
	case errors.Is(err, context.Canceled):
		return Problem{Kind: ProblemCancelled, Level: LevelInfo, Title: "Sync was stopped"}
	case isDiskFull(err):
		return Problem{Kind: ProblemDisk, Level: LevelError,
			Title:  "This computer is out of disk space",
			Action: "Free up some space, then sync again. Your world was not changed."}
	case errors.Is(err, os.ErrPermission):
		return Problem{Kind: ProblemPermission, Level: LevelError,
			Title:  "WorldHop can't read or write a world folder",
			Action: "Check the folder's permissions, and that antivirus or another program isn't locking it."}
	case errors.As(err, &s3e):
		return explainS3(s3e)
	case errors.As(err, &he):
		return explainHTTP(he)
	case isNetwork(err):
		return Problem{Kind: ProblemOffline, Level: LevelError,
			Title:  "Can't reach the cloud",
			Action: "Check your internet connection. WorldHop will try again by itself."}
	}
	return Problem{Kind: ProblemUnknown, Level: LevelError,
		Title:  "Something went wrong while syncing",
		Action: "Try Force sync. If it keeps happening, open Details and include them in a bug report."}
}

func explainHTTP(e *httpx.StatusError) Problem {
	body := strings.ToLower(e.Body)
	switch {
	case strings.Contains(body, "storagequotaexceeded"), e.Code == 507:
		return quotaProblem()
	case strings.Contains(body, "ratelimit"), e.Code == 429:
		return busyProblem()
	case e.Code == 401:
		return Problem{Kind: ProblemAuth, Level: LevelError,
			Title:  "Your cloud sign-in expired",
			Action: "Reconnect your account on the Cloud account tab. Your worlds are safe.",
			Fix:    "/cloud"}
	case e.Code == 403:
		return Problem{Kind: ProblemAuth, Level: LevelError,
			Title:  "The cloud refused WorldHop's access",
			Action: "Reconnect your account and allow access to its app folder.",
			Fix:    "/cloud"}
	case e.Code >= 500:
		return Problem{Kind: ProblemServer, Level: LevelError,
			Title:  "The cloud service is having trouble",
			Action: "This is on their side. WorldHop will try again in a few minutes."}
	}
	return Problem{Kind: ProblemUnknown, Level: LevelError,
		Title:  "The cloud service rejected a request",
		Action: "Try Force sync. If it keeps happening, open Details and include them in a bug report."}
}

func explainS3(e *s3.Error) Problem {
	setup := func(title, action string) Problem {
		return Problem{Kind: ProblemStorageSetup, Level: LevelError, Title: title, Action: action, Fix: "/cloud"}
	}
	switch e.Code {
	case "InvalidAccessKeyId", "SignatureDoesNotMatch", "AccessDenied", "ExpiredToken", "InvalidToken":
		return setup("Your storage keys were rejected",
			"Check the access key, secret key and permissions on the Cloud account tab.")
	case "NoSuchBucket":
		return setup("That bucket wasn't found", "Check the bucket name and endpoint on the Cloud account tab.")
	case "PermanentRedirect", "AuthorizationHeaderMalformed":
		return setup("The bucket is in a different region", "Check the region and endpoint on the Cloud account tab.")
	case "QuotaExceeded", "StorageQuotaExceeded", "MaxStorageExceeded", "InsufficientStorage":
		return quotaProblem()
	case "SlowDown", "RequestLimitExceeded", "Throttling", "ThrottlingException":
		return busyProblem()
	}
	if e.Status == 507 {
		return quotaProblem()
	}
	if e.Status >= 500 {
		return explainHTTP(&httpx.StatusError{Code: e.Status})
	}
	return Problem{Kind: ProblemUnknown, Level: LevelError,
		Title:  "The storage service rejected a request",
		Action: "Check your storage settings on the Cloud account tab, then try Force sync.",
		Fix:    "/cloud"}
}

func quotaProblem() Problem {
	return Problem{Kind: ProblemQuota, Level: LevelError,
		Title:  "Your cloud storage is full",
		Action: "Free up space in your cloud account (or buy more), then sync again."}
}

func busyProblem() Problem {
	return Problem{Kind: ProblemBusy, Level: LevelError,
		Title:  "The cloud service is asking WorldHop to slow down",
		Action: "No action needed. WorldHop will try again shortly."}
}

func isNetwork(err error) bool {
	var ne net.Error
	var ue *url.Error
	var de *net.DNSError
	return errors.As(err, &ne) || errors.As(err, &ue) || errors.As(err, &de) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED)
}

func isDiskFull(err error) bool {
	var en syscall.Errno
	if !errors.As(err, &en) {
		return false
	}
	if en == syscall.ENOSPC {
		return true
	}
	// Windows: ERROR_HANDLE_DISK_FULL (39) and ERROR_DISK_FULL (112).
	return runtime.GOOS == "windows" && (en == 39 || en == 112)
}
