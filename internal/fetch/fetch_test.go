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

	"github.com/sifty-works/sifty-server/internal/db"
	"github.com/sifty-works/sifty-server/internal/fetch"
	"github.com/sifty-works/sifty-server/internal/fetch/fetchtest"
)

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

func testBlocks(t *testing.T) *fetch.DomainBlocks {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"), db.Migrations)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return fetch.NewDomainBlocks(database)
}

func newTestFetcher(bs *fetch.DomainBlocks, clk *fakeClock, sl *sleepRecorder) fetch.Fetcher {
	return fetch.New(fetch.Options{
		Blocks:  bs,
		Clock:   clk.Now,
		Sleep:   sl.Sleep,
		Rand:    rand.New(rand.NewSource(0)),
		Policy:  fetch.AllowLoopback,
		Timeout: 5 * time.Second,
	})
}

func newTestFetcherWithPolicy(t *testing.T, policy fetch.AddressPolicy) fetch.Fetcher {
	return fetch.New(fetch.Options{
		Blocks: testBlocks(t),
		Clock:  time.Now,
		Sleep: func(_ context.Context, _ time.Duration) error {
			return nil
		},
		Rand:    rand.New(rand.NewSource(0)),
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
	newTestFetcher(blocks, clk, &sleepRecorder{}).Get(ctx, srv.URL+"/", fetch.Opt{})
	n := calls.Load()
	if _, err := newTestFetcher(blocks, clk, &sleepRecorder{}).Get(ctx, srv.URL+"/", fetch.Opt{}); !errors.Is(err, fetch.ErrBlocked) {
		t.Fatalf("new fetcher: want ErrBlocked, got %v", err)
	}
	if calls.Load() != n {
		t.Error("new fetcher made HTTP request during block")
	}
	clk.Advance(25 * time.Hour)
	newTestFetcher(blocks, clk, &sleepRecorder{}).Get(ctx, srv.URL+"/", fetch.Opt{})
	if calls.Load() == n {
		t.Error("after 24h, no request made; block should have expired")
	}
}

func TestStatusRule(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		header  http.Header
		body    []byte
		wantErr error
	}{
		{"403 is blocked", 403, nil, nil, fetch.ErrBlocked},
		{"cf-mitigated is blocked", 200, http.Header{"Cf-Mitigated": {"challenge"}}, nil, fetch.ErrBlocked},
		{"challenge-platform on 503 is blocked", 503, nil, []byte("challenge-platform"), fetch.ErrBlocked},
		{"Just a moment title is blocked", 200, nil, []byte("<title>Just a moment...</title>"), fetch.ErrBlocked},
		{"404 is not found", 404, nil, nil, fetch.ErrNotFound},
		{"429 is transient", 429, nil, nil, fetch.ErrTransient},
		{"503 without challenge is transient", 503, nil, nil, fetch.ErrTransient},
		{"challenge-platform on 200 is content", 200, nil, []byte("challenge-platform"), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, vs := range tc.header {
					w.Header()[k] = vs
				}
				w.WriteHeader(tc.status)
				w.Write(tc.body)
			}))
			defer srv.Close()
			f := newTestFetcher(testBlocks(t), newFakeClock(), &sleepRecorder{})
			_, fetchErr := f.Get(context.Background(), srv.URL+"/", fetch.Opt{})
			if !errors.Is(fetchErr, tc.wantErr) {
				t.Errorf("fetcher: got %v, want %v", fetchErr, tc.wantErr)
			}
			m := fetchtest.NewMap()
			m.Set("https://x.test/", fetchtest.MapEntry{
				Response: fetch.Response{Status: tc.status, Body: tc.body},
				Header:   tc.header,
			})
			_, mapErr := m.Get(context.Background(), "https://x.test/", fetch.Opt{})
			if !errors.Is(mapErr, tc.wantErr) {
				t.Errorf("map: got %v, want %v", mapErr, tc.wantErr)
			}
		})
	}
}

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

	mustTransient("/fail")
	n := calls.Load()
	clk.Advance(10 * time.Second)
	mustTransient("/fail")
	if calls.Load() != n {
		t.Errorf("call during back-off made HTTP request")
	}
	clk.Advance(21 * time.Second)
	mustTransient("/fail")
	clk.Advance(61 * time.Second)
	if _, err := f.Get(ctx, srv.URL+"/ok", fetch.Opt{}); err != nil {
		t.Fatalf("after success: %v", err)
	}
	n = calls.Load()
	clk.Advance(time.Second)
	f.Get(ctx, srv.URL+"/fail", fetch.Opt{})
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

func TestGet_spacing_within_domain(t *testing.T) {
	srv := okServer(t, "hi")
	defer srv.Close()

	clk := newFakeClock()
	sl := &sleepRecorder{}
	f := newTestFetcher(testBlocks(t), clk, sl)

	_, err := f.Get(context.Background(), srv.URL+"/a", fetch.Opt{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sl.pauses) != 0 {
		t.Errorf("first request: unexpected sleep %v", sl.pauses)
	}

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

func TestGet_body_over_8MB_is_transient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(200)
		chunk := strings.Repeat("x", 1024)
		for i := 0; i < 8193; i++ {
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
		<-done
	}))
	defer srv.Close()
	defer close(done)

	f := fetch.New(fetch.Options{
		Blocks: testBlocks(t),
		Clock:  time.Now,
		Sleep: func(_ context.Context, _ time.Duration) error {
			return nil
		},
		Rand:    rand.New(rand.NewSource(0)),
		Policy:  fetch.AllowLoopback,
		Timeout: 50 * time.Millisecond,
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

	_, err = f.Get(context.Background(), srv.URL+"/", fetch.Opt{})
	if errors.Is(err, fetch.ErrTransient) {
		t.Errorf("after cancellation, domain is in back-off")
	}
}

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
		Rand:    rand.New(rand.NewSource(0)),
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

func TestUserText(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{fetch.ErrBlocked, "Blocked by site"},
		{fetch.ErrNotFound, "Not found"},
		{fetch.ErrTimeout, "Timed out"},
		{fetch.ErrTransient, "Site not answering"},
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
