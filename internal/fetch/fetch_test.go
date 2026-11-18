package fetch_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sifty-works/sifty/internal/db"
	"github.com/sifty-works/sifty/internal/fetch"
)

// --- helpers ---

type fakeClock struct {
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

type sleepRecorder struct {
	pauses []time.Duration
}

func (s *sleepRecorder) Sleep(_ context.Context, d time.Duration) error {
	s.pauses = append(s.pauses, d)
	return nil
}

// testBlocks opens a fresh fixture database and returns a DomainBlocks backed by it.
func testBlocks(t *testing.T) *fetch.DomainBlocks {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"), db.Migrations)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return fetch.NewDomainBlocks(database)
}

// newTestFetcher builds a client wired with a loopback policy, fake clock,
// and sleep recorder. The httptest server address is not needed here since
// we pass the URL explicitly.
func newTestFetcher(bs *fetch.DomainBlocks, clk *fakeClock, sl *sleepRecorder) fetch.Fetcher {
	return fetch.New(fetch.Options{
		Blocks:  bs,
		Clock:   clk.Now,
		Sleep:   sl.Sleep,
		Rand:    rand.New(rand.NewSource(0)), //nolint:gosec
		Policy:  fetch.AllowLoopback,
		Timeout: 5 * time.Second,
	})
}

// newTestFetcherWithPolicy builds a client with a custom policy.
func newTestFetcherWithPolicy(t *testing.T, policy fetch.AddressPolicy) fetch.Fetcher {
	return fetch.New(fetch.Options{
		Blocks: testBlocks(t),
		Clock:  time.Now,
		Sleep: func(_ context.Context, _ time.Duration) error {
			return nil
		},
		Rand:    rand.New(rand.NewSource(0)), //nolint:gosec
		Policy:  policy,
		Timeout: 5 * time.Second,
	})
}

func okServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, body)
	}))
}

// --- address policy ---

func TestPublicOnly(t *testing.T) {
	cases := []struct {
		addr  string
		allow bool
	}{
		{"127.0.0.1", false}, {"::1", false}, {"127.0.0.2", false},
		{"10.0.0.1", false}, {"192.168.1.1", false}, {"172.16.0.1", false}, {"fd00::1", false},
		{"169.254.0.1", false}, {"fe80::1", false},
		{"224.0.0.1", false}, {"ff02::1", false},
		{"0.0.0.0", false}, {"::", false},
		{"::ffff:127.0.0.1", false}, {"::ffff:192.168.1.1", false},
		{"1.1.1.1", true}, {"8.8.8.8", true}, {"2001:db8::1", true},
	}
	for _, tc := range cases {
		ip := net.ParseIP(tc.addr)
		if got := fetch.PublicOnly(ip); got != tc.allow {
			t.Errorf("PublicOnly(%s) = %v, want %v", tc.addr, got, tc.allow)
		}
	}
}

// --- URL validation ---

func TestGet_refused_conditions(t *testing.T) {
	loopback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	}))
	defer loopback.Close()

	cases := []struct {
		name   string
		url    string
		policy fetch.AddressPolicy
	}{
		{"credentials in URL", "http://user:pass@example.com/", fetch.AllowLoopback},
		{"unsupported scheme", "ftp://example.com/", fetch.AllowLoopback},
		{"loopback address with PublicOnly policy", loopback.URL + "/", fetch.PublicOnly},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newTestFetcherWithPolicy(t, tc.policy)
			_, err := f.Get(context.Background(), tc.url, fetch.Opt{})
			if !errors.Is(err, fetch.ErrRefused) {
				t.Errorf("got %v, want ErrRefused", err)
			}
		})
	}
}

// --- response status codes ---

func TestGet_block_survives_restart_and_expires(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(403)
	}))
	defer srv.Close()
	clk := newFakeClock()
	blocks := testBlocks(t)
	ctx := context.Background()
	// First 403 blocks the domain and writes the row.
	newTestFetcher(blocks, clk, &sleepRecorder{}).Get(ctx, srv.URL+"/", fetch.Opt{}) //nolint:errcheck
	n := calls.Load()
	// A new fetcher on the same database (simulates a restart) refuses without a request.
	if _, err := newTestFetcher(blocks, clk, &sleepRecorder{}).Get(ctx, srv.URL+"/", fetch.Opt{}); !errors.Is(err, fetch.ErrBlocked) {
		t.Fatalf("new fetcher: want ErrBlocked, got %v", err)
	}
	if calls.Load() != n {
		t.Error("new fetcher made HTTP request during block")
	}
	// After 24 hours the block expires; the domain is requested again.
	clk.Advance(25 * time.Hour)
	newTestFetcher(blocks, clk, &sleepRecorder{}).Get(ctx, srv.URL+"/", fetch.Opt{}) //nolint:errcheck
	if calls.Load() == n {
		t.Error("after 24h, no request made; block should have expired")
	}
}

func TestGet_challenge_and_block_patterns(t *testing.T) {
	cases := []struct {
		name     string
		handler  http.HandlerFunc
		wantErr  error // nil means expect 200 content
		wantBody string
	}{
		{
			name:    "403 is blocked",
			handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) },
			wantErr: fetch.ErrBlocked,
		},
		{
			name: "cf-mitigated challenge header is blocked",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("cf-mitigated", "challenge")
				fmt.Fprint(w, "verify")
			},
			wantErr: fetch.ErrBlocked,
		},
		{
			name: "challenge-platform script on 503 is blocked",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(503)
				fmt.Fprint(w, `<html><body><script src="/cdn-cgi/challenge-platform/x"></script></body></html>`)
			},
			wantErr: fetch.ErrBlocked,
		},
		{
			name: "challenge-platform script on 200 is content",
			handler: func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `<html><body><script src="/cdn-cgi/challenge-platform/x"></script><p>jobs</p></body></html>`)
			},
			wantBody: "jobs",
		},
		{
			name: "Just a moment title is blocked",
			handler: func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, "<html><head><title>Just a moment...</title></head></html>")
			},
			wantErr: fetch.ErrBlocked,
		},
		{
			name: "reCAPTCHA form on 200 is content",
			handler: func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `<html><body><div class="g-recaptcha">captcha</div></body></html>`)
			},
			wantBody: "g-recaptcha",
		},
		{"404 not found", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }, fetch.ErrNotFound, ""},
		{"410 not found", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(410) }, fetch.ErrNotFound, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			f := newTestFetcher(testBlocks(t), newFakeClock(), &sleepRecorder{})
			resp, err := f.Get(context.Background(), srv.URL+"/", fetch.Opt{})
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("got %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil || resp.Status != 200 {
				t.Errorf("got err=%v status=%d, want 200 content", err, resp.Status)
			}
			if tc.wantBody != "" && !strings.Contains(string(resp.Body), tc.wantBody) {
				t.Errorf("body %q does not contain %q", resp.Body, tc.wantBody)
			}
		})
	}
}

// --- back-off ---

func TestGet_backoff_exponential_then_reset(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.Contains(r.URL.Path, "ok") {
			fmt.Fprint(w, "ok")
		} else {
			w.WriteHeader(503)
		}
	}))
	defer srv.Close()
	clk := newFakeClock()
	f := newTestFetcher(testBlocks(t), clk, &sleepRecorder{})
	ctx := context.Background()

	mustTransient := func(path string) {
		t.Helper()
		if _, err := f.Get(ctx, srv.URL+path, fetch.Opt{}); !errors.Is(err, fetch.ErrTransient) {
			t.Fatalf("%s: want ErrTransient", path)
		}
	}

	mustTransient("/fail") // first 503: back-off 30 s
	n := calls.Load()
	clk.Advance(10 * time.Second)
	mustTransient("/fail") // during back-off: no HTTP request
	if calls.Load() != n {
		t.Errorf("call during back-off made HTTP request")
	}
	clk.Advance(21 * time.Second) // back-off expired
	mustTransient("/fail")        // second 503: back-off 60 s
	clk.Advance(61 * time.Second)
	if _, err := f.Get(ctx, srv.URL+"/ok", fetch.Opt{}); err != nil {
		t.Fatalf("after success: %v", err)
	}
	n = calls.Load()
	clk.Advance(time.Second)
	f.Get(ctx, srv.URL+"/fail", fetch.Opt{}) //nolint:errcheck
	if calls.Load() == n {
		t.Errorf("after back-off reset, no request made")
	}
}

func TestGet_backoff_cap_at_one_hour(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv.Close()
	clk := newFakeClock()
	f := newTestFetcher(testBlocks(t), clk, &sleepRecorder{})
	ctx := context.Background()
	for _, step := range []time.Duration{
		30 * time.Second, 60 * time.Second, 2 * time.Minute,
		4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute, time.Hour,
	} {
		if _, err := f.Get(ctx, srv.URL+"/", fetch.Opt{}); !errors.Is(err, fetch.ErrTransient) {
			t.Fatalf("want ErrTransient")
		}
		clk.Advance(step + time.Second)
	}
	if _, err := f.Get(ctx, srv.URL+"/", fetch.Opt{}); !errors.Is(err, fetch.ErrTransient) {
		t.Fatalf("at cap: want ErrTransient")
	}
	clk.Advance(59 * time.Minute)
	if _, err := f.Get(ctx, srv.URL+"/", fetch.Opt{}); !errors.Is(err, fetch.ErrTransient) {
		t.Fatalf("within 1h: want ErrTransient")
	}
}

// --- spacing ---

func TestGet_spacing_within_domain(t *testing.T) {
	srv := okServer(t, "hi")
	defer srv.Close()

	clk := newFakeClock()
	sl := &sleepRecorder{}
	f := newTestFetcher(testBlocks(t), clk, sl)

	// First request: no sleep.
	_, err := f.Get(context.Background(), srv.URL+"/a", fetch.Opt{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sl.pauses) != 0 {
		t.Errorf("first request: unexpected sleep %v", sl.pauses)
	}

	// Second request to same domain: sleep of 3–6 s.
	_, err = f.Get(context.Background(), srv.URL+"/b", fetch.Opt{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sl.pauses) != 1 {
		t.Fatalf("second request: want 1 sleep, got %d", len(sl.pauses))
	}
	if sl.pauses[0] < 3*time.Second || sl.pauses[0] > 6*time.Second {
		t.Errorf("spacing = %v, want 3–6 s", sl.pauses[0])
	}
}

// --- body limit and timeout ---

func TestGet_body_over_8MB_is_transient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(200)
		chunk := strings.Repeat("x", 1024)
		for i := 0; i < 8193; i++ { // > 8 MB
			fmt.Fprint(w, chunk)
		}
	}))
	defer srv.Close()

	sl := &sleepRecorder{}
	f := newTestFetcher(testBlocks(t), newFakeClock(), sl)
	_, err := f.Get(context.Background(), srv.URL+"/", fetch.Opt{})
	if !errors.Is(err, fetch.ErrTransient) {
		t.Fatalf("body > 8MB: got %v, want ErrTransient", err)
	}
}

func TestGet_timeout_is_transient(t *testing.T) {
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-done // block until test finishes
	}))
	defer srv.Close()
	defer close(done)

	f := fetch.New(fetch.Options{
		Blocks: testBlocks(t),
		Clock:  time.Now,
		Sleep: func(_ context.Context, _ time.Duration) error {
			return nil
		},
		Rand:    rand.New(rand.NewSource(0)), //nolint:gosec
		Policy:  fetch.AllowLoopback,
		Timeout: 50 * time.Millisecond, // very short
	})
	_, err := f.Get(context.Background(), srv.URL+"/", fetch.Opt{})
	if !errors.Is(err, fetch.ErrTransient) {
		t.Fatalf("timeout: got %v, want ErrTransient", err)
	}
}

func TestGet_cancellation_does_not_trigger_backoff(t *testing.T) {
	srv := okServer(t, "ok")
	defer srv.Close()

	clk := newFakeClock()
	sl := &sleepRecorder{}
	f := newTestFetcher(testBlocks(t), clk, sl)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := f.Get(ctx, srv.URL+"/", fetch.Opt{})
	if err == nil || errors.Is(err, fetch.ErrTransient) {
		t.Fatalf("cancelled ctx: got %v, want context error (not ErrTransient)", err)
	}

	// Back-off must not have been recorded; second request goes straight through.
	_, err = f.Get(context.Background(), srv.URL+"/", fetch.Opt{})
	if errors.Is(err, fetch.ErrTransient) {
		t.Errorf("after cancellation, domain is in back-off")
	}
}

// --- offline ---

func TestGet_offline_returns_ErrOffline_no_request(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, "hello")
	}))
	defer srv.Close()

	f := fetch.New(fetch.Options{
		Blocks: testBlocks(t),
		Clock:  time.Now,
		Sleep: func(_ context.Context, _ time.Duration) error {
			return nil
		},
		Rand:    rand.New(rand.NewSource(0)), //nolint:gosec
		Policy:  fetch.AllowLoopback,
		Timeout: 5 * time.Second,
		Offline: true,
	})
	_, err := f.Get(context.Background(), srv.URL+"/", fetch.Opt{})
	if !errors.Is(err, fetch.ErrOffline) {
		t.Fatalf("offline: got %v, want ErrOffline", err)
	}
	if calls.Load() != 0 {
		t.Errorf("offline: made %d HTTP request(s), want 0", calls.Load())
	}
}

// --- UserText ---

func TestUserText(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{fetch.ErrBlocked, "Blocked by site"},
		{fetch.ErrNotFound, "Not found"},
		{fetch.ErrTransient, "Timed out"},
		{fetch.ErrRefused, "Refused"},
		{fetch.ErrOffline, "Offline"},
		{fmt.Errorf("other"), "Error"},
	}
	for _, tc := range cases {
		got := fetch.UserText(tc.err)
		if got != tc.want {
			t.Errorf("UserText(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// --- User-Agent ---

func TestGet_user_agent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	sl := &sleepRecorder{}
	f := newTestFetcher(testBlocks(t), newFakeClock(), sl)
	_, err := f.Get(context.Background(), srv.URL+"/", fetch.Opt{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "Sifty/1.0 (personal read-only job discovery)" {
		t.Errorf("User-Agent = %q", got)
	}
}

// --- redirect behaviour ---

// buildRedirectChain builds a chain of n redirectors (each pointing to the next)
// and returns the first server. The final server in the chain is final.
func buildRedirectChain(t *testing.T, n int, final string) *httptest.Server {
	t.Helper()
	var first *httptest.Server
	target := final
	for i := 0; i < n; i++ {
		tgt := target
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, tgt, http.StatusFound)
		}))
		t.Cleanup(srv.Close)
		target = srv.URL + "/"
		first = srv
	}
	return first
}

func TestGet_redirect_behaviour(t *testing.T) {
	t.Run("redirect to private address is refused", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://192.168.1.1/", http.StatusFound)
		}))
		defer srv.Close()
		f := newTestFetcher(testBlocks(t), newFakeClock(), &sleepRecorder{})
		_, err := f.Get(context.Background(), srv.URL+"/", fetch.Opt{})
		if !errors.Is(err, fetch.ErrRefused) {
			t.Errorf("got %v, want ErrRefused", err)
		}
	})

	t.Run("chain of six redirects is followed", func(t *testing.T) {
		final := okServer(t, "done")
		defer final.Close()
		first := buildRedirectChain(t, 6, final.URL+"/")
		f := newTestFetcher(testBlocks(t), newFakeClock(), &sleepRecorder{})
		resp, err := f.Get(context.Background(), first.URL+"/", fetch.Opt{})
		if err != nil {
			t.Fatalf("chain of 6: unexpected error: %v", err)
		}
		if strings.HasPrefix(resp.URL, first.URL) {
			t.Errorf("response URL %q should be the final server, not the first", resp.URL)
		}
	})

	t.Run("seventh redirect is refused", func(t *testing.T) {
		first := buildRedirectChain(t, 7, "http://nowhere.test/")
		f := newTestFetcher(testBlocks(t), newFakeClock(), &sleepRecorder{})
		_, err := f.Get(context.Background(), first.URL+"/", fetch.Opt{})
		if !errors.Is(err, fetch.ErrTransient) {
			t.Errorf("got %v, want ErrTransient", err)
		}
	})

	t.Run("response URL is final destination after redirect", func(t *testing.T) {
		target := okServer(t, "hi")
		defer target.Close()
		first := buildRedirectChain(t, 1, target.URL+"/landing")
		f := newTestFetcher(testBlocks(t), newFakeClock(), &sleepRecorder{})
		resp, err := f.Get(context.Background(), first.URL+"/", fetch.Opt{})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(resp.URL, "/landing") {
			t.Errorf("URL = %q, want suffix /landing", resp.URL)
		}
	})
}
