-- name: UpsertRecording :one
INSERT INTO recordings (camera_id, path, started_at, ended_at, size_bytes)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (path) DO UPDATE
SET ended_at   = EXCLUDED.ended_at,
    size_bytes = EXCLUDED.size_bytes
RETURNING *;

-- name: GetRecording :one
SELECT * FROM recordings WHERE id = $1;

-- name: ListRecordingsByCamera :many
SELECT * FROM recordings
WHERE camera_id = $1
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR started_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz   IS NULL OR started_at <  sqlc.narg('to_time')::timestamptz)
ORDER BY started_at DESC
LIMIT sqlc.arg('limit')
OFFSET sqlc.arg('offset');

-- name: CountRecordingsByCamera :one
SELECT COUNT(*) FROM recordings
WHERE camera_id = $1
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR started_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz   IS NULL OR started_at <  sqlc.narg('to_time')::timestamptz);

-- name: UpdateRecordingMeta :exec
UPDATE recordings SET thumbnail_path = $2, duration_secs = $3 WHERE id = $1;

-- name: DeleteOldRecordings :many
DELETE FROM recordings
WHERE started_at < now() - make_interval(days => $1)
RETURNING path, thumbnail_path;

-- name: DeleteRecording :exec
DELETE FROM recordings WHERE id = $1;

-- name: ListRecordings :many
SELECT * FROM recordings
WHERE (sqlc.narg('camera_id')::bigint IS NULL OR camera_id = sqlc.narg('camera_id')::bigint)
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR started_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz   IS NULL OR started_at <  sqlc.narg('to_time')::timestamptz)
ORDER BY started_at DESC
LIMIT sqlc.arg('limit')
OFFSET sqlc.arg('offset');

