-- +goose Up
CREATE TYPE delivery_status AS ENUM ('pending', 'succeeded', 'dead');

-- Doubles as the transactional outbox: queued_at IS NULL = not yet on SQS
CREATE TABLE deliveries (
    id               uuid PRIMARY KEY,
    tenant_id        uuid            NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    event_id         uuid            NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    endpoint_id      uuid            NOT NULL REFERENCES endpoints (id) ON DELETE CASCADE,
    status           delivery_status NOT NULL DEFAULT 'pending',
    attempt_count    integer         NOT NULL DEFAULT 0,
    next_attempt_at  timestamptz     NOT NULL DEFAULT now(),
    queued_at        timestamptz,
    delivered_at     timestamptz,
    last_status_code integer,
    last_error       text,
    max_attempts     integer         NOT NULL DEFAULT 8 CHECK (max_attempts > 0),
    created_at       timestamptz     NOT NULL DEFAULT now(),
    updated_at       timestamptz     NOT NULL DEFAULT now(),
    UNIQUE (event_id, endpoint_id)
);
CREATE INDEX deliveries_outbox_idx   ON deliveries (created_at) WHERE queued_at IS NULL;
CREATE INDEX deliveries_endpoint_idx ON deliveries (endpoint_id, id DESC);
CREATE INDEX deliveries_tenant_idx   ON deliveries (tenant_id, id DESC);

-- Append-only history of every attempt
CREATE TABLE delivery_attempts (
    id               uuid PRIMARY KEY,
    delivery_id      uuid        NOT NULL REFERENCES deliveries (id) ON DELETE CASCADE,
    attempt_number   integer     NOT NULL,
    started_at       timestamptz NOT NULL,
    duration_ms      integer     NOT NULL,
    status_code      integer,
    error            text,
    response_snippet text,
    UNIQUE (delivery_id, attempt_number)
);

-- +goose Down
DROP TABLE delivery_attempts;
DROP TABLE deliveries;
DROP TYPE delivery_status;
