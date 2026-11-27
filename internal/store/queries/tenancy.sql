-- name: CreateTenant :one
INSERT INTO tenants (id, name, rate_limit_per_minute)
VALUES (@id, @name, @rate_limit_per_minute) RETURNING *;

-- name: GetTenant :one
SELECT *
FROM tenants
WHERE id = @id;

-- name: CreateAPIKey :one
INSERT INTO api_keys (id, tenant_id, name, key_prefix, key_hash)
VALUES (@id, @tenant_id, @name, @key_prefix, @key_hash) RETURNING *;

-- name: ListAPIKeys :many
SELECT id, tenant_id, name, key_prefix, created_at, last_used_at, revoked_at
FROM api_keys
WHERE tenant_id = @tenant_id
ORDER BY created_at DESC;

-- name: RevokeAPIKey :execrows
UPDATE api_keys
SET revoked_at = now()
WHERE id = @id
  AND tenant_id = @tenant_id
  AND revoked_at IS NULL;

-- Auth hot path: hash the presented key in Go, resolve to tenant in one query.
-- name: GetAPIKeyAuth :one
SELECT k.id     AS api_key_id,
       t.id     AS tenant_id,
       t.status AS tenant_status,
       t.rate_limit_per_minute
FROM api_keys k
         JOIN tenants t ON t.id = k.tenant_id
WHERE k.key_hash = @key_hash
  AND k.revoked_at IS NULL;

-- Throttled so authentication isn't a write per request.
-- name: TouchAPIKey :exec
UPDATE api_keys
SET last_used_at = now()
WHERE id = @id
  AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');

-- Fixed-window counter. Caller compares the returned count to the tenant's limit.
-- name: IncrementRateLimit :one
INSERT INTO rate_limit_windows (tenant_id, window_start, count)
VALUES (@tenant_id, @window_start, 1) ON CONFLICT (tenant_id, window_start)
DO
UPDATE SET count = rate_limit_windows.count + 1
    RETURNING count;

-- name: DeleteExpiredRateLimitWindows :execrows
DELETE
FROM rate_limit_windows
WHERE window_start < @before;
