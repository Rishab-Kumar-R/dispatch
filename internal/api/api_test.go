package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Rishab-Kumar-R/dispatch/internal/store"
	"github.com/Rishab-Kumar-R/dispatch/internal/store/db"
)

var testTenant = uuid.New()

type fakeStore struct {
	authErr   error
	allow     bool
	ingestRes store.IngestResult
	ingestErr error
	ingested  []store.IngestInput
	queued    []uuid.UUID
	created   store.CreateEndpointInput
	endpoints map[uuid.UUID]db.Endpoint
}

func newFakeStore() *fakeStore {
	return &fakeStore{allow: true, endpoints: map[uuid.UUID]db.Endpoint{}}
}

func (f *fakeStore) Authenticate(context.Context, []byte) (store.Principal, error) {
	if f.authErr != nil {
		return store.Principal{}, f.authErr
	}
	return store.Principal{
		APIKeyID:           uuid.New(),
		TenantID:           testTenant,
		RateLimitPerMinute: 100,
	}, nil
}
func (f *fakeStore) AllowRequest(context.Context, uuid.UUID, int, time.Time) (bool, error) {
	return f.allow, nil
}
func (f *fakeStore) Ingest(_ context.Context, in store.IngestInput) (store.IngestResult, error) {
	f.ingested = append(f.ingested, in)
	return f.ingestRes, f.ingestErr
}
func (f *fakeStore) MarkQueued(_ context.Context, ids []uuid.UUID) error {
	f.queued = append(f.queued, ids...)
	return nil
}
func (f *fakeStore) CreateEndpoint(_ context.Context, in store.CreateEndpointInput) (db.Endpoint, error) {
	f.created = in
	ep := db.Endpoint{
		ID:          in.ID,
		TenantID:    in.TenantID,
		Url:         in.URL,
		Description: in.Description,
		EventTypes:  in.EventTypes,
		Status:      db.EndpointStatusActive,
		CreatedAt:   time.Now(),
	}
	f.endpoints[in.ID] = ep
	return ep, nil
}
func (f *fakeStore) GetEndpoint(_ context.Context, tenant, id uuid.UUID) (db.Endpoint, error) {
	ep, ok := f.endpoints[id]
	if !ok || ep.TenantID != tenant {
		return db.Endpoint{}, store.ErrNotFound
	}
	return ep, nil
}
func (f *fakeStore) ListEndpoints(context.Context, uuid.UUID, *uuid.UUID, int) ([]db.Endpoint, error) {
	return nil, nil
}
func (f *fakeStore) DeleteEndpoint(context.Context, uuid.UUID, uuid.UUID) error { return nil }

type fakePublisher struct {
	published []uuid.UUID
	err       error
}

func (p *fakePublisher) Publish(_ context.Context, ids []uuid.UUID) error {
	if p.err != nil {
		return p.err
	}
	p.published = append(p.published, ids...)
	return nil
}

type fakeBox struct{}

func (fakeBox) Encrypt(context.Context, uuid.UUID, string) ([]byte, string, error) {
	return []byte("ct"), "key-1", nil
}

func newTestAPI(fs *fakeStore, pub *fakePublisher) http.Handler {
	return New(Deps{
		Store:     fs,
		Publisher: pub,
		Secrets:   fakeBox{},
		Now: func() time.Time {
			return time.Date(2026, 1, 1, 12, 0, 30, 0, time.UTC)
		},
	})
}

// do sends a request with a default bearer token; an empty header value removes that header.
func do(h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer dsp_live_test")
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestHealthzNeedsNoAuth(t *testing.T) {
	rr := do(newTestAPI(newFakeStore(), &fakePublisher{}), "GET", "/healthz", "", map[string]string{"Authorization": ""})
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d", rr.Code)
	}
}

func TestAuthFailures(t *testing.T) {
	cases := []struct {
		name    string
		authErr error
		header  map[string]string
		want    int
	}{
		{"missing header", nil, map[string]string{"Authorization": ""}, 401},
		{"not a bearer token", nil, map[string]string{"Authorization": "Basic abc"}, 401},
		{"unknown key", store.ErrUnauthenticated, nil, 401},
		{"suspended tenant", store.ErrTenantSuspended, nil, 403},
		{"store failure", errors.New("db down"), nil, 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeStore()
			fs.authErr = tc.authErr
			rr := do(newTestAPI(fs, &fakePublisher{}), "POST", "/v1/events", `{}`, tc.header)
			if rr.Code != tc.want {
				t.Fatalf("got %d, want %d (%s)", rr.Code, tc.want, rr.Body)
			}
			if strings.Contains(rr.Body.String(), "db down") {
				t.Fatal("internal error text leaked to the client")
			}
		})
	}
}

func TestRateLimited(t *testing.T) {
	fs := newFakeStore()
	fs.allow = false
	rr := do(newTestAPI(fs, &fakePublisher{}), "POST", "/v1/events", `{}`, nil)
	if rr.Code != 429 || rr.Header().Get("Retry-After") != "30" {
		t.Fatalf("got %d, Retry-After=%q", rr.Code, rr.Header().Get("Retry-After"))
	}
}

func TestCreateEventAccepted(t *testing.T) {
	fs, pub := newFakeStore(), &fakePublisher{}
	d1, d2 := uuid.New(), uuid.New()
	fs.ingestRes = store.IngestResult{
		Event:       db.Event{ID: uuid.New(), EventType: "invoice.paid", CreatedAt: time.Now()},
		DeliveryIDs: []uuid.UUID{d1, d2},
	}
	rr := do(newTestAPI(fs, pub), "POST", "/v1/events",
		`{"event_type":"invoice.paid","payload":{"amount":1}}`, map[string]string{"Idempotency-Key": "k1"})

	if rr.Code != http.StatusAccepted {
		t.Fatalf("got %d: %s", rr.Code, rr.Body)
	}
	if len(fs.ingested) != 1 || fs.ingested[0].IdempotencyKey != "k1" || fs.ingested[0].TenantID != testTenant {
		t.Fatalf("unexpected ingest input: %+v", fs.ingested)
	}
	if len(pub.published) != 2 || len(fs.queued) != 2 {
		t.Fatalf("published=%d queued=%d, want 2/2", len(pub.published), len(fs.queued))
	}
	var body eventResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body.Deliveries != 2 || body.Duplicate {
		t.Fatalf("body: %+v err=%v", body, err)
	}
}

func TestCreateEventDuplicateReturns200AndPublishesNothing(t *testing.T) {
	fs, pub := newFakeStore(), &fakePublisher{}
	fs.ingestRes = store.IngestResult{Event: db.Event{ID: uuid.New(), EventType: "a.b"}, Duplicate: true}
	rr := do(newTestAPI(fs, pub), "POST", "/v1/events", `{"event_type":"a.b","payload":{}}`,
		map[string]string{"Idempotency-Key": "k1"})
	if rr.Code != http.StatusOK || len(pub.published) != 0 {
		t.Fatalf("got %d, published=%d", rr.Code, len(pub.published))
	}
}

func TestCreateEventIdempotencyConflict(t *testing.T) {
	fs := newFakeStore()
	fs.ingestErr = store.ErrIdempotencyConflict
	rr := do(newTestAPI(fs, &fakePublisher{}), "POST", "/v1/events", `{"event_type":"a.b","payload":{}}`,
		map[string]string{"Idempotency-Key": "k1"})
	if rr.Code != http.StatusConflict {
		t.Fatalf("got %d", rr.Code)
	}
}

func TestCreateEventValidation(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		headers map[string]string
		want    int
	}{
		{"bad event type", `{"event_type":"bad type!","payload":{}}`, nil, 422},
		{"payload not an object", `{"event_type":"a.b","payload":[1]}`, nil, 422},
		{"payload missing", `{"event_type":"a.b"}`, nil, 422},
		{"unknown field", `{"event_type":"a.b","payload":{},"extra":1}`, nil, 400},
		{"not json", `not json`, nil, 400},
		{"trailing data", `{"event_type":"a.b","payload":{}} {}`, nil, 400},
		{"idempotency key too long", `{"event_type":"a.b","payload":{}}`,
			map[string]string{"Idempotency-Key": strings.Repeat("x", 256)}, 422},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeStore()
			rr := do(newTestAPI(fs, &fakePublisher{}), "POST", "/v1/events", tc.body, tc.headers)
			if rr.Code != tc.want {
				t.Fatalf("got %d, want %d (%s)", rr.Code, tc.want, rr.Body)
			}
			if len(fs.ingested) != 0 {
				t.Fatal("invalid request reached the store")
			}
		})
	}
}

func TestPublishFailureStillAccepted(t *testing.T) {
	fs, pub := newFakeStore(), &fakePublisher{err: errors.New("sqs down")}
	fs.ingestRes = store.IngestResult{
		Event: db.Event{ID: uuid.New(), EventType: "a.b"}, DeliveryIDs: []uuid.UUID{uuid.New()},
	}
	rr := do(newTestAPI(fs, pub), "POST", "/v1/events", `{"event_type":"a.b","payload":{}}`, nil)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("an outbox event must be accepted even if SQS is down, got %d", rr.Code)
	}
	if len(fs.queued) != 0 {
		t.Fatal("rows must stay unqueued so the relay picks them up")
	}
}

func TestCreateEndpointShowsSecretOnlyOnce(t *testing.T) {
	fs := newFakeStore()
	h := newTestAPI(fs, &fakePublisher{})

	rr := do(h, "POST", "/v1/endpoints", `{"url":"https://example.com/hook","event_types":["invoice.paid"]}`, nil)
	if rr.Code != http.StatusCreated {
		t.Fatalf("got %d: %s", rr.Code, rr.Body)
	}
	var created endpointResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Secret, "whsec_") {
		t.Fatalf("secret missing from create response: %+v", created)
	}
	if string(fs.created.Ciphertext) != "ct" || fs.created.KMSKeyID != "key-1" {
		t.Fatalf("encrypted secret not passed to the store: %+v", fs.created)
	}

	got := do(h, "GET", "/v1/endpoints/"+created.ID.String(), "", nil)
	if got.Code != http.StatusOK || strings.Contains(got.Body.String(), "secret") {
		t.Fatalf("GET must not expose the secret: %d %s", got.Code, got.Body)
	}
}

func TestCreateEndpointRejectsPrivateURL(t *testing.T) {
	rr := do(newTestAPI(newFakeStore(), &fakePublisher{}), "POST", "/v1/endpoints", `{"url":"https://10.0.0.1/hook"}`, nil)
	if rr.Code != 422 {
		t.Fatalf("got %d", rr.Code)
	}
}

func TestGetEndpointNotFound(t *testing.T) {
	h := newTestAPI(newFakeStore(), &fakePublisher{})
	for _, id := range []string{uuid.NewString(), "not-a-uuid"} {
		if rr := do(h, "GET", "/v1/endpoints/"+id, "", nil); rr.Code != 404 {
			t.Fatalf("id %q: got %d", id, rr.Code)
		}
	}
}
