-- name: CreateEndpoint :one
INSERT INTO endpoints (id, tenant_id, url, description, event_types)
VALUES (@id, @tenant_id, @url, @description, @event_types) RETURNING *;

-- name: GetEndpoint :one
SELECT *
FROM endpoints
WHERE id = @id
  AND tenant_id = @tenant_id
  AND deleted_at IS NULL;

-- Cursor pagination on the time-ordered UUIDv7 id.
-- name: ListEndpoints :many
SELECT *
FROM endpoints
WHERE tenant_id = @tenant_id
  AND deleted_at IS NULL
  AND (sqlc.narg('after_id')::uuid IS NULL OR id > sqlc.narg('after_id')::uuid)
ORDER BY id LIMIT sqlc.arg('page_size')::int;

-- Partial update: NULL args leave the column unchanged.
-- name: UpdateEndpoint :one
UPDATE endpoints
SET url         = COALESCE(sqlc.narg('url')::text, url),
    description = COALESCE(sqlc.narg('description')::text, description),
    event_types = COALESCE(sqlc.narg('event_types')::text[], event_types),
    updated_at  = now()
WHERE id = @id
  AND tenant_id = @tenant_id
  AND deleted_at IS NULL RETURNING *;

-- name: SoftDeleteEndpoint :execrows
UPDATE endpoints
SET deleted_at = now(),
    updated_at = now()
WHERE id = @id
  AND tenant_id = @tenant_id
  AND deleted_at IS NULL;

-- Manual re-enable of a degraded/disabled endpoint.
-- name: ResetEndpointBreaker :one
UPDATE endpoints
SET status               = 'active',
    consecutive_failures = 0,
    status_changed_at    = now(),
    next_probe_at        = NULL,
    updated_at           = now()
WHERE id = @id
  AND tenant_id = @tenant_id
  AND deleted_at IS NULL RETURNING *;

-- Fan-out targets for an event. Disabled endpoints are skipped.
-- name: ListMatchingEndpoints :many
SELECT id
FROM endpoints
WHERE tenant_id = @tenant_id
  AND deleted_at IS NULL
  AND status <> 'disabled'
  AND (cardinality(event_types) = 0 OR @event_type::text = ANY (event_types));

-- Circuit breaker: the state machine lives in Go (testable); the DB just
-- serialises concurrent workers via the row lock. Call both in one transaction.
-- name: LockEndpointForUpdate :one
SELECT id, status, consecutive_failures, status_changed_at, next_probe_at
FROM endpoints
WHERE id = @id
    FOR UPDATE;

-- name: SetEndpointBreakerState :exec
UPDATE endpoints
SET status = @status::endpoint_status,
    consecutive_failures = @consecutive_failures,
    status_changed_at = CASE WHEN status <> @status::endpoint_status THEN now() ELSE status_changed_at
END,
    next_probe_at = sqlc.narg('next_probe_at')::timestamptz,
    updated_at = now()
WHERE id = @id;

-- Signing secrets (rotation: expire current ones, then create the new one)
-- name: CreateEndpointSecret :one
INSERT INTO endpoint_secrets (id, endpoint_id, ciphertext, kms_key_id)
VALUES (@id, @endpoint_id, @ciphertext, @kms_key_id) RETURNING *;

-- name: ExpireCurrentEndpointSecrets :exec
UPDATE endpoint_secrets
SET expires_at = @expires_at
WHERE endpoint_id = @endpoint_id
  AND expires_at IS NULL;

-- name: ListActiveEndpointSecrets :many
SELECT *
FROM endpoint_secrets
WHERE endpoint_id = @endpoint_id
  AND (expires_at IS NULL OR expires_at > now())
ORDER BY created_at DESC;

-- name: DeleteExpiredEndpointSecrets :execrows
DELETE
FROM endpoint_secrets
WHERE expires_at IS NOT NULL
  AND expires_at < now();