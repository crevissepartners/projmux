package web

import (
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// A server on loopback is still reachable from every web page the operator has
// open. Two attacks follow from that, and both were real against the
// prototype this server replaces:
//
//   - A cross-site page posts a body with a "simple" content type. The browser
//     sends it without a preflight, and a handler that decodes JSON anyway runs
//     it.
//   - A page on an attacker's domain rebinds that domain to 127.0.0.1. Its
//     requests are then same-origin to the browser, and only the Host header
//     still says where they were aimed.
//
// guardLoopback refuses both before any handler runs, ahead of the start token
// check (requireToken), so a foreign page is refused as foreign whether or not
// the browser holds the cookie. Both are applied to the TCP listener only: a
// browser cannot reach the unix socket, whose file mode is the access control
// there.
func guardLoopback(port string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host, port) {
			writeError(w, NewError(http.StatusForbidden, CodeForbiddenOrigin, "request host is not this server"))
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r.Host) {
			writeError(w, NewError(http.StatusForbidden, CodeForbiddenOrigin, "request origin is not this server"))
			return
		}
		if needsJSON(r) && !jsonContentType(r.Header.Get("Content-Type")) && !uploadContentType(r) {
			writeError(w, NewError(http.StatusForbidden, CodeForbiddenOrigin, "a request that changes state must be application/json"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loopbackHost accepts the spellings a browser uses for this server and
// nothing else. The port has to match too, so a page served from another
// local port is not treated as this one.
func loopbackHost(hostport, port string) bool {
	host, gotPort, err := net.SplitHostPort(hostport)
	if err != nil || gotPort != port {
		return false
	}
	switch strings.ToLower(host) {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.Path != "" && u.Path != "/" {
		return false
	}
	return strings.EqualFold(u.Host, host)
}

// needsJSON reports whether a request must declare a JSON body. A bodiless
// DELETE is exempt: a cross-site page cannot send DELETE without a preflight,
// which this server never answers, and a dry run has nothing to send. A
// bodiless POST is not exempt, since a no-cors fetch can send one.
func needsJSON(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		return false
	case http.MethodDelete:
		return r.ContentLength != 0
	}
	return true
}

func jsonContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && mediaType == "application/json"
}
