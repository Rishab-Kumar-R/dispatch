-- +goose Up
CREATE TYPE endpoint_status AS ENUM ('active', 'degraded', 'disabled');

CREATE TABLE endpoints (
    id                   uuid PRIMARY KEY,
    tenant_id            uuid            NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    url                  text            NOT NULL CHECK (url ~ '^https://'),
    description          text,
    event_types          text[]          NOT NULL DEFAULT '{}',
    status               endpoint_status NOT NULL DEFAULT 'active',
    consecutive_failures integer         NOT NULL DEFAULT 0,
    status_changed_at    timestamptz     NOT NULL DEFAULT now(),
    next_probe_at        timestamptz,
    created_at           timestamptz     NOT NULL DEFAULT now(),
    updated_at           timestamptz     NOT NULL DEFAULT now(),
    deleted_at           timestamptz
);
CREATE INDEX endpoints_tenant_idx ON endpoints (tenant_id, id) WHERE deleted_at IS NULL;

-- Encrypted (recoverable) signing secrets; several live at once during rotation
CREATE TABLE endpoint_secrets (
    id          uuid PRIMARY KEY,
    endpoint_id uuid        NOT NULL REFERENCES endpoints (id) ON DELETE CASCADE,
    ciphertext  bytea       NOT NULL,
    kms_key_id  text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz
);
CREATE INDEX endpoint_secrets_endpoint_idx ON endpoint_secrets (endpoint_id);

-- +goose Down
DROP TABLE endpoint_secrets;
DROP TABLE endpoints;
DROP TYPE endpoint_status;
