package delivery

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Outcome int

const (
	OutcomeSuccess   Outcome = iota + 1 // 2xx
	OutcomeRetry                        // try again later
	OutcomePermanent                    // do not retry (410 Gone, blocked or invalid destination)
)

func (o Outcome) String() string {
	switch o {
	case OutcomeSuccess:
		return "success"
	case OutcomeRetry:
		return "retry"
	case OutcomePermanent:
		return "permanent"
	}

	return "unknown"
}

type Request struct {
	URL    string
	Body   []byte
	Header http.Header // built with BuildHeaders
}

// Result carries everything the worker persists about one attempt
type Result struct {
	Outcome    Outcome
	StatusCode int
	Snippet    string
	Duration   time.Duration
	RetryAfter time.Duration
	Err        error
}

type ClientConfig struct {
	Timeout               time.Duration // whole attempt; default 15s
	DialTimeout           time.Duration // default 5s
	TLSHandshakeTimeout   time.Duration // default 5s
	ResponseHeaderTimeout time.Duration // default 10s
	MaxSnippetBytes       int           // response bytes kept for the delivery log; default 2048
	MaxRetryAfter         time.Duration // default 1h
	UserAgent             string        // default "Dispatch-Webhooks/1.0"
}

func (c ClientConfig) withDefaults() ClientConfig {
	if c.Timeout <= 0 {
		c.Timeout = 15 * time.Second
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = 5 * time.Second
	}
	if c.TLSHandshakeTimeout <= 0 {
		c.TLSHandshakeTimeout = 5 * time.Second
	}
	if c.ResponseHeaderTimeout <= 0 {
		c.ResponseHeaderTimeout = 10 * time.Second
	}
	if c.MaxSnippetBytes <= 0 {
		c.MaxSnippetBytes = 2048
	}
	if c.MaxRetryAfter <= 0 {
		c.MaxRetryAfter = time.Hour
	}
	if c.UserAgent == "" {
		c.UserAgent = "Dispatch-Webhooks/1.0"
	}

	return c
}

const maxDrainBytes = 64 << 10

type Client struct {
	cfg   ClientConfig
	http  *http.Client
	check func(netip.Addr) error
}

// NewClient is the production client: every connection is checked by CheckAddr.
func NewClient(cfg ClientConfig) *Client {
	return newClient(cfg, CheckAddr, nil)
}

// newClient is unexported so production code cannot weaken the address check
func newClient(cfg ClientConfig, check func(netip.Addr) error, tlsCfg *tls.Config) *Client {
	cfg = cfg.withDefaults()
	if tlsCfg == nil {
		tlsCfg = &tls.Config{}
	}
	tlsCfg = tlsCfg.Clone()
	tlsCfg.MinVersion = tls.VersionTLS12

	dialer := &net.Dialer{
		Timeout:   cfg.DialTimeout,
		KeepAlive: 30 * time.Second,
		Control:   dialControl(check),
	}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       tlsCfg,
		TLSHandshakeTimeout:   cfg.TLSHandshakeTimeout,
		ResponseHeaderTimeout: cfg.ResponseHeaderTimeout,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
	}

	return &Client{
		cfg: cfg,
		http: &http.Client{
			Transport: tr,
			Timeout:   cfg.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		check: check,
	}
}

// dialControl runs after DNS resolution, once per connection attempt, with the
// literal IP about to be dialled. Checking here (not just at URL validation)
// defeats DNS rebinding.
func dialControl(check func(netip.Addr) error) func(network, address string, c syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return fmt.Errorf("%w: unparseable address %q", ErrBlockedDestination, address)
		}

		return check(ap.Addr())
	}
}

// Send performs one delivery attempt. It never returns an error: every outcome
// is data for the worker to record.
func (c *Client) Send(ctx context.Context, r Request) (res Result) {
	start := time.Now()
	defer func() {
		res.Duration = time.Since(start)
	}()

	if err := validateURL(r.URL, c.check); err != nil {
		return Result{
			Outcome: OutcomePermanent,
			Err:     err,
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL, bytes.NewReader(r.Body))
	if err != nil {
		return Result{
			Outcome: OutcomePermanent,
			Err:     err,
		}
	}
	for k, vs := range r.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", c.cfg.UserAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		out := OutcomeRetry
		if errors.Is(err, ErrBlockedDestination) {
			out = OutcomePermanent
		}
		return Result{
			Outcome: out,
			Err:     err,
		}
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, int64(c.cfg.MaxSnippetBytes)))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))

	res = Result{
		StatusCode: resp.StatusCode,
		Snippet:    cleanSnippet(snippet),
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		res.Outcome = OutcomeSuccess
	case resp.StatusCode == http.StatusGone:
		res.Outcome = OutcomePermanent
		res.Err = errors.New("endpoint returned 410 Gone")
	default:
		res.Outcome = OutcomeRetry
		res.Err = fmt.Errorf("unexpected status %d", resp.StatusCode)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			res.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), time.Now(), c.cfg.MaxRetryAfter)
		}
	}

	return res
}

// cleanSnippet makes the response excerpt safe to store in a Postgres text
// column: valid UTF-8 and no NUL bytes (Postgres rejects \x00).
func cleanSnippet(b []byte) string {
	s := strings.ToValidUTF8(string(b), "\uFFFD")
	return strings.ReplaceAll(s, "\x00", "")
}

func parseRetryAfter(v string, now time.Time, limit time.Duration) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}

	var d time.Duration
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs < 0 {
			return 0
		}
		if secs > int64(limit/time.Second) {
			return limit
		}
		d = time.Duration(secs) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = t.Sub(now)
	}
	if d < 0 {
		return 0
	}

	return min(d, limit)
}
