package claude

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Fixture credential values. They are obviously fake and never leave the temp
// directory of the test that writes them.
const (
	failureFixtureAccess        = "fake-access-token-for-test"
	failureFixtureRefresh       = "fake-refresh-token-for-test"
	failureFixtureRotatedAccess = "fake-rotated-access-token-for-test"
	failureFixtureUsageURL      = "http://claude-usage.invalid/usage"
	failureFixtureRefreshURL    = "http://claude-usage.invalid/refresh"
)

var allFailureClasses = []error{
	ErrCredentialsUnavailable, ErrCredentialsTokenEmpty, ErrAuthRejected,
	ErrRateLimited, ErrHTTPStatus, ErrNetwork, ErrResponseInvalid,
}

type failureRoundTrip func(*http.Request) (*http.Response, error)

func (f failureRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type failureBody struct{ err error }

func (b failureBody) Read([]byte) (int, error) { return 0, b.err }

func (failureBody) Close() error { return nil }

func failureResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

// failureRoutes answers the usage endpoint by the bearer token and the refresh
// endpoint with one fixed response. A nil entry fails the test.
type failureRoutes struct {
	usageStale     func() (*http.Response, error)
	usageRefreshed func() (*http.Response, error)
	refresh        func() (*http.Response, error)
}

func (routes failureRoutes) transport(t *testing.T) http.RoundTripper {
	return failureRoundTrip(func(r *http.Request) (*http.Response, error) {
		var handler func() (*http.Response, error)
		switch {
		case r.URL.Path == "/refresh":
			handler = routes.refresh
		case r.Header.Get("Authorization") == "Bearer "+failureFixtureRotatedAccess:
			handler = routes.usageRefreshed
		default:
			handler = routes.usageStale
		}
		if handler == nil {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			return failureResponse(http.StatusTeapot, ""), nil
		}
		return handler()
	})
}

func fixedFailure(status int, body string) func() (*http.Response, error) {
	return func() (*http.Response, error) { return failureResponse(status, body), nil }
}

const rotatedRefreshBody = `{"access_token":"` + failureFixtureRotatedAccess + `","refresh_token":"fake-rotated-refresh-token-for-test","expires_in":3600}`

// TestCollectFailureClassesKeepMessagesByteIdentical pins every failing return
// of Collect: it carries exactly one failure class, its Error() is the exact
// message the adapter returned before classes existed, and a wrapped cause is
// still reachable through errors.Is / errors.As.
func TestCollectFailureClassesKeepMessagesByteIdentical(t *testing.T) {
	t.Parallel()

	transportCause := errors.New("fixture transport refused")
	bodyCause := errors.New("fixture body interrupted")

	type fixture struct {
		credsPath  string
		usageURL   string
		client     *http.Client
		wantString string
		wantCause  func(t *testing.T, err error)
	}
	validCreds := func(t *testing.T, dir, access, refresh string) string {
		t.Helper()
		path := filepath.Join(dir, ".credentials.json")
		writeCreds(t, path, access, refresh)
		return path
	}
	client := func(t *testing.T, routes failureRoutes) *http.Client {
		return &http.Client{Transport: routes.transport(t)}
	}

	tests := []struct {
		name  string
		class error
		setup func(t *testing.T, dir string) fixture
	}{
		{"credentials path unresolved", ErrCredentialsUnavailable, func(t *testing.T, dir string) fixture {
			return fixture{wantString: "claude: no credentials path resolved"}
		}},
		{"credentials file missing", ErrCredentialsUnavailable, func(t *testing.T, dir string) fixture {
			path := filepath.Join(dir, "missing.json")
			return fixture{credsPath: path, wantString: "claude: credentials not found at " + path}
		}},
		{"credentials unreadable", ErrCredentialsUnavailable, func(t *testing.T, dir string) fixture {
			// A directory in place of the file fails the read without
			// depending on file permissions.
			_, readErr := os.ReadFile(dir)
			if readErr == nil {
				t.Fatal("reading a directory unexpectedly succeeded")
			}
			return fixture{
				credsPath:  dir,
				wantString: "claude: read credentials: " + readErr.Error(),
				wantCause: func(t *testing.T, err error) {
					var pathErr *fs.PathError
					if !errors.As(err, &pathErr) {
						t.Fatalf("read failure lost its *fs.PathError cause: %v", err)
					}
				},
			}
		}},
		{"credentials unparseable", ErrCredentialsUnavailable, func(t *testing.T, dir string) fixture {
			path := filepath.Join(dir, ".credentials.json")
			data := []byte(`{"claudeAiOauth": {"accessToken": "` + failureFixtureAccess + `"`)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			var probe credentials
			parseErr := json.Unmarshal(data, &probe)
			return fixture{
				credsPath:  path,
				wantString: "claude: parse credentials: " + parseErr.Error(),
				wantCause: func(t *testing.T, err error) {
					var syntaxErr *json.SyntaxError
					if !errors.As(err, &syntaxErr) {
						t.Fatalf("parse failure lost its *json.SyntaxError cause: %v", err)
					}
				},
			}
		}},
		{"access token empty", ErrCredentialsTokenEmpty, func(t *testing.T, dir string) fixture {
			return fixture{credsPath: validCreds(t, dir, "", failureFixtureRefresh), wantString: "claude: empty access token"}
		}},
		{"401 without refresh token", ErrAuthRejected, func(t *testing.T, dir string) fixture {
			return fixture{
				credsPath:  validCreds(t, dir, failureFixtureAccess, ""),
				client:     client(t, failureRoutes{usageStale: fixedFailure(http.StatusUnauthorized, "")}),
				wantString: "claude: 401 and no refresh token available",
			}
		}},
		{"401 refresh rejected", ErrAuthRejected, func(t *testing.T, dir string) fixture {
			return fixture{
				credsPath: validCreds(t, dir, failureFixtureAccess, failureFixtureRefresh),
				client: client(t, failureRoutes{
					usageStale: fixedFailure(http.StatusUnauthorized, ""),
					refresh:    fixedFailure(http.StatusBadRequest, `{"error":"invalid_grant"}`),
				}),
				wantString: "claude: token refresh failed: refresh status 400",
			}
		}},
		{"401 refresh without access token", ErrAuthRejected, func(t *testing.T, dir string) fixture {
			return fixture{
				credsPath: validCreds(t, dir, failureFixtureAccess, failureFixtureRefresh),
				client: client(t, failureRoutes{
					usageStale: fixedFailure(http.StatusUnauthorized, ""),
					refresh:    fixedFailure(http.StatusOK, `{"expires_in":3600}`),
				}),
				wantString: "claude: token refresh failed: refresh response missing access_token",
			}
		}},
		{"401 refresh transport failure", ErrAuthRejected, func(t *testing.T, dir string) fixture {
			c := client(t, failureRoutes{
				usageStale: fixedFailure(http.StatusUnauthorized, ""),
				refresh:    func() (*http.Response, error) { return nil, transportCause },
			})
			return fixture{
				credsPath:  validCreds(t, dir, failureFixtureAccess, failureFixtureRefresh),
				client:     c,
				wantString: "claude: token refresh failed: POST refresh: " + clientDoError(t, c, http.MethodPost, failureFixtureRefreshURL).Error(),
				wantCause: func(t *testing.T, err error) {
					if !errors.Is(err, transportCause) {
						t.Fatalf("refresh failure lost its transport cause: %v", err)
					}
				},
			}
		}},
		{"401 again after successful refresh", ErrAuthRejected, func(t *testing.T, dir string) fixture {
			return fixture{
				credsPath: validCreds(t, dir, failureFixtureAccess, failureFixtureRefresh),
				client: client(t, failureRoutes{
					usageStale:     fixedFailure(http.StatusUnauthorized, ""),
					refresh:        fixedFailure(http.StatusOK, rotatedRefreshBody),
					usageRefreshed: fixedFailure(http.StatusUnauthorized, ""),
				}),
				wantString: "claude: usage endpoint returned status 401",
			}
		}},
		{"429", ErrRateLimited, func(t *testing.T, dir string) fixture {
			return fixture{
				credsPath:  validCreds(t, dir, failureFixtureAccess, failureFixtureRefresh),
				client:     client(t, failureRoutes{usageStale: fixedFailure(http.StatusTooManyRequests, "")}),
				wantString: "claude: usage endpoint returned status 429 (backing off)",
			}
		}},
		{"429 after successful refresh", ErrRateLimited, func(t *testing.T, dir string) fixture {
			return fixture{
				credsPath: validCreds(t, dir, failureFixtureAccess, failureFixtureRefresh),
				client: client(t, failureRoutes{
					usageStale:     fixedFailure(http.StatusUnauthorized, ""),
					refresh:        fixedFailure(http.StatusOK, rotatedRefreshBody),
					usageRefreshed: fixedFailure(http.StatusTooManyRequests, ""),
				}),
				wantString: "claude: usage endpoint returned status 429 (backing off)",
			}
		}},
		{"500", ErrHTTPStatus, func(t *testing.T, dir string) fixture {
			return fixture{
				credsPath:  validCreds(t, dir, failureFixtureAccess, failureFixtureRefresh),
				client:     client(t, failureRoutes{usageStale: fixedFailure(http.StatusInternalServerError, "")}),
				wantString: "claude: usage endpoint returned status 500",
			}
		}},
		{"403", ErrHTTPStatus, func(t *testing.T, dir string) fixture {
			return fixture{
				credsPath:  validCreds(t, dir, failureFixtureAccess, failureFixtureRefresh),
				client:     client(t, failureRoutes{usageStale: fixedFailure(http.StatusForbidden, "")}),
				wantString: "claude: usage endpoint returned status 403",
			}
		}},
		{"500 after successful refresh", ErrHTTPStatus, func(t *testing.T, dir string) fixture {
			return fixture{
				credsPath: validCreds(t, dir, failureFixtureAccess, failureFixtureRefresh),
				client: client(t, failureRoutes{
					usageStale:     fixedFailure(http.StatusUnauthorized, ""),
					refresh:        fixedFailure(http.StatusOK, rotatedRefreshBody),
					usageRefreshed: fixedFailure(http.StatusInternalServerError, ""),
				}),
				wantString: "claude: usage endpoint returned status 500",
			}
		}},
		{"build request", ErrNetwork, func(t *testing.T, dir string) fixture {
			const badURL = "://missing-scheme/usage"
			_, buildErr := http.NewRequestWithContext(context.Background(), http.MethodGet, badURL, nil)
			if buildErr == nil {
				t.Fatal("building a request for a scheme-less URL unexpectedly succeeded")
			}
			return fixture{
				credsPath:  validCreds(t, dir, failureFixtureAccess, failureFixtureRefresh),
				usageURL:   badURL,
				wantString: "claude: build request: " + buildErr.Error(),
			}
		}},
		{"GET usage transport failure", ErrNetwork, func(t *testing.T, dir string) fixture {
			c := client(t, failureRoutes{usageStale: func() (*http.Response, error) { return nil, transportCause }})
			return fixture{
				credsPath:  validCreds(t, dir, failureFixtureAccess, failureFixtureRefresh),
				client:     c,
				wantString: "claude: GET usage: " + clientDoError(t, c, http.MethodGet, failureFixtureUsageURL).Error(),
				wantCause: func(t *testing.T, err error) {
					if !errors.Is(err, transportCause) {
						t.Fatalf("GET failure lost its transport cause: %v", err)
					}
				},
			}
		}},
		{"GET usage transport failure after successful refresh", ErrNetwork, func(t *testing.T, dir string) fixture {
			routes := failureRoutes{
				usageStale:     fixedFailure(http.StatusUnauthorized, ""),
				refresh:        fixedFailure(http.StatusOK, rotatedRefreshBody),
				usageRefreshed: func() (*http.Response, error) { return nil, transportCause },
			}
			c := client(t, routes)
			probe := &http.Client{Transport: failureRoutes{usageStale: routes.usageRefreshed}.transport(t)}
			return fixture{
				credsPath:  validCreds(t, dir, failureFixtureAccess, failureFixtureRefresh),
				client:     c,
				wantString: "claude: GET usage: " + clientDoError(t, probe, http.MethodGet, failureFixtureUsageURL).Error(),
				wantCause: func(t *testing.T, err error) {
					if !errors.Is(err, transportCause) {
						t.Fatalf("GET failure lost its transport cause: %v", err)
					}
				},
			}
		}},
		{"read body", ErrNetwork, func(t *testing.T, dir string) fixture {
			return fixture{
				credsPath: validCreds(t, dir, failureFixtureAccess, failureFixtureRefresh),
				client: client(t, failureRoutes{usageStale: func() (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: failureBody{err: bodyCause}}, nil
				}}),
				wantString: "claude: read body: " + bodyCause.Error(),
				wantCause: func(t *testing.T, err error) {
					if !errors.Is(err, bodyCause) {
						t.Fatalf("read failure lost its body cause: %v", err)
					}
				},
			}
		}},
		{"usage response not JSON", ErrResponseInvalid, func(t *testing.T, dir string) fixture {
			return fixture{
				credsPath:  validCreds(t, dir, failureFixtureAccess, failureFixtureRefresh),
				client:     client(t, failureRoutes{usageStale: fixedFailure(http.StatusOK, `{"five_hour":`)}),
				wantString: "claude: parse usage response: invalid typed payload",
			}
		}},
		{"usage limits not an array", ErrResponseInvalid, func(t *testing.T, dir string) fixture {
			var rows []json.RawMessage
			shapeErr := json.Unmarshal([]byte(`{"kind":"x"}`), &rows)
			return fixture{
				credsPath:  validCreds(t, dir, failureFixtureAccess, failureFixtureRefresh),
				client:     client(t, failureRoutes{usageStale: fixedFailure(http.StatusOK, `{"limits":{"kind":"x"}}`)}),
				wantString: "claude: parse usage response limits: expected array: " + shapeErr.Error(),
			}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			f := test.setup(t, t.TempDir())
			usageURL := failureFixtureUsageURL
			if f.usageURL != "" {
				usageURL = f.usageURL
			}
			httpClient := f.client
			if httpClient == nil {
				httpClient = &http.Client{Transport: failureRoundTrip(func(r *http.Request) (*http.Response, error) {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
					return failureResponse(http.StatusTeapot, ""), nil
				})}
			}
			a := NewWithConfig(f.credsPath, usageURL, failureFixtureRefreshURL, httpClient)
			a.now = func() time.Time { return time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC) }

			snaps, err := a.Collect(context.Background())
			if err == nil {
				t.Fatalf("Collect() error = nil, snaps = %+v", snaps)
			}
			if snaps != nil {
				t.Fatalf("snaps = %+v, want nil on a whole failure", snaps)
			}
			if got := err.Error(); got != f.wantString {
				t.Fatalf("Error() = %q, want byte-identical %q", got, f.wantString)
			}
			for _, class := range allFailureClasses {
				if got, want := errors.Is(err, class), class == test.class; got != want {
					t.Fatalf("errors.Is(err, %v) = %v, want %v", class, got, want)
				}
			}
			if f.wantCause != nil {
				f.wantCause(t, err)
			}
		})
	}
}

// TestCollectBackoffShortCircuitAndPartialResultCarryNoFailureClass keeps the
// two non-failures out of the failure classes: a deferred call during backoff
// and a partial result with skipped rows.
func TestCollectBackoffShortCircuitAndPartialResultCarryNoFailureClass(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, ".credentials.json")
	writeCreds(t, path, failureFixtureAccess, failureFixtureRefresh)
	body := `{"five_hour":{"utilization":3,"resets_at":"2026-05-06T17:00:00Z"},"limits":[{"kind":"x"}]}`
	a := NewWithConfig(path, failureFixtureUsageURL, failureFixtureRefreshURL,
		&http.Client{Transport: failureRoutes{usageStale: fixedFailure(http.StatusOK, body)}.transport(t)})
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	snaps, err := a.Collect(context.Background())
	if len(snaps) == 0 || err == nil {
		t.Fatalf("partial collect = %+v / %v, want rows and a row-skip warning", snaps, err)
	}
	for _, class := range allFailureClasses {
		if errors.Is(err, class) {
			t.Fatalf("partial warning carries failure class %v", class)
		}
	}

	a.backoffUntil = now.Add(time.Minute)
	if snaps, err := a.Collect(context.Background()); snaps != nil || err != nil {
		t.Fatalf("backoff short-circuit = %+v / %v, want nil / nil", snaps, err)
	}
}

// clientDoError reproduces the error the client returns for one request, so a
// test can pin the adapter's message without restating net/http's formatting.
func clientDoError(t *testing.T, c *http.Client, method, target string) error {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("%s %s unexpectedly succeeded", method, target)
	}
	return err
}
