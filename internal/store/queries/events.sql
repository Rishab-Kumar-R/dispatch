-- name: InsertEvent :one
INSERT INTO events (id, tenant_id, event_type, payload, idempotency_key)
VALUES (@id, @tenant_id, @event_type, @payload,
        sqlc.narg('idempotency_key')::text) ON CONFLICT (tenant_id, idempotency_key)
WHERE idempotency_key IS NOT NULL
    DO NOTHING
    RETURNING *;

-- name: GetEventByIdempotencyKey :one
SELECT *
FROM events
WHERE tenant_id = @tenant_id
  AND idempotency_key = @idempotency_key;

-- name: GetEvent :one
SELECT *
FROM events
WHERE id = @id
  AND tenant_id = @tenant_id;

-- name: ListEvents :many
SELECT *
FROM events
WHERE tenant_id = @tenant_id
  AND (sqlc.narg('event_type')::text IS NULL OR event_type = sqlc.narg('event_type')::text)
  AND (sqlc.narg('before_id')::uuid IS NULL OR id < sqlc.narg('before_id')::uuid)
ORDER BY id DESC LIMIT sqlc.arg('page_size')::int;

-- name: DeleteEventsOlderThan :execrows
DELETE
FROM events
WHERE id IN (SELECT e.id
             FROM events e
             WHERE e.created_at < @before
             ORDER BY e.id
    LIMIT sqlc.arg('batch_size'):: int
    );