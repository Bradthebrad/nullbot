package hfhub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// These classify the errors this package returns, for errors.Is. The text a
// user should see is the *Error's own message, not these.
var (
	// ErrTokenRequired: the repo is gated or private and no token was given.
	ErrTokenRequired = errors.New("a Hugging Face access token is needed")
	// ErrTokenInvalid: the Hub did not accept the token that was given.
	ErrTokenInvalid = errors.New("the Hugging Face access token was not accepted")
	// ErrAccessDenied: the token is valid but may not read this repo, for
	// example because the model's terms have not been accepted yet.
	ErrAccessDenied = errors.New("access to the model was refused")
	ErrNotFound     = errors.New("not found on Hugging Face")
	// ErrRateLimited: too many requests; see Error.RetryAfter.
	ErrRateLimited       = errors.New("rate limited by Hugging Face")
	ErrServer            = errors.New("server error")
	ErrBadResponse       = errors.New("unexpected response")
	ErrNetwork           = errors.New("network error")
	ErrInvalidArgument   = errors.New("invalid argument")
	ErrInvalidRepo       = errors.New("invalid model id")
	ErrInvalidRevision   = errors.New("invalid revision")
	ErrUnsafePath        = errors.New("unsafe file path")
	ErrFileExists        = errors.New("file already exists")
	ErrInsufficientSpace = errors.New("not enough disk space")
	ErrChecksumMismatch  = errors.New("checksum mismatch")
	ErrFileSystem        = errors.New("could not write the file")
	// ErrIncomplete: the transfer ended early; the .part file is kept so the
	// next Download resumes it.
	ErrIncomplete = errors.New("download incomplete")
)

// Error is what this package returns for anything a user can act on. Its
// message is a plain sentence for the user; Kind says what went wrong for
// code that wants to react (for example, asking for a token).
type Error struct {
	Kind       error
	StatusCode int           // HTTP status, when the error came from a response
	RetryAfter time.Duration // how long the Hub asked us to wait, for ErrRateLimited
	msg        string
	cause      error
}

func (e *Error) Error() string { return e.msg }

// Unwrap exposes both the kind and the underlying cause, so errors.Is works
// for ErrNetwork as well as for context.Canceled behind it.
func (e *Error) Unwrap() []error {
	if e.cause != nil {
		return []error{e.Kind, e.cause}
	}
	return []error{e.Kind}
}

func newError(kind error, format string, args ...any) *Error {
	return &Error{Kind: kind, msg: fmt.Sprintf(format, args...)}
}

// target names what a request was about, so errors can say it.
type target struct {
	repo, revision, path string
}

// responseError turns a failed HTTP response into a sentence the user can
// act on. It reads the Hub's X-Error-Code header, which tells a gated repo
// apart from a missing one. It never quotes the request URL: download URLs
// carry signed query strings, and nothing here echoes the token.
func (s *session) responseError(resp *http.Response, t target) error {
	defer drain(resp.Body)
	status := resp.StatusCode
	code := resp.Header.Get("X-Error-Code")
	fromHub := resp.Request == nil || sameOrigin(resp.Request.URL, s.base)
	hasToken := s.token != ""
	subject := t.repo
	if subject == "" {
		subject = "the request"
	}
	e := &Error{StatusCode: status}
	set := func(kind error, format string, args ...any) error {
		e.Kind, e.msg = kind, fmt.Sprintf(format, args...)
		return e
	}

	switch {
	case status == http.StatusTooManyRequests:
		e.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		wait := "a minute"
		if e.RetryAfter > 0 {
			wait = humanDuration(e.RetryAfter)
		}
		return set(ErrRateLimited, "Hugging Face is limiting how many requests can be made; wait %s and try again", wait)

	case status >= 500:
		return set(ErrServer, "Hugging Face is having problems right now (HTTP %d); try again in a few minutes", status)

	case !fromHub:
		// The CDN behind a redirect has no notion of tokens or gating; a
		// failure there is usually an expired signed link.
		return set(ErrBadResponse, "the download server answered HTTP %d for %s; try again", status, subject)

	case status == http.StatusUnauthorized:
		switch {
		case !hasToken && code == "RepoNotFound":
			return set(ErrNotFound, "%s was not found on Hugging Face; check the name, and if the model is private add a Hugging Face access token", subject)
		case !hasToken:
			return set(ErrTokenRequired, "%s is gated or private on Hugging Face, so downloading it needs a Hugging Face access token; add one and try again", subject)
		case code == "RepoNotFound":
			return set(ErrNotFound, "%s was not found on Hugging Face, or your access token cannot see it; check the name and the token", subject)
		case code == "GatedRepo":
			return set(ErrAccessDenied, "%s is gated; open %s while signed in, request access, then try again", subject, s.repoPage(t.repo))
		default:
			return set(ErrTokenInvalid, "Hugging Face did not accept the access token; check that it is correct and not expired")
		}

	case status == http.StatusForbidden:
		switch {
		case !hasToken:
			return set(ErrTokenRequired, "%s is gated or private on Hugging Face, so downloading it needs a Hugging Face access token; add one and try again", subject)
		case code == "GatedRepo":
			return set(ErrAccessDenied, "%s is gated and your account does not have access yet; open %s while signed in, accept the terms, then try again", subject, s.repoPage(t.repo))
		default:
			return set(ErrAccessDenied, "Hugging Face refused access to %s; check that your access token has read permission for it", subject)
		}

	case status == http.StatusNotFound:
		switch code {
		case "EntryNotFound":
			return set(ErrNotFound, "%s has no file %q at revision %q", subject, t.path, t.revision)
		case "RevisionNotFound":
			return set(ErrNotFound, "%s has no revision %q", subject, t.revision)
		}
		if !hasToken {
			return set(ErrNotFound, "%s was not found on Hugging Face; check the name, and if the model is private add a Hugging Face access token", subject)
		}
		return set(ErrNotFound, "%s was not found on Hugging Face, or your access token cannot see it; check the name and the token", subject)
	}
	return set(ErrBadResponse, "Hugging Face answered HTTP %d for %s, which was not expected; try again later", status, subject)
}

func (s *session) repoPage(repo string) string {
	return strings.TrimRight(s.base.String(), "/") + "/" + repo
}

// networkError reports a failed round trip without the URL in it: Go's
// *url.Error quotes the full URL, and a CDN URL holds a signed query string.
func networkError(ctx context.Context, what string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return stopped(what, ctxErr)
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return &Error{
		Kind:  ErrNetwork,
		msg:   fmt.Sprintf("could not reach Hugging Face for %s (%v); check the internet connection and try again", what, err),
		cause: err,
	}
}

// stopped describes a cancelled or timed-out operation and keeps the context
// error underneath, so callers can still test errors.Is(err, context.Canceled).
func stopped(what string, ctxErr error) error {
	verb := "was cancelled"
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		verb = "took too long and was stopped"
	}
	return &Error{Kind: ctxErr, msg: fmt.Sprintf("%s %s", what, verb)}
}

// parseRetryAfter reads a Retry-After value, which is either seconds or an
// HTTP date. It caps the wait at a day so a bad header cannot stall a UI.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	const maxWait = 24 * time.Hour
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs <= 0 {
			return 0
		}
		return min(time.Duration(secs)*time.Second, maxWait)
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return min(d.Round(time.Second), maxWait)
		}
	}
	return 0
}

func humanDuration(d time.Duration) string {
	switch {
	case d < 2*time.Second:
		return "a second"
	case d < 2*time.Minute:
		return fmt.Sprintf("%d seconds", int(d.Seconds()+0.5))
	case d < 2*time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()+0.5))
	}
	return fmt.Sprintf("%d hours", int(d.Hours()+0.5))
}

// drain reads a little of a body before closing it so the connection can be
// reused, without ever reading a large error page in full.
func drain(body io.ReadCloser) {
	_, _ = io.CopyN(io.Discard, body, 64<<10)
	_ = body.Close()
}
