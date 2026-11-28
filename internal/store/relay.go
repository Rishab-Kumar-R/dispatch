package store

import (
	"context"
	"fmt"
	"time"

	"github.com/Rishab-Kumar-R/dispatch/internal/store/db"
	"github.com/google/uuid"
)

type PendingDelivery = db.ClaimUnqueuedDeliveriesRow

// PublishPending claims deliveries that were committed but never queued, hands
// them to publish (which sends them to SQS), and marks them queued, all in one
// transaction. If the commit fails after a successful publish, the rows are
// published again on the next sweep: that's the at-least-once guarantee, and
// the worker's guards make duplicates harmless.
func (s *Store) PublishPending(
	ctx context.Context,
	olderThan time.Time,
	batchSize int,
	publish func(ctx context.Context, batch []PendingDelivery) error,
) (int, error) {
	published := 0
	err := s.InTx(ctx, func(q *db.Queries) error {
		rows, err := q.ClaimUnqueuedDeliveries(ctx, db.ClaimUnqueuedDeliveriesParams{
			OlderThan: olderThan,
			BatchSize: int32(batchSize),
		})
		if err != nil {
			return fmt.Errorf("claim deliveries: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}

		if err := publish(ctx, rows); err != nil {
			return fmt.Errorf("publish: %w", err)
		}

		ids := make([]uuid.UUID, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}

		if err = q.MarkDeliveriesQueued(ctx, ids); err != nil {
			return fmt.Errorf("mark queued: %w", err)
		}

		published = len(rows)
		return nil
	})

	return published, err
}
