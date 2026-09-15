package claude

import (
	"errors"
	"net/http"
)

// Failure classes for a whole Claude usage collection failure.
//
// Collect wraps every failing return in exactly one of these sentinels, so a
// caller can tell why nothing refreshed with errors.Is instead of reading the
// message. The wrapper keeps Error() byte-identical to the message the adapter
// has always returned (the PROJMUX_USAGE_DEBUG warning prints it) and keeps any
// wrapped cause reachable. The sentinel text itself is never rendered.
var (
	// ErrCredentialsUnavailable: no credentials path could be resolved, or the
	// credentials file is missing, unreadable, or not parseable.
	ErrCredentialsUnavailable = errors.New("claude usage failure: credentials unavailable")
	// ErrCredentialsTokenEmpty: the credentials parsed but carry no access
	// token.
	ErrCredentialsTokenEmpty = errors.New("claude usage failure: access token empty")
	// ErrAuthRejected: the usage endpoint answered 401 and the stored refresh
	// token could not restore access: there is no refresh token, the refresh
	// round-trip failed, or the refreshed token was rejected again.
	ErrAuthRejected = errors.New("claude usage failure: authentication rejected")
	// ErrRateLimited: the usage endpoint answered 429 and a backoff was
	// recorded.
	ErrRateLimited = errors.New("claude usage failure: rate limited")
	// ErrHTTPStatus: the usage endpoint answered any other non-200 status.
	ErrHTTPStatus = errors.New("claude usage failure: unexpected http status")
	// ErrNetwork: the usage request could not be built or sent, or its body
	// could not be read.
	ErrNetwork = errors.New("claude usage failure: network")
	// ErrResponseInvalid: the usage endpoint answered 200 with a body that
	// could not be parsed.
	ErrResponseInvalid = errors.New("claude usage failure: response invalid")
)

// classifiedError attaches one failure class to an error without changing its
// message or cutting its wrapped chain.
type classifiedError struct {
	class error
	err   error
}

func classify(class, err error) error {
	return &classifiedError{class: class, err: err}
}

func (e *classifiedError) Error() string { return e.err.Error() }

func (e *classifiedError) Unwrap() []error { return []error{e.err, e.class} }

// usageStatusFailure classifies the generic non-200 branch of Collect. A 401
// reaches that branch only on the retry after a successful token refresh,
// because the first 401 is handled by the refresh branch. The refreshed token
// was rejected as well, so the user has to sign in again: that is an
// authentication rejection, not an arbitrary upstream status.
func usageStatusFailure(status int, err error) error {
	if status == http.StatusUnauthorized {
		return classify(ErrAuthRejected, err)
	}
	return classify(ErrHTTPStatus, err)
}
