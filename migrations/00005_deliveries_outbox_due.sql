-- +goose Up
-- Retries and breaker deferrals are scheduled via next_attempt_at, so the
-- relay's sweep now orders and ranges by it.
DROP INDEX deliveries_outbox_idx;
CREATE INDEX deliveries_outbox_idx ON deliveries (next_attempt_at)
    WHERE queued_at IS NULL AND status = 'pending';

-- +goose Down
DROP INDEX deliveries_outbox_idx;
CREATE INDEX deliveries_outbox_idx ON deliveries (created_at) WHERE queued_at IS NULL;
