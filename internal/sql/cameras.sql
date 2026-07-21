-- name: CreateCamera :one
INSERT INTO cameras (name, rtsp_url, enabled, transcode_video)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetCamera :one
SELECT * FROM cameras WHERE id = $1;

-- name: GetCameraByName :one
SELECT * FROM cameras WHERE name = $1;

-- name: ListCameras :many
SELECT * FROM cameras ORDER BY created_at ASC;

-- name: ListEnabledCameras :many
SELECT * FROM cameras WHERE enabled = true ORDER BY created_at ASC;

-- name: UpdateCamera :one
UPDATE cameras
SET name            = $2,
    rtsp_url        = $3,
    enabled         = $4,
    codec           = $5,
    transcode_video = $6,
    updated_at      = now()
WHERE id = $1
RETURNING *;

-- name: UpdateCameraCodec :one
UPDATE cameras
SET codec      = $2,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteCamera :exec
DELETE FROM cameras WHERE id = $1;
