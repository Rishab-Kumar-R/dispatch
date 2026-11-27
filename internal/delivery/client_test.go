package delivery

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(srv *httptest.Server, cfg ClientConfig) *Client {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return newClient(cfg, func(netip.Addr) error {
		return nil
	}, &tls.Config{
		RootCAs: pool,
	})
}

func send(c *Client, url string) Result {
	return c.Send(context.Background(), Request{
		URL:  url,
		Body: []byte(`{"a":1}`),
	})
}

func TestSendSuccessAndHeaders(t *testing.T) {
	type seen struct {
		method, ct, sig, ua, body string
	}
	got := make(chan seen, 1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- seen{
			r.Method,
			r.Header.Get("Content-Type"),
			r.Header.Get(HeaderSignature),
			r.Header.Get("User-Agent"),
			string(b),
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	h, _ := BuildHeaders(HeaderInput{
		EventID:    "e",
		DeliveryID: "d",
		Attempt:    1,
		Timestamp:  time.Now(),
		Secrets:    []string{"s"},
		Body:       []byte(`{"a":1}`),
	})
	res := testClient(srv, ClientConfig{}).Send(context.Background(), Request{
		URL:    srv.URL,
		Body:   []byte(`{"a":1}`),
		Header: h,
	})

	if res.Outcome != OutcomeSuccess || res.StatusCode != 200 || res.Snippet != "ok" || res.Err != nil {
		t.Fatalf("unexpected result: %+v", res)
	}
	s := <-got
	if s.method != http.MethodPost || s.ct != "application/json" || s.body != `{"a":1}` ||
		!strings.HasPrefix(s.sig, "v1=") || s.ua != "Dispatch-Webhooks/1.0" {
		t.Fatalf("receiver saw %+v", s)
	}
}

func TestSendClassifiesStatuses(t *testing.T) {
	cases := []struct {
		status int
		want   Outcome
	}{
		{200, OutcomeSuccess},
		{204, OutcomeSuccess},
		{400, OutcomeRetry},
		{404, OutcomeRetry},
		{429, OutcomeRetry},
		{500, OutcomeRetry},
		{503, OutcomeRetry},
		{410, OutcomePermanent},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			res := send(testClient(srv, ClientConfig{}), srv.URL)
			if res.Outcome != tc.want || res.StatusCode != tc.status {
				t.Fatalf("got %+v, want outcome %s", res, tc.want)
			}
		})
	}
}

func TestSendParsesRetryAfter(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	res := send(testClient(srv, ClientConfig{}), srv.URL)
	if res.RetryAfter != 2*time.Minute {
		t.Fatalf("RetryAfter = %v", res.RetryAfter)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"30", 30 * time.Second},
		{"-5", 0},
		{"999999999999", time.Hour},
		{now.Add(90 * time.Second).UTC().Format(http.TimeFormat), 90 * time.Second},
		{now.Add(-time.Hour).UTC().Format(http.TimeFormat), 0},
		{"garbage", 0},
	}
	for _, tc := range cases {
		if got := parseRetryAfter(tc.in, now, time.Hour); got != tc.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestSendDoesNotFollowRedirects(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirected" {
			hits.Add(1)
			return
		}
		http.Redirect(w, r, "https://"+r.Host+"/redirected", http.StatusFound)
	}))
	defer srv.Close()

	res := send(testClient(srv, ClientConfig{}), srv.URL)
	if res.StatusCode != http.StatusFound || res.Outcome != OutcomeRetry || hits.Load() != 0 {
		t.Fatalf("redirect was followed or misclassified: %+v (hits=%d)", res, hits.Load())
	}
}

func TestSendSnippetCappedAndSanitised(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/nul" {
			_, _ = w.Write([]byte("a\x00b\xffc"))
			return
		}
		_, _ = w.Write([]byte(strings.Repeat("a", 10_000)))
	}))
	defer srv.Close()
	c := testClient(srv, ClientConfig{})

	if res := send(c, srv.URL); len(res.Snippet) != 2048 {
		t.Fatalf("snippet length = %d, want 2048", len(res.Snippet))
	}
	res := send(c, srv.URL+"/nul")
	if strings.ContainsRune(res.Snippet, 0) || !strings.HasPrefix(res.Snippet, "ab") {
		t.Fatalf("snippet not sanitised: %q", res.Snippet)
	}
}

func TestSendTimeout(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(400 * time.Millisecond)
	}))
	defer srv.Close()

	res := send(testClient(srv, ClientConfig{Timeout: 100 * time.Millisecond}), srv.URL)
	if res.Outcome != OutcomeRetry || res.Err == nil || res.StatusCode != 0 {
		t.Fatalf("got %+v", res)
	}
}

func TestSendRejectsInvalidURL(t *testing.T) {
	res := NewClient(ClientConfig{}).Send(context.Background(), Request{URL: "http://example.com/hook"})
	if res.Outcome != OutcomePermanent || !errors.Is(res.Err, ErrInvalidURL) {
		t.Fatalf("got %+v", res)
	}
}

func TestSendBlocksPrivateDestinations(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	c := NewClient(ClientConfig{Timeout: 2 * time.Second})
	targets := map[string]string{
		"ip literal":              srv.URL,
		"hostname -> loopback ip": strings.Replace(srv.URL, "127.0.0.1", "localhost", 1),
	}
	for name, target := range targets {
		t.Run(name, func(t *testing.T) {
			res := send(c, target)
			if res.Outcome != OutcomePermanent || !errors.Is(res.Err, ErrBlockedDestination) {
				t.Fatalf("got %+v", res)
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatalf("a request reached the private server %d times", hits.Load())
	}
}
