package app

// notification_uri.go encodes and decodes the `projmux://` URI understood by
// the `projmux internal focus --uri` entrypoint.
//
// COMPATIBILITY ONLY as of 0.11.0. Desktop notification delivery is now a
// two-state model (`off` / `notify`): Toasts carry no click URI and projmux
// registers no `projmux://` protocol handler, so nothing in the product
// produces such a URI any more. The parser and the `--uri` flag are retained
// so a handler wired by an earlier version (or by the user directly) keeps
// working; `buildFocusURI` is retained as the canonical round-trip
// counterpart that keeps the parser honest in tests. Do not wire either of
// them to a new producer.
//
// The URI shape is:
//
//	projmux://focus?pane_id=<%paneID>&socket=<socketPath>&source=toast
//
// `pane_id` is the tmux pane id (e.g. `%8`) — the same identifier the Toast
// already carries in its `Tag`. `socket` is the tmux socket path the
// notification was produced from (we read `#{socket_path}` at notify time
// so the click round-trips back to the right server even when the user runs
// multiple tmux servers). `source` defaults to `toast` and is surfaced in
// focus telemetry so the click path is distinguishable from a status-bar or
// notify-sidebar click.
//
// Both encodings are URL-encoded once; the toast XML attribute layer then
// xml-escapes the result. The two encodings compose without ambiguity (URL
// reserved chars and XML reserved chars do not overlap on the producer
// side after `&` → `%26`).

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const (
	focusURIHost      = "focus"
	focusURIParamPane = "pane_id"
	focusURIParamSock = "socket"
	focusURIParamSrc  = "source"
	focusURISourceDef = "toast"
)

// buildFocusURI assembles a `projmux://focus?...` URI from a tmux pane id
// and a tmux socket path. `paneID` is required; an empty pane id returns an
// empty string. `socket` is optional — the receiving `projmux internal focus --uri`
// falls back to $TMUX when the query param is absent.
//
// No production call site remains: this is the round-trip counterpart of the
// retained compatibility parser (see the package comment above), which is why
// it is listed in .deadcode-allowlist.txt.
func buildFocusURI(paneID, socket string) string {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return ""
	}
	values := url.Values{}
	values.Set(focusURIParamPane, paneID)
	if s := strings.TrimSpace(socket); s != "" {
		values.Set(focusURIParamSock, s)
	}
	values.Set(focusURIParamSrc, focusURISourceDef)
	// url.Values.Encode sorts keys, which keeps the output stable for tests.
	return desktopURIScheme + "://" + focusURIHost + "?" + values.Encode()
}

// focusURI is the parsed shape of a `projmux://focus?...` URI.
type focusURI struct {
	PaneID string
	Socket string
	Source string
}

// parseFocusURI inverts buildFocusURI. Unknown query parameters are ignored
// so future extensions stay backward-compatible. Source defaults to
// `toast` when the parameter is absent (the click invariably comes from a
// Toast in the WSL scope shipping today, and the explicit default makes
// telemetry stable when older Toast XML is encountered).
func parseFocusURI(raw string) (focusURI, error) {
	if strings.TrimSpace(raw) == "" {
		return focusURI{}, errors.New("focus uri: empty")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return focusURI{}, fmt.Errorf("focus uri: parse %q: %w", raw, err)
	}
	if parsed.Scheme != desktopURIScheme {
		return focusURI{}, fmt.Errorf("focus uri: scheme %q is not %q", parsed.Scheme, desktopURIScheme)
	}
	// `projmux://focus?...` parses with Host=="focus". Some shells may
	// pass `projmux:focus?...` (opaque form); accept both. RawPath /
	// Opaque cover the corner case where Windows hands us the URL with
	// extra encoding around the host segment.
	host := strings.ToLower(parsed.Host)
	if host == "" {
		// Opaque form: `projmux:focus?...` ends up in Opaque without `//`.
		opaque := parsed.Opaque
		if i := strings.Index(opaque, "?"); i >= 0 {
			opaque = opaque[:i]
		}
		host = strings.ToLower(strings.Trim(opaque, "/"))
	}
	if host != focusURIHost {
		return focusURI{}, fmt.Errorf("focus uri: host %q is not %q", host, focusURIHost)
	}

	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return focusURI{}, fmt.Errorf("focus uri: query %q: %w", parsed.RawQuery, err)
	}
	pane := strings.TrimSpace(values.Get(focusURIParamPane))
	if pane == "" {
		return focusURI{}, errors.New("focus uri: missing pane_id")
	}
	socket := strings.TrimSpace(values.Get(focusURIParamSock))
	source := strings.TrimSpace(values.Get(focusURIParamSrc))
	if source == "" {
		source = focusURISourceDef
	}
	return focusURI{PaneID: pane, Socket: socket, Source: source}, nil
}
