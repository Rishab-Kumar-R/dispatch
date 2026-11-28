package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/Rishab-Kumar-R/dispatch/internal/auth"
	"github.com/Rishab-Kumar-R/dispatch/internal/store"
	"github.com/Rishab-Kumar-R/dispatch/internal/store/db"
	"github.com/google/uuid"
)

const maxBodyBytes = 256 << 10

// Store is the slice of *store.Store the API needs.
type Store interface {
	Authenticate(ctx context.Context, keyHash []byte) (store.Principal, error)
	AllowRequest(ctx context.Context, tenantID uuid.UUID, limitPerMinute int, now time.Time) (bool, error)
	Ingest(ctx context.Context, in store.IngestInput) (store.IngestResult, error)
	MarkQueued(ctx context.Context, ids []uuid.UUID) error
	CreateEndpoint(ctx context.Context, in store.CreateEndpointInput) (db.Endpoint, error)
	GetEndpoint(ctx context.Context, tenantID, id uuid.UUID) (db.Endpoint, error)
	ListEndpoints(ctx context.Context, tenantID uuid.UUID, afterID *uuid.UUID, limit int) ([]db.Endpoint, error)
	DeleteEndpoint(ctx context.Context, tenantID, id uuid.UUID) error
}

type Publisher interface {
	Publish(ctx context.Context, deliveryIDs []uuid.UUID) error
}

type SecretBox interface {
	Encrypt(ctx context.Context, endpointID uuid.UUID, plaintext string) (ciphertext []byte, keyID string, err error)
}

type Deps struct {
	Store     Store
	Publisher Publisher
	Secrets   SecretBox
	Logger    *slog.Logger
	Now       func() time.Time
}

type API struct {
	Deps
}

func New(d Deps) http.Handler {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	a := &API{
		d,
	}

	v1 := http.NewServeMux()
	v1.HandleFunc("POST /v1/events", a.createEvent)
	v1.HandleFunc("POST /v1/endpoints", a.createEndpoint)
	v1.HandleFunc("GET /v1/endpoints", a.listEndpoints)
	v1.HandleFunc("GET /v1/endpoints/{id}", a.getEndpoint)
	v1.HandleFunc("DELETE /v1/endpoints/{id}", a.deleteEndpoint)

	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	root.Handle("/v1/", a.authenticate(v1))
	root.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such route")
	})

	return a.recoverer(root)
}

type principalKey struct{}

func principalFrom(ctx context.Context) store.Principal {
	p, _ := ctx.Value(principalKey{}).(store.Principal)
	return p
}

func (a *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := auth.BearerToken(r.Header.Get("Authorization"))
		if !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "unauthenticated", "missing or malformed Authorization header")
			return
		}

		p, err := a.Store.Authenticate(r.Context(), auth.HashKey(token))
		switch {
		case errors.Is(err, store.ErrUnauthenticated):
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "unauthenticated", "invalid api key")
			return
		case errors.Is(err, store.ErrTenantSuspended):
			writeError(w, http.StatusForbidden, "tenant_suspended", "this tenant is suspended")
			return
		case err != nil:
			a.fail(w, r, "authenticate", err)
			return
		}

		now := a.Now()
		allowed, err := a.Store.AllowRequest(r.Context(), p.TenantID, p.RateLimitPerMinute, now)
		if err != nil {
			a.fail(w, r, "rate limit", err)
			return
		}
		if !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(60-now.UTC().Second()))
			writeError(w, http.StatusTooManyRequests, "rate_limited", "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

func (a *API) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				a.Logger.ErrorContext(r.Context(), "panic in handler", "panic", v)
				writeError(w, http.StatusInternalServerError, "internal", "internal error")
			}
		}()

		next.ServeHTTP(w, r)
	})
}

func (a *API) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.Logger.ErrorContext(r.Context(), what, "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	var b errorBody
	b.Error.Code, b.Error.Message = code, msg
	writeJSON(w, status, b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds 256 KB")
			return false
		}

		writeError(w, http.StatusBadRequest, "invalid_json", "request body is not valid JSON for this endpoint")
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_json", "unexpected data after the JSON body")
		return false
	}

	return true
}
