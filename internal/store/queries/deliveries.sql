-- name: CreateDeliveries :many
INSERT INTO deliveries (id, tenant_id, event_id, endpoint_id)
SELECT unnest(@ids::uuid[]), @tenant_id::uuid, @event_id::uuid, unnest(@endpoint_ids::uuid[])
    ON CONFLICT (event_id, endpoint_id) DO NOTHING
RETURNING id;

-- name: ClaimUnqueuedDeliveries :many
SELECT id, tenant_id
FROM deliveries
WHERE queued_at IS NULL
  AND status = 'pending'
  AND created_at < @older_than::timestamptz
ORDER BY created_at
    LIMIT sqlc.arg('batch_size')::int
    FOR UPDATE SKIP LOCKED;

-- name: MarkDeliveriesQueued :exec
UPDATE deliveries
SET queued_at = now(), updated_at = now()
WHERE id = ANY (@ids::uuid[]);

-- Worker: everything needed to send, in one round trip.
-- name: GetDeliveryForSend :one
SELECT d.id, d.tenant_id, d.event_id, d.endpoint_id,
       d.status, d.attempt_count, d.max_attempts,
       e.url AS endpoint_url,
       e.status AS endpoint_status,
       e.deleted_at AS endpoint_deleted_at,
       ev.event_type, ev.payload, ev.created_at AS event_created_at
FROM deliveries d
         JOIN endpoints e ON e.id = d.endpoint_id
         JOIN events ev ON ev.id = d.event_id
WHERE d.id = @id;

-- name: MarkDeliverySucceeded :execrows
UPDATE deliveries
SET status = 'succeeded',
    attempt_count = attempt_count + 1,
    delivered_at = now(),
    last_status_code = sqlc.narg('status_code')::int,
    last_error = NULL,
    updated_at = now()
WHERE id = @id
  AND status = 'pending'
  AND attempt_count = @expected_attempt_count;

-- name: MarkDeliveryAttemptFailed :one
UPDATE deliveries
SET attempt_count = attempt_count + 1,
    status = CASE WHEN attempt_count + 1 >= max_attempts
                      THEN 'dead'::delivery_status
                  ELSE 'pending'::delivery_status END,
    next_attempt_at = @next_attempt_at::timestamptz,
    last_status_code = sqlc.narg('status_code')::int,
    last_error = sqlc.narg('last_error')::text,
    updated_at = now()
WHERE id = @id
  AND status = 'pending'
  AND attempt_count = @expected_attempt_count
    RETURNING id, status, attempt_count;

-- name: InsertDeliveryAttempt :exec
INSERT INTO delivery_attempts
(id, delivery_id, attempt_number, started_at, duration_ms, status_code, error, response_snippet)
VALUES
    (@id, @delivery_id, @attempt_number, @started_at, @duration_ms,
     sqlc.narg('status_code')::int, sqlc.narg('error')::text, sqlc.narg('response_snippet')::text)
    ON CONFLICT (delivery_id, attempt_number) DO NOTHING;

-- name: ReplayDelivery :one
UPDATE deliveries
SET status = 'pending',
    max_attempts = attempt_count + 8,
    next_attempt_at = now(),
    queued_at = NULL,
    delivered_at = NULL,
    last_error = NULL,
    last_status_code = NULL,
    updated_at = now()
WHERE id = @id
  AND tenant_id = @tenant_id
  AND status IN ('dead', 'succeeded')
    RETURNING *;

-- name: GetDelivery :one
SELECT * FROM deliveries WHERE id = @id AND tenant_id = @tenant_id;

-- name: ListDeliveriesForEvent :many
SELECT d.*, e.url AS endpoint_url
FROM deliveries d
         JOIN endpoints e ON e.id = d.endpoint_id
WHERE d.event_id = @event_id AND d.tenant_id = @tenant_id
ORDER BY d.id;

-- name: ListDeliveriesForEndpoint :many
SELECT * FROM deliveries
WHERE endpoint_id = @endpoint_id
  AND tenant_id = @tenant_id
  AND (sqlc.narg('status')::delivery_status IS NULL OR status = sqlc.narg('status')::delivery_status)
  AND (sqlc.narg('before_id')::uuid IS NULL OR id < sqlc.narg('before_id')::uuid)
ORDER BY id DESC
    LIMIT sqlc.arg('page_size')::int;

-- name: ListDeliveryAttempts :many
SELECT a.*
FROM delivery_attempts a
         JOIN deliveries d ON d.id = a.delivery_id
WHERE a.delivery_id = @delivery_id AND d.tenant_id = @tenant_id
ORDER BY a.attempt_number;
