package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/Rishab-Kumar-R/dispatch/internal/store/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type IngestInput struct {
	TenantID       uuid.UUID
	EventType      string
	Payload        json.RawMessage
	IdempotencyKey string
}

type IngestResult struct {
	Event       db.Event
	Duplicate   bool
	DeliveryIDs []uuid.UUID
}

// Ingest stores the event and creates one pending delivery per matching
// endpoint in a single transaction. The deliveries are the outbox: the caller
// publishes them to SQS afterwards and calls MarkQueued, and the relay sweeps
// whatever was missed.
func (s *Store) Ingest(ctx context.Context, in IngestInput) (IngestResult, error) {
	var res IngestResult
	err := s.InTx(ctx, func(q *db.Queries) error {
		var key *string
		if in.IdempotencyKey != "" {
			key = &in.IdempotencyKey
		}

		ev, err := q.InsertEvent(ctx, db.InsertEventParams{
			ID:             newID(),
			TenantID:       in.TenantID,
			EventType:      in.EventType,
			Payload:        in.Payload,
			IdempotencyKey: key,
		})
		if errors.Is(err, pgx.ErrNoRows) && key != nil {
			existing, err := q.GetEventByIdempotencyKey(ctx, db.GetEventByIdempotencyKeyParams{
				TenantID:       in.TenantID,
				IdempotencyKey: key,
			})
			if err != nil {
				return fmt.Errorf("load idempotent event: %w", err)
			}

			if existing.EventType != in.EventType || !sameJSON(existing.Payload, in.Payload) {
				return ErrIdempotencyConflict
			}
			res = IngestResult{
				Event:     existing,
				Duplicate: true,
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("insert event: %w", err)
		}
		res.Event = ev

		endpointIDs, err := q.ListMatchingEndpoints(ctx, db.ListMatchingEndpointsParams{
			TenantID:  in.TenantID,
			EventType: in.EventType,
		})
		if err != nil {
			return fmt.Errorf("match endpoints: %w", err)
		}
		if len(endpointIDs) == 0 {
			return nil
		}

		ids := make([]uuid.UUID, len(endpointIDs))
		for i := range ids {
			ids[i] = newID()
		}

		res.DeliveryIDs, err = q.CreateDeliveries(ctx, db.CreateDeliveriesParams{
			Ids:         ids,
			TenantID:    in.TenantID,
			EventID:     ev.ID,
			EndpointIds: endpointIDs,
		})
		if err != nil {
			return fmt.Errorf("create deliveries: %w", err)
		}
		return nil
	})

	return res, err
}

// MarkQueued records that deliveries were published to SQS.
func (s *Store) MarkQueued(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}

	return s.q.MarkDeliveriesQueued(ctx, ids)
}

// sameJSON compares two JSON documents structurally. Postgres jsonb normalises
// whitespace and key order, so raw bytes can't be compared; numbers are kept as
// json.Number so large integers don't lose precision.
func sameJSON(a, b []byte) bool {
	da, dbd := json.NewDecoder(bytes.NewReader(a)), json.NewDecoder(bytes.NewReader(b))
	da.UseNumber()
	dbd.UseNumber()

	var va, vb any
	if da.Decode(&va) != nil || dbd.Decode(&vb) != nil {
		return false
	}

	return reflect.DeepEqual(va, vb)
}
