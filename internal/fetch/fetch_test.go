package fetch_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// newTestFetcher builds a client wired with a loopback policy, fake clock,
// and sleep recorder. The httptest server address is not needed here since
// we pass the URL explicitly.
func newTestFetcher(bs fetch.BlockStore, clk *fakeClock, sl *sleepRecorder) fetch.Fetcher {
	return fetch.New(fetch.Options{
		BlockStore: bs,
		Clock:      clk.Now,
		Sleep:      sl.Sleep,
		Rand:       rand.New(rand.NewSource(0)), //nolint:gosec
		Policy:     fetch.AllowLoopback,
		Timeout:    5 * time.Second,
	})
}

// newTestFetcherWithPolicy builds a client with a custom policy.
func newTestFetcherWithPolicy(policy fetch.AddressPolicy) fetch.Fetcher {
	return fetch.New(fetch.Options{
		BlockStore: fetch.NewMemBlockStore(),
		Clock:      time.Now,
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

func TestGet_credentials_refused(t *testing.T) {
	f := newTestFetcherWithPolicy(fetch.AllowLoopback)
	_, err := f.Get(context.Background(), "http://user:pass@example.com/", fetch.Opt{})
	if !errors.Is(err, fetch.ErrRefused) {
		t.Fatalf("credentials in URL: got %v, want ErrRefused", err)
	}
}

func TestGet_unsupported_scheme_refused(t *testing.T) {
	f := newTestFetcherWithPolicy(fetch.AllowLoopback)
	_, err := f.Get(context.Background(), "ftp://example.com/", fetch.Opt{})
	if !errors.Is(err, fetch.ErrRefused) {
		t.Fatalf("ftp scheme: got %v, want ErrRefused", err)
	}
}

// --- dialer control: loopback with PublicOnly policy ---

func TestGet_loopback_refused_by_policy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	}))
	defer srv.Close()

	// Use PublicOnly policy: dialer control should refuse loopback.
	f := newTestFetcherWithPolicy(fetch.PublicOnly)
	_, err := f.Get(context.Background(), srv.URL+"/", fetch.Opt{})
	if !errors.Is(err, fetch.ErrRefused) {
		t.Fatalf("loopback with PublicOnly: got %v, want ErrRefused", err)
	}
}

// --- redirects ---

func TestGet_redirect_to_private_refused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Redirect to a private address.
		http.Redirect(w, r, "http://192.168.1.1/", http.StatusFound)
	}))
	defer srv.Close()

	sl := &sleepRecorder{}
	f := newTestFetcher(fetch.NewMemBlockStore(), newFakeClock(), sl)
	_, err := f.Get(context.Background(), srv.URL+"/", fetch.Opt{})
	if !errors.Is(err, fetch.ErrRefused) {
		t.Fatalf("redirect to private: got %v, want ErrRefused", err)
	}
}

// --- response status codes ---

func TestGet_200_is_content(t *testing.T) {
	srv := okServer(t, "<p>hello</p>")
	defer srv.Close()

	sl := &sleepRecorder{}
	f := newTestFetcher(fetch.NewMemBlockStore(), newFakeClock(), sl)
	resp, err := f.Get(context.Background(), srv.URL+"/", fetch.Opt{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != 200 {
		t.Errorf("status = %d, want 200", resp.Status)
	}
}

func TestGet_403_is_blocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
	}))
	defer srv.Close()

	sl := &sleepRecorder{}
	f := newTestFetcher(fetch.NewMemBlockStore(), newFakeClock(), sl)
	_, err := f.Get(context.Background(), srv.URL+"/", fetch.Opt{})
	if !errors.Is(err, fetch.ErrBlocked) {
		t.Fatalf("403: got %v, want ErrBlocked", err)
	}
}

func TestGet_403_blocks_domain_for_24h(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(403)
	}))
	defer srv.Close()
	clk := newFakeClock()
	f := newTestFetcher(fetch.NewMemBlockStore(), clk, &sleepRecorder{})
	ctx := context.Background()

	if _, err := f.Get(ctx, srv.URL+"/", fetch.Opt{}); !errors.Is(err, fetch.ErrBlocked) {
		t.Fatalf("403: got %v, want ErrBlocked", err)
	}
	n := calls.Load()
	clk.Advance(23 * time.Hour)
	if _, err := f.Get(ctx, srv.URL+"/", fetch.Opt{}); !errors.Is(err, fetch.ErrBlocked) {
		t.Fatalf("within 24h: got %v, want ErrBlocked", err)
	}
	if calls.Load() != n {
		t.Errorf("second call made HTTP request (want no request during block)")
	}
	clk.Advance(2 * time.Hour)
	f.Get(ctx, srv.URL+"/", fetch.Opt{}) //nolint:errcheck
	if calls.Load() == n {
		t.Errorf("after 24h, no request made; expected block to expire")
	}
}

func TestGet_challenge_header_is_blocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("cf-mitigated", "challenge")
		fmt.Fprint(w, "verify")
	}))
	defer srv.Close()
	f := newTestFetcher(fetch.NewMemBlockStore(), newFakeClock(), &sleepRecorder{})
	if _, err := f.Get(context.Background(), srv.URL+"/", fetch.Opt{}); !errors.Is(err, fetch.ErrBlocked) {
		t.Errorf("cf-mitigated: challenge: got %v, want ErrBlocked", err)
	}
}

func TestGet_challenge_platform_on_503_is_blocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		fmt.Fprint(w, `<html><body><script src="/cdn-cgi/challenge-platform/x"></script></body></html>`)
	}))
	defer srv.Close()
	f := newTestFetcher(fetch.NewMemBlockStore(), newFakeClock(), &sleepRecorder{})
	if _, err := f.Get(context.Background(), srv.URL+"/", fetch.Opt{}); !errors.Is(err, fetch.ErrBlocked) {
		t.Errorf("challenge-platform on 503: got %v, want ErrBlocked", err)
	}
}

func TestGet_challenge_platform_on_200_is_content(t *testing.T) {
	body := `<html><body><script src="/cdn-cgi/challenge-platform/x"></script><p>job listing</p></body></html>`
	srv := okServer(t, body)
	defer srv.Close()
	f := newTestFetcher(fetch.NewMemBlockStore(), newFakeClock(), &sleepRecorder{})
	if resp, err := f.Get(context.Background(), srv.URL+"/", fetch.Opt{}); err != nil || resp.Status != 200 {
		t.Errorf("challenge-platform on 200: err=%v status=%d, want 200 content", err, resp.Status)
	}
}

func TestGet_just_a_moment_title_is_blocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html><head><title>Just a moment...</title></head></html>")
	}))
	defer srv.Close()
	f := newTestFetcher(fetch.NewMemBlockStore(), newFakeClock(), &sleepRecorder{})
	if _, err := f.Get(context.Background(), srv.URL+"/", fetch.Opt{}); !errors.Is(err, fetch.ErrBlocked) {
		t.Errorf("'Just a moment...' title: got %v, want ErrBlocked", err)
	}
}

func TestGet_recaptcha_form_on_200_is_content(t *testing.T) {
	body := `<html><body><div class="g-recaptcha">captcha</div></body></html>`
	srv := okServer(t, body)
	defer srv.Close()
	f := newTestFetcher(fetch.NewMemBlockStore(), newFakeClock(), &sleepRecorder{})
	if resp, err := f.Get(context.Background(), srv.URL+"/", fetch.Opt{}); err != nil || resp.Status != 200 {
		t.Errorf("reCAPTCHA on 200: err=%v status=%d, want 200 content", err, resp.Status)
	}
}

func TestGet_not_found_statuses(t *testing.T) {
	for _, status := range []int{404, 410} {
		status := status
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			fmt.Fprint(w, "not found")
		}))
		defer srv.Close()
		f := newTestFetcher(fetch.NewMemBlockStore(), newFakeClock(), &sleepRecorder{})
		resp, err := f.Get(context.Background(), srv.URL+"/", fetch.Opt{})
		if !errors.Is(err, fetch.ErrNotFound) {
			t.Errorf("HTTP %d: got %v, want ErrNotFound", status, err)
		}
		if len(resp.Body) != 0 {
			t.Errorf("HTTP %d returned body, want empty", status)
		}
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
	f := newTestFetcher(fetch.NewMemBlockStore(), clk, &sleepRecorder{})
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
	f := newTestFetcher(fetch.NewMemBlockStore(), clk, &sleepRecorder{})
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
	f := newTestFetcher(fetch.NewMemBlockStore(), clk, sl)

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
	f := newTestFetcher(fetch.NewMemBlockStore(), newFakeClock(), sl)
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
		BlockStore: fetch.NewMemBlockStore(),
		Clock:      time.Now,
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
	f := newTestFetcher(fetch.NewMemBlockStore(), clk, sl)

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
		BlockStore: fetch.NewMemBlockStore(),
		Clock:      time.Now,
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
	f := newTestFetcher(fetch.NewMemBlockStore(), newFakeClock(), sl)
	_, err := f.Get(context.Background(), srv.URL+"/", fetch.Opt{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "Sifty/1.0 (personal read-only job discovery)" {
		t.Errorf("User-Agent = %q", got)
	}
}

// --- final redirect URL ---

func TestGet_redirect_chain_of_six_followed(t *testing.T) {
	final := okServer(t, "done")
	defer final.Close()

	// Build a chain of 6 redirectors, each pointing to the next.
	var chain [6]*httptest.Server
	for i := 5; i >= 0; i-- {
		i := i
		var target string
		if i == 5 {
			target = final.URL + "/"
		} else {
			target = chain[i+1].URL + "/"
		}
		chain[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target, http.StatusFound)
		}))
		defer chain[i].Close()
	}

	sl := &sleepRecorder{}
	f := newTestFetcher(fetch.NewMemBlockStore(), newFakeClock(), sl)
	resp, err := f.Get(context.Background(), chain[0].URL+"/", fetch.Opt{})
	if err != nil {
		t.Fatalf("chain of 6: unexpected error: %v", err)
	}
	if string(resp.Body) != "done" {
		t.Errorf("body = %q, want %q", resp.Body, "done")
	}
	if !strings.HasSuffix(resp.URL, "/") || strings.HasPrefix(resp.URL, chain[0].URL) {
		t.Errorf("response URL %q does not look like the final server", resp.URL)
	}
}

func TestGet_seventh_redirect_refused(t *testing.T) {
	final := okServer(t, "done")
	defer final.Close()

	// Build a chain of 7 redirectors. The 7th hop should be refused.
	var chain [7]*httptest.Server
	for i := 6; i >= 0; i-- {
		i := i
		var target string
		if i == 6 {
			target = final.URL + "/"
		} else {
			target = chain[i+1].URL + "/"
		}
		chain[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target, http.StatusFound)
		}))
		defer chain[i].Close()
	}

	sl := &sleepRecorder{}
	f := newTestFetcher(fetch.NewMemBlockStore(), newFakeClock(), sl)
	_, err := f.Get(context.Background(), chain[0].URL+"/", fetch.Opt{})
	if !errors.Is(err, fetch.ErrTransient) {
		t.Fatalf("7th redirect: got %v, want ErrTransient", err)
	}
}

func TestGet_response_url_is_final_after_redirect(t *testing.T) {
	target := okServer(t, "hi")
	defer target.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/landing", http.StatusMovedPermanently)
	}))
	defer srv.Close()

	sl := &sleepRecorder{}
	f := newTestFetcher(fetch.NewMemBlockStore(), newFakeClock(), sl)
	resp, err := f.Get(context.Background(), srv.URL+"/", fetch.Opt{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(resp.URL, "/landing") {
		t.Errorf("URL = %q, want suffix /landing", resp.URL)
	}
}
