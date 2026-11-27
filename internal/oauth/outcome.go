// outcome.go: the ways a sign-in can end, and the plain-language wording the browser
// page shows for each. The Go side only classifies; the page template does the drawing.

package oauth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Kind says how an authorization ended. It picks the wording of the page the
// browser shows and nothing else; callers still get an ordinary error.
type Kind string

const (
	KindWorking     Kind = ""            // not finished yet
	KindSuccess     Kind = "success"     // connected and saved
	KindDenied      Kind = "denied"      // the user declined on the consent screen
	KindPermission  Kind = "permission"  // consent given, but not every permission ticked
	KindUnavailable Kind = "unavailable" // the provider is having trouble
	KindBlocked     Kind = "blocked"     // an administrator or policy refuses this app
	KindSetup       Kind = "setup"       // the app's own sign-in settings are wrong
	KindProvider    Kind = "provider"    // any other error the provider reported
	KindIncomplete  Kind = "incomplete"  // the redirect carried no code
	KindStale       Kind = "stale"       // a redirect that is not part of this attempt
	KindExpired     Kind = "expired"     // the code was already used or has expired
	KindNetwork     Kind = "network"     // could not reach the provider to finish
	KindTimeout     Kind = "timeout"     // took too long
	KindExchange    Kind = "exchange"    // the provider refused the token request
	KindStorage     Kind = "storage"     // the credentials could not be saved
	KindCancelled   Kind = "cancelled"   // abandoned from inside WorldHop
)

// Outcome is what the browser page shows. State is "working", "success" or
// "error"; the text is already worded for the person reading it.
type Outcome struct {
	State   string `json:"state"`
	Kind    Kind   `json:"kind,omitempty"`
	Title   string `json:"title"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// flowError is a failure that already knows which page to show.
type flowError struct {
	kind   Kind
	detail string
	err    error
}

// Error returns the underlying error's text.
func (e *flowError) Error() string { return e.err.Error() }

// Unwrap returns the underlying error.
func (e *flowError) Unwrap() error { return e.err }

// fail builds a flowError of a given kind with a formatted message and optional
// technical detail.
func fail(kind Kind, detail string, format string, a ...any) error {
	return &flowError{kind: kind, detail: detail, err: fmt.Errorf(format, a...)}
}

// kindOf finds the page a failed flow should show.
func kindOf(err error) (Kind, string) {
	var fe *flowError
	switch {
	case errors.As(err, &fe):
		return fe.kind, fe.detail
	case errors.Is(err, ErrReauth):
		return KindExpired, ""
	case errors.Is(err, context.Canceled):
		return KindCancelled, ""
	case errors.Is(err, context.DeadlineExceeded):
		return KindTimeout, ""
	}
	var ne net.Error
	var ue *url.Error
	if errors.As(err, &ne) || errors.As(err, &ue) {
		return KindNetwork, ""
	}
	return KindExchange, err.Error()
}

// providerError classifies the `error` parameter of a redirect (RFC 6749 4.1.2.1).
func providerError(code, description string) error {
	kind := KindProvider
	switch code {
	case "access_denied":
		kind = KindDenied
	case "temporarily_unavailable", "server_error":
		kind = KindUnavailable
	case "admin_policy_enforced", "org_internal", "disabled_client", "access_blocked":
		kind = KindBlocked
	case "invalid_client", "unauthorized_client", "invalid_request", "invalid_scope",
		"unsupported_response_type", "redirect_uri_mismatch":
		kind = KindSetup
	}
	detail := code
	if description != "" {
		detail = code + ": " + description
	}
	msg := fmt.Sprintf("oauth: provider returned %q", code)
	if description != "" {
		msg += " (" + description + ")"
	}
	return &flowError{kind: kind, detail: detail, err: errors.New(msg)}
}

// missingScopes lists the requested scopes a redirect's `scope` parameter
// leaves out. A redirect with no `scope` parameter is taken as complete: not
// every provider sends one.
func missingScopes(want []string, granted string) []string {
	if granted == "" || len(want) == 0 {
		return nil
	}
	have := map[string]bool{}
	for _, s := range strings.Fields(granted) {
		have[s] = true
	}
	var out []string
	for _, s := range want {
		if !have[s] {
			out = append(out, s)
		}
	}
	return out
}

const maxDetail = 300

// clip collapses whitespace and shortens a detail string so a long provider message
// can't blow up the page.
func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxDetail {
		s = s[:maxDetail] + "…"
	}
	return s
}

// describe words the page for a kind. provider is the display name ("Google
// Drive"); detail is technical text for the "details" line, empty for none.
func describe(kind Kind, provider, detail string) Outcome {
	if provider == "" {
		provider = "your cloud account"
	}
	o := Outcome{State: "error", Kind: kind, Detail: clip(detail)}
	switch kind {
	case KindWorking:
		o.State = "working"
		o.Title = "Finishing sign-in…"
		o.Message = "WorldHop is completing the connection to " + provider + ". This only takes a moment."
	case KindSuccess:
		o.State = "success"
		o.Title = provider + " is connected"
		o.Message = "WorldHop can now sync your worlds through " + provider + "."
		o.Hint = "You can close this tab and return to WorldHop."
	case KindDenied:
		o.Title = "Connection cancelled"
		o.Message = "You chose not to give WorldHop access to " + provider + ", so nothing was connected."
		o.Hint = "Go back to WorldHop and click Connect to try again."
	case KindPermission:
		o.Title = "A permission is missing"
		o.Message = provider + " did not grant everything WorldHop needs to keep its files there."
		o.Hint = "Click Connect again and, on the consent screen, leave every permission ticked."
	case KindUnavailable:
		o.Title = provider + " is having trouble"
		o.Message = "The sign-in service reported a temporary problem."
		o.Hint = "Wait a few minutes, then click Connect in WorldHop to try again."
	case KindBlocked:
		o.Title = "Access is blocked"
		o.Message = "Your account or its administrator does not allow WorldHop to connect."
		o.Hint = "Try a personal account, or ask the administrator to allow the app."
	case KindSetup:
		o.Title = "Sign-in is not set up correctly"
		o.Message = "This build of WorldHop is not registered correctly with " + provider + "."
		o.Hint = "Install the latest release from the download page. If it still happens, report it with the details below."
	case KindProvider:
		o.Title = "Sign-in did not complete"
		o.Message = provider + " reported a problem and nothing was connected."
		o.Hint = "Go back to WorldHop and click Connect to try again."
	case KindIncomplete:
		o.Title = "Sign-in did not finish"
		o.Message = "The reply from " + provider + " was incomplete, so nothing was connected."
		o.Hint = "Go back to WorldHop and click Connect to try again."
	case KindStale:
		o.Title = "This sign-in link is not valid"
		o.Message = "It may belong to an earlier attempt, or WorldHop may have been restarted since."
		o.Hint = "Go back to WorldHop and click Connect to start again."
	case KindExpired:
		o.Title = "This sign-in has expired"
		o.Message = provider + " says this sign-in was already used or is too old."
		o.Hint = "Go back to WorldHop and click Connect to start again."
	case KindNetwork:
		o.Title = "Could not reach " + provider
		o.Message = "You signed in, but WorldHop could not finish the connection over the network."
		o.Hint = "Check your internet connection, then click Connect in WorldHop to try again."
	case KindTimeout:
		o.Title = "That took too long"
		o.Message = "The connection to " + provider + " was not finished in time."
		o.Hint = "Go back to WorldHop and click Connect to try again."
	case KindExchange:
		o.Title = "Could not finish connecting"
		o.Message = provider + " accepted your sign-in, but refused to hand WorldHop access."
		o.Hint = "Go back to WorldHop and click Connect to try again."
	case KindStorage:
		o.Title = "Could not save your sign-in"
		o.Message = "You signed in, but WorldHop could not store the credentials in your system keychain."
		o.Hint = "Unlock your keychain (on Linux, make sure GNOME Keyring or KWallet is running), then click Connect again."
	case KindCancelled:
		o.Title = "Cancelled in WorldHop"
		o.Message = "This connection attempt was cancelled or replaced from inside WorldHop."
		o.Hint = "Start again from the Cloud account tab if you still want to connect."
	default:
		o.Title = "Could not connect"
		o.Message = "Something went wrong and nothing was connected."
		o.Hint = "Go back to WorldHop and click Connect to try again."
	}
	if kind == KindDenied || kind == KindStale || kind == KindCancelled || kind == KindExpired {
		o.Detail = "" // nothing technical to add
	}
	return o
}
