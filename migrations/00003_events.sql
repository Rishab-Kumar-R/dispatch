-- +goose Up
CREATE TABLE events (
    id              uuid PRIMARY KEY,
    tenant_id       uuid        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    event_type      text        NOT NULL,   -- tenant-defined, deliberately not an enum
    payload         jsonb       NOT NULL,
    idempotency_key text,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX events_idempotency_uq
    ON events (tenant_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX events_tenant_idx ON events (tenant_id, id DESC);

-- +goose Down
DROP TABLE events;
