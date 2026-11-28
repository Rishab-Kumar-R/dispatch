package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Rishab-Kumar-R/dispatch/internal/delivery"
	"github.com/Rishab-Kumar-R/dispatch/internal/store/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// LoadDelivery returns everything needed to send one delivery.
func (s *Store) LoadDelivery(ctx context.Context, id uuid.UUID) (db.GetDeliveryForSendRow, error) {
	row, err := s.q.GetDeliveryForSend(ctx, id)
	return row, notFound(err)
}

// ActiveSecrets returns the endpoint's live (encrypted) signing secrets, newest first.
func (s *Store) ActiveSecrets(ctx context.Context, endpointID uuid.UUID) ([]db.EndpointSecret, error) {
	return s.q.ListActiveEndpointSecrets(ctx, endpointID)
}

// AdmitEndpoint asks the circuit breaker whether a request may go out now. It
// holds the endpoint row lock, so concurrent workers can't both become the
// half-open probe.
func (s *Store) AdmitEndpoint(ctx context.Context, b *delivery.Breaker, endpointID uuid.UUID) (delivery.Decision, error) {
	var dec delivery.Decision
	err := s.InTx(ctx, func(q *db.Queries) error {
		row, err := q.LockEndpointForUpdate(ctx, endpointID)
		if err != nil {
			return notFound(err)
		}
		cur := stateFromRow(row)
		d, next := b.Admit(cur, s.now())
		if !next.NextProbeAt.Equal(cur.NextProbeAt) {
			if err := persistState(ctx, q, endpointID, next); err != nil {
				return err
			}
		}

		dec = d
		return nil
	})

	return dec, err
}

type Attempt struct {
	DeliveryID           uuid.UUID
	EndpointID           uuid.UUID
	ExpectedAttemptCount int32
	StartedAt            time.Time
	Result               delivery.Result
	NextAttemptAt        time.Time
}

type FinishResult struct {
	Status        db.DeliveryStatus
	AttemptNumber int32
}

// FinishAttempt records the outcome of one attempt atomically: the delivery
// state, the attempt log row and the endpoint's breaker state. The
// expected-attempt guard makes a duplicate SQS delivery lose cleanly with
// ErrStale instead of double-counting.
func (s *Store) FinishAttempt(ctx context.Context, b *delivery.Breaker, a Attempt) (FinishResult, error) {
	var out FinishResult
	err := s.InTx(ctx, func(q *db.Queries) error {
		r := a.Result
		statusCode, errText := int32Ptr(r.StatusCode), errString(r.Err)

		switch r.Outcome {
		case delivery.OutcomeSuccess:
			n, err := q.MarkDeliverySucceeded(ctx, db.MarkDeliverySucceededParams{
				StatusCode:           statusCode,
				ID:                   a.DeliveryID,
				ExpectedAttemptCount: a.ExpectedAttemptCount,
			})
			if err != nil {
				return fmt.Errorf("mark succeeded: %w", err)
			}
			if n == 0 {
				return ErrStale
			}
			out.Status = db.DeliveryStatusSucceeded
		case delivery.OutcomePermanent:
			n, err := q.MarkDeliveryDead(ctx, db.MarkDeliveryDeadParams{
				StatusCode:           statusCode,
				LastError:            errText,
				ID:                   a.DeliveryID,
				ExpectedAttemptCount: a.ExpectedAttemptCount,
			})
			if err != nil {
				return fmt.Errorf("mark dead: %w", err)
			}
			if n == 0 {
				return ErrStale
			}
			out.Status = db.DeliveryStatusDead
		default: // OutcomeRetry
			row, err := q.MarkDeliveryAttemptFailed(ctx, db.MarkDeliveryAttemptFailedParams{
				NextAttemptAt:        a.NextAttemptAt,
				StatusCode:           statusCode,
				LastError:            errText,
				ID:                   a.DeliveryID,
				ExpectedAttemptCount: a.ExpectedAttemptCount,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrStale
			}
			if err != nil {
				return fmt.Errorf("mark failed: %w", err)
			}
			out.Status = row.Status
		}

		out.AttemptNumber = a.ExpectedAttemptCount + 1
		if err := q.InsertDeliveryAttempt(ctx, db.InsertDeliveryAttemptParams{
			ID:              newID(),
			DeliveryID:      a.DeliveryID,
			AttemptNumber:   out.AttemptNumber,
			StartedAt:       a.StartedAt,
			DurationMs:      int32(r.Duration.Milliseconds()),
			StatusCode:      statusCode,
			Error:           errText,
			ResponseSnippet: truncPtr(r.Snippet, 2048),
		}); err != nil {
			return fmt.Errorf("insert attempt: %w", err)
		}

		row, err := q.LockEndpointForUpdate(ctx, a.EndpointID)
		if err != nil {
			return fmt.Errorf("lock endpoint: %w", notFound(err))
		}
		next := b.Record(stateFromRow(row), r.Outcome == delivery.OutcomeSuccess, s.now())
		return persistState(ctx, q, a.EndpointID, next)
	})

	return out, err
}

// DeferDelivery postpones a delivery without consuming an attempt.
func (s *Store) DeferDelivery(ctx context.Context, id uuid.UUID, at time.Time) error {
	return s.q.DeferDelivery(ctx, db.DeferDeliveryParams{
		ID:            id,
		NextAttemptAt: at,
	})
}

func stateFromRow(r db.LockEndpointForUpdateRow) delivery.State {
	st := delivery.State{
		Status:              delivery.Status(r.Status),
		ConsecutiveFailures: int(r.ConsecutiveFailures),
	}
	if r.NextProbeAt != nil {
		st.NextProbeAt = *r.NextProbeAt
	}

	return st
}

func persistState(ctx context.Context, q *db.Queries, id uuid.UUID, st delivery.State) error {
	var probe *time.Time
	if !st.NextProbeAt.IsZero() {
		probe = &st.NextProbeAt
	}
	if err := q.SetEndpointBreakerState(ctx, db.SetEndpointBreakerStateParams{
		ID:                  id,
		Status:              db.EndpointStatus(st.Status),
		ConsecutiveFailures: int32(st.ConsecutiveFailures),
		NextProbeAt:         probe,
	}); err != nil {
		return fmt.Errorf("set breaker state: %w", err)
	}

	return nil
}

func int32Ptr(n int) *int32 {
	if n == 0 {
		return nil
	}

	v := int32(n)
	return &v
}

func errString(err error) *string {
	if err == nil {
		return nil
	}

	return truncPtr(err.Error(), 1000)
}

func truncPtr(s string, max int) *string {
	if s == "" {
		return nil
	}
	if len(s) > max {
		s = strings.ToValidUTF8(s[:max], "")
	}

	return &s
}
