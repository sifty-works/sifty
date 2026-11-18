package fetch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	userAgent      = "Sifty/1.0 (personal read-only job discovery)"
	maxBodyBytes   = 8 * 1024 * 1024 // 8 MB
	defaultTimeout = 30 * time.Second
	maxRedirects   = 6
	spacingBase    = 3 * time.Second
	spacingJitter  = 3 * time.Second
	blockDuration  = 24 * time.Hour
	backoffInitial = 30 * time.Second
	backoffMax     = 1 * time.Hour
)

// Options configure a fetcher built by New.
type Options struct {
	// Blocks is the persistent domain block store (required).
	Blocks *DomainBlocks
	// Clock returns the current time. Defaults to time.Now.
	Clock func() time.Time
	// Sleep pauses for d, honouring ctx. Defaults to a real sleep.
	Sleep func(ctx context.Context, d time.Duration) error
	// Rand is the random source for spacing jitter.
	Rand *rand.Rand
	// Policy decides whether a resolved or dialled IP may be used.
	// Defaults to PublicOnly.
	Policy AddressPolicy
	// Timeout per request. Defaults to 30 s.
	Timeout time.Duration
	// Offline makes every Get return ErrOffline.
	Offline bool
}

// domainState tracks per-domain spacing and back-off.
type domainState struct {
	lastRequest  time.Time
	backoff      time.Duration // 0 means no back-off; current step size
	backoffUntil time.Time     // time after which the domain may be tried again
}

// client implements Fetcher.
type client struct {
	blocks  *DomainBlocks
	clock   func() time.Time
	sleep   func(ctx context.Context, d time.Duration) error
	rng     *rand.Rand
	policy  AddressPolicy
	timeout time.Duration
	offline bool

	mu      sync.Mutex
	domains map[string]*domainState

	httpClient *http.Client
}

// New returns a Fetcher configured by opt.
func New(opt Options) Fetcher {
	if opt.Clock == nil {
		opt.Clock = time.Now
	}
	if opt.Sleep == nil {
		opt.Sleep = realSleep
	}
	if opt.Rand == nil {
		opt.Rand = rand.New(rand.NewSource(time.Now().UnixNano())) //nolint:gosec
	}
	if opt.Policy == nil {
		opt.Policy = PublicOnly
	}
	if opt.Timeout == 0 {
		opt.Timeout = defaultTimeout
	}

	c := &client{
		blocks:  opt.Blocks,
		clock:   opt.Clock,
		sleep:   opt.Sleep,
		rng:     opt.Rand,
		policy:  opt.Policy,
		timeout: opt.Timeout,
		offline: opt.Offline,
		domains: make(map[string]*domainState),
	}
	c.httpClient = c.buildHTTPClient()
	return c
}

func realSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// buildHTTPClient builds an http.Client with our policy-enforcing dialer and
// a CheckRedirect that validates each hop and limits the chain to maxRedirects.
func (c *client) buildHTTPClient() *http.Client {
	dialer := &net.Dialer{
		ControlContext: func(_ context.Context, _, address string, _ syscall.RawConn) error {
			return c.checkDialAddress(address)
		},
	}

	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		MaxIdleConns:          20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return &http.Client{
		Transport: transport,
		// CheckRedirect validates each hop: refuse after maxRedirects and
		// reject any redirect to an unsupported scheme or a URL with credentials.
		// The dialer's ControlContext checks every resolved IP, so private
		// address redirection is caught there without an extra pre-check.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return wrapErr(ErrTransient, "too many redirects (>%d)", maxRedirects)
			}
			if _, err := parseAndCheckURL(req.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}
}

// checkDialAddress is called by the dialer for each TCP dial.
// address is "ip:port" with the actual resolved address.
func (c *client) checkDialAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil
	}
	if !c.policy(ip) {
		return wrapErr(ErrRefused, "address %s is not a public address", host)
	}
	return nil
}

// Get fetches the given URL.
func (c *client) Get(ctx context.Context, rawURL string, opt Opt) (Response, error) {
	if c.offline {
		return Response{}, ErrOffline
	}

	u, err := parseAndCheckURL(rawURL)
	if err != nil {
		return Response{}, err
	}

	domain := domainKey(u.Host)

	if err := c.checkBlocked(domain); err != nil {
		return Response{}, err
	}

	if err := c.checkBackoff(domain); err != nil {
		return Response{}, err
	}

	if err := c.applySpacing(ctx, domain); err != nil {
		// Spacing honours ctx; a cancellation here is not a transient error.
		return Response{}, ctx.Err()
	}

	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Response{}, wrapErr(ErrTransient, "building request: %v", err)
	}
	req.Header.Set("User-Agent", userAgent)
	if opt.Accept != "" {
		req.Header.Set("Accept", opt.Accept)
	}

	httpResp, err := c.httpClient.Do(req)
	if err != nil {
		// Caller cancelled or its deadline passed: propagate as-is, no back-off.
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		// Per-request timeout fired.
		if reqCtx.Err() != nil {
			return Response{}, wrapErr(ErrTransient, "request timed out")
		}
		// Do not record back-off for address refusals — only for genuine
		// transient network failures.
		if !errors.Is(err, ErrRefused) {
			c.recordTransient(domain)
		}
		if errors.Is(err, ErrRefused) || errors.Is(err, ErrTransient) {
			return Response{}, err
		}
		return Response{}, wrapErr(ErrTransient, "request failed: %v", err)
	}

	body, err := io.ReadAll(io.LimitReader(httpResp.Body, maxBodyBytes+1))
	httpResp.Body.Close()
	if err != nil {
		return Response{}, wrapErr(ErrTransient, "reading body: %v", err)
	}
	if len(body) > maxBodyBytes {
		return Response{}, wrapErr(ErrTransient, "response body exceeds 8 MB limit")
	}

	// resp.Request.URL is the final URL after all redirects.
	finalURL := httpResp.Request.URL.String()
	ct := httpResp.Header.Get("Content-Type")
	resp, err := c.interpretResponse(httpResp.StatusCode, ct, httpResp.Header, body, finalURL)
	if err != nil {
		// Back-off: not for blocked (recordBlock already fired) or permanent not-found.
		if !errors.Is(err, ErrBlocked) && !errors.Is(err, ErrNotFound) {
			c.recordTransient(domain)
		}
		return Response{}, err
	}
	return resp, nil
}

// parseAndCheckURL validates the scheme and absence of credentials.
func parseAndCheckURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, wrapErr(ErrRefused, "invalid URL: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, wrapErr(ErrRefused, "unsupported scheme %q", u.Scheme)
	}
	if u.User != nil {
		return nil, wrapErr(ErrRefused, "URL must not contain credentials")
	}
	return u, nil
}

// interpretResponse maps HTTP status + body to a Response or error.
//
// Block rules (in order):
//   - 403 → ErrBlocked
//   - cf-mitigated: challenge header present → ErrBlocked
//   - title is exactly "Just a moment..." → ErrBlocked
//   - 503 body contains "challenge-platform" → ErrBlocked
//   - all other 2xx → content
func (c *client) interpretResponse(status int, ct string, header http.Header, body []byte, finalURL string) (Response, error) {
	domain := domainKey(hostOf(finalURL))

	cloudflareBlocked := header.Get("cf-mitigated") == "challenge" ||
		containsTitleExact(body, "Just a moment...") ||
		(status == 503 && bytes.Contains(body, []byte("challenge-platform")))

	switch {
	case status == 403 || cloudflareBlocked:
		c.recordBlock(domain)
		return Response{}, wrapErr(ErrBlocked, "HTTP %d from %s", status, domain)

	case status == 404 || status == 410:
		return Response{}, wrapErr(ErrNotFound, "HTTP %d from %s", status, domain)

	case status >= 200 && status < 300:
		c.recordSuccess(domain)
		return Response{
			URL:         finalURL,
			Status:      status,
			ContentType: ct,
			Body:        body,
		}, nil

	default: // 429, other 4xx, 5xx
		return Response{}, wrapErr(ErrTransient, "HTTP %d from %s", status, domain)
	}
}

// containsTitleExact returns true if the HTML body has a <title> exactly
// equal to title (case-sensitive, whitespace-trimmed).
func containsTitleExact(body []byte, title string) bool {
	lower := bytes.ToLower(body)
	start := bytes.Index(lower, []byte("<title>"))
	if start < 0 {
		return false
	}
	start += len("<title>")
	end := bytes.Index(lower[start:], []byte("</title>"))
	if end < 0 {
		return false
	}
	return strings.TrimSpace(string(body[start:start+end])) == title
}

// hostOf returns the host from a URL string.
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// --- Blocking ---

func (c *client) checkBlocked(domain string) error {
	until, ok := c.blocks.Blocked(domain)
	if !ok {
		return nil
	}
	if c.clock().Before(until) {
		return wrapErr(ErrBlocked, "domain %s is blocked until %s", domain, until.Format(time.RFC3339))
	}
	return nil
}

func (c *client) recordBlock(domain string) {
	c.blocks.Block(domain, c.clock().Add(blockDuration))
}

// --- Back-off ---

// checkBackoff returns ErrTransient immediately if the domain is still in
// its back-off period. When the period has passed the state is left as-is so
// the next recordTransient doubles from the current step.
func (c *client) checkBackoff(domain string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ds := c.getOrCreate(domain)
	if ds.backoff == 0 {
		return nil
	}
	if c.clock().Before(ds.backoffUntil) {
		return wrapErr(ErrTransient, "domain %s is in back-off until %s", domain, ds.backoffUntil.Format(time.RFC3339))
	}
	return nil
}

func (c *client) recordTransient(domain string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ds := c.getOrCreate(domain)
	if ds.backoff == 0 {
		ds.backoff = backoffInitial
	} else {
		ds.backoff *= 2
		if ds.backoff > backoffMax {
			ds.backoff = backoffMax
		}
	}
	ds.backoffUntil = c.clock().Add(ds.backoff)
}

func (c *client) recordSuccess(domain string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ds := c.getOrCreate(domain)
	ds.backoff = 0
	ds.backoffUntil = time.Time{}
}

// --- Spacing ---

func (c *client) applySpacing(ctx context.Context, domain string) error {
	c.mu.Lock()
	ds := c.getOrCreate(domain)
	last := ds.lastRequest
	now := c.clock()
	jitter := time.Duration(c.rng.Int63n(int64(spacingJitter)))
	required := spacingBase + jitter
	var wait time.Duration
	if !last.IsZero() {
		elapsed := now.Sub(last)
		if elapsed < required {
			wait = required - elapsed
		}
	}
	// Record intended next request time before releasing the lock.
	ds.lastRequest = now.Add(wait)
	c.mu.Unlock()

	if wait > 0 {
		return c.sleep(ctx, wait)
	}
	return nil
}

func (c *client) getOrCreate(domain string) *domainState {
	ds, ok := c.domains[domain]
	if !ok {
		ds = &domainState{}
		c.domains[domain] = ds
	}
	return ds
}
