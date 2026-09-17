package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// The loopback guard keeps other web sites out, but not other local
// programs: any process on the machine can connect to a loopback port. The
// TCP listener therefore also requires the token `projmux web` generates at
// start and prints once, in the URL it tells the operator to open.
//
// The unix socket does not check it. Its 0600 file mode in an owner-only
// directory already limits it to the operator, which is what the token proves
// on TCP.

// tokenBytes is the size of the random value behind a start token.
const tokenBytes = 32

// tokenQuery is the query parameter the startup URL carries the token in.
const tokenQuery = "token"

// NewToken returns a fresh start token: 32 bytes from crypto/rand, encoded as
// unpadded base64url so it survives a URL unescaped.
func NewToken() (string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("web: generate start token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// tokenCookieName is per port. Browsers do not separate cookies by port, so
// two servers on one loopback host would otherwise overwrite each other's
// cookie and lock the operator out of whichever started first.
func tokenCookieName(port string) string {
	return "projmux_web_token_" + port
}

// requireToken admits a request that carries the start token as the cookie
// this server set or as an `Authorization: Bearer` header. A GET or HEAD that
// carries it as `?token=` instead gets the cookie and a redirect to `/`, so the
// token does not stay in the address bar or the history, and the page's own
// requests carry the cookie from then on.
//
// The redirect target is the constant `/`, never the request's path or query:
// the only URL `projmux web` prints is `/?token=<token>`, and a request-built
// target would turn `//evil.example/?token=<token>` into an off-site,
// protocol-relative redirect.
//
// The cookie is HttpOnly and SameSite=Strict but not Secure: the listener is
// plain http on loopback, and browsers that do not count http loopback as a
// secure context (WebKit) drop a Secure cookie there and lock the operator out.
//
// Nothing here logs or echoes a token: refusals log the path only.
func requireToken(port, token string, log *slog.Logger, next http.Handler) http.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	cookieName := tokenCookieName(port)
	valid := func(candidate string) bool {
		return candidate != "" && subtle.ConstantTimeCompare([]byte(candidate), []byte(token)) == 1
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && query.Has(tokenQuery) {
			if !valid(query.Get(tokenQuery)) {
				refuseToken(w, r, log)
				return
			}
			http.SetCookie(w, &http.Cookie{ // #nosec G124 -- plain-http loopback listener; Secure would make WebKit drop the cookie, HttpOnly and SameSite=Strict are set.
				Name:     cookieName,
				Value:    token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			})
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		if cookie, err := r.Cookie(cookieName); err == nil && valid(cookie.Value) {
			next.ServeHTTP(w, r)
			return
		}
		if bearer, ok := bearerToken(r.Header.Get("Authorization")); ok && valid(bearer) {
			next.ServeHTTP(w, r)
			return
		}
		refuseToken(w, r, log)
	})
}

func bearerToken(header string) (string, bool) {
	scheme, value, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	return strings.TrimSpace(value), true
}

func refuseToken(w http.ResponseWriter, r *http.Request, log *slog.Logger) {
	log.Debug("request refused", "method", r.Method, "path", r.URL.Path, "code", CodeUnauthorized)
	w.Header().Set("WWW-Authenticate", `Bearer realm="projmux web"`)
	writeError(w, NewError(http.StatusUnauthorized, CodeUnauthorized,
		"this listener needs the start token that `projmux web` printed; open the printed URL or send Authorization: Bearer"))
}
