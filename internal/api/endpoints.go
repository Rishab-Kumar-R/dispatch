package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Rishab-Kumar-R/dispatch/internal/delivery"
	"github.com/Rishab-Kumar-R/dispatch/internal/store"
	"github.com/Rishab-Kumar-R/dispatch/internal/store/db"
	"github.com/google/uuid"
)

type createEndpointRequest struct {
	URL         string   `json:"url"`
	Description *string  `json:"description"`
	EventTypes  []string `json:"event_types"`
}

type endpointResponse struct {
	ID          uuid.UUID `json:"id"`
	URL         string    `json:"url"`
	Description *string   `json:"description"`
	EventTypes  []string  `json:"event_types"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	Secret      string    `json:"secret,omitempty"`
}

func toEndpoint(e db.Endpoint) endpointResponse {
	types := e.EventTypes
	if types == nil {
		types = []string{}
	}
	return endpointResponse{
		ID:          e.ID,
		URL:         e.Url,
		Description: e.Description,
		EventTypes:  types,
		Status:      string(e.Status),
		CreatedAt:   e.CreatedAt,
	}
}

func (a *API) createEndpoint(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())

	var req createEndpointRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := delivery.ValidateEndpointURL(req.URL); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return
	}
	if req.Description != nil && len(*req.Description) > 500 {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "description is limited to 500 characters")
		return
	}
	types, err := normalizeEventTypes(req.EventTypes)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return
	}

	id := store.NewID()
	secret, err := delivery.NewSecret()
	if err != nil {
		a.fail(w, r, "generate secret", err)
		return
	}
	ciphertext, keyID, err := a.Secrets.Encrypt(r.Context(), id, secret)
	if err != nil {
		a.fail(w, r, "encrypt secret", err)
		return
	}
	ep, err := a.Store.CreateEndpoint(r.Context(), store.CreateEndpointInput{
		ID:          id,
		TenantID:    p.TenantID,
		URL:         req.URL,
		Description: req.Description,
		EventTypes:  types,
		Ciphertext:  ciphertext,
		KMSKeyID:    keyID,
	})
	if err != nil {
		a.fail(w, r, "create endpoint", err)
		return
	}

	resp := toEndpoint(ep)
	resp.Secret = secret
	writeJSON(w, http.StatusCreated, resp)
}

func (a *API) getEndpoint(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	ep, err := a.Store.GetEndpoint(r.Context(), principalFrom(r.Context()).TenantID, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "endpoint not found")
		return
	}
	if err != nil {
		a.fail(w, r, "get endpoint", err)
		return
	}

	writeJSON(w, http.StatusOK, toEndpoint(ep))
}

func (a *API) listEndpoints(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			writeError(w, http.StatusUnprocessableEntity, "validation_error", "limit must be between 1 and 100")
			return
		}
		limit = n
	}

	var after *uuid.UUID
	if v := r.URL.Query().Get("after"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_error", "after must be an endpoint id")
			return
		}

		after = &id
	}

	rows, err := a.Store.ListEndpoints(r.Context(), p.TenantID, after, limit+1)
	if err != nil {
		a.fail(w, r, "list endpoints", err)
		return
	}

	var next *uuid.UUID
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1].ID
		next = &last
	}

	data := make([]endpointResponse, len(rows))
	for i, e := range rows {
		data[i] = toEndpoint(e)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":       data,
		"next_after": next,
	})
}

func (a *API) deleteEndpoint(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	err := a.Store.DeleteEndpoint(r.Context(), principalFrom(r.Context()).TenantID, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "endpoint not found")
		return
	}
	if err != nil {
		a.fail(w, r, "delete endpoint", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func pathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "endpoint not found")
		return uuid.Nil, false
	}

	return id, true
}

func normalizeEventTypes(in []string) ([]string, error) {
	if len(in) > 50 {
		return nil, errors.New("at most 50 event_types per endpoint")
	}

	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, t := range in {
		if !eventTypeRe.MatchString(t) {
			return nil, errors.New("invalid event type: " + t)
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}

	return out, nil
}
