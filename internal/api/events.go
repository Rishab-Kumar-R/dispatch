package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/Rishab-Kumar-R/dispatch/internal/store"
	"github.com/google/uuid"
)

var eventTypeRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type createEventRequest struct {
	EventType string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
}

type eventResponse struct {
	ID         uuid.UUID `json:"id"`
	EventType  string    `json:"event_type"`
	CreatedAt  time.Time `json:"created_at"`
	Deliveries int       `json:"deliveries"`
	Duplicate  bool      `json:"duplicate"`
}

func (a *API) createEvent(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())

	var req createEventRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !eventTypeRe.MatchString(req.EventType) {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "event_type must be 1-128 chars: letters, digits, '.', '_', ':', '-'")
		return
	}
	if !isJSONObject(req.Payload) {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "payload must be a JSON object")
		return
	}

	key := r.Header.Get("Idempotency-Key")
	if key != "" && !validIdempotencyKey(key) {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "Idempotency-Key must be 1-255 printable ASCII characters")
		return
	}

	res, err := a.Store.Ingest(r.Context(), store.IngestInput{
		TenantID:       p.TenantID,
		EventType:      req.EventType,
		Payload:        req.Payload,
		IdempotencyKey: key,
	})
	if errors.Is(err, store.ErrIdempotencyConflict) {
		writeError(w, http.StatusConflict, "idempotency_conflict", "this Idempotency-Key was already used with a different request")
		return
	}
	if err != nil {
		a.fail(w, r, "ingest", err)
		return
	}

	if len(res.DeliveryIDs) > 0 {
		a.publish(r.Context(), res.DeliveryIDs)
	}

	status := http.StatusAccepted
	if res.Duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, eventResponse{
		ID:         res.Event.ID,
		EventType:  res.Event.EventType,
		CreatedAt:  res.Event.CreatedAt,
		Deliveries: len(res.DeliveryIDs),
		Duplicate:  res.Duplicate,
	})
}

func (a *API) publish(ctx context.Context, ids []uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if err := a.Publisher.Publish(ctx, ids); err != nil {
		a.Logger.WarnContext(ctx, "publish failed; relay will retry", "err", err, "deliveries", len(ids))
		return
	}

	if err := a.Store.MarkQueued(ctx, ids); err != nil {
		a.Logger.WarnContext(ctx, "mark queued failed; relay will re-publish", "err", err)
	}
}

func isJSONObject(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && t[0] == '{'
}

func validIdempotencyKey(k string) bool {
	if len(k) == 0 || len(k) > 255 {
		return false
	}

	for i := 0; i < len(k); i++ {
		if k[i] < 0x21 || k[i] > 0x7e {
			return false
		}
	}

	return true
}
