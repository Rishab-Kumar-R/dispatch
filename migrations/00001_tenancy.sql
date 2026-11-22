-- +goose Up
CREATE TYPE tenant_status AS ENUM ('active', 'suspended');

CREATE TABLE tenants (
    id                    uuid PRIMARY KEY,
    name                  text          NOT NULL,
    status                tenant_status NOT NULL DEFAULT 'active',
    rate_limit_per_minute integer       NOT NULL DEFAULT 600 CHECK (rate_limit_per_minute > 0),
    created_at            timestamptz   NOT NULL DEFAULT now()
);

CREATE TABLE api_keys (
    id           uuid PRIMARY KEY,
    tenant_id    uuid        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name         text        NOT NULL,
    key_prefix   text        NOT NULL,         -- display only
    key_hash     bytea       NOT NULL UNIQUE,  -- SHA-256 of the full key
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    revoked_at   timestamptz
);
CREATE INDEX api_keys_tenant_idx ON api_keys (tenant_id);

-- Fixed-window counter per tenant
CREATE TABLE rate_limit_windows (
    tenant_id    uuid        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    window_start timestamptz NOT NULL,
    count        integer     NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id, window_start)
);

-- +goose Down
DROP TABLE rate_limit_windows;
DROP TABLE api_keys;
DROP TABLE tenants;
DROP TYPE tenant_status;
