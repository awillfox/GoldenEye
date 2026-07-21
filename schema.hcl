table "users" {
  schema = schema.public
  column "id" {
    type = bigserial
    null = false
  }
  column "username" {
    type = text
    null = false
  }
  column "password_hash" {
    type = text
    null = false
  }
  column "role" {
    type    = text
    null    = false
    comment = "admin | viewer"
  }
  column "created_at" {
    type    = timestamptz
    null    = false
    default = sql("now()")
  }
  column "updated_at" {
    type    = timestamptz
    null    = false
    default = sql("now()")
  }
  primary_key {
    columns = [column.id]
  }
  index "users_username_key" {
    unique  = true
    columns = [column.username]
  }
}

table "cameras" {
  schema = schema.public
  column "id" {
    type = bigserial
    null = false
  }
  column "name" {
    type    = text
    null    = false
    comment = "URL-safe slug; also the mediamtx path name"
  }
  column "rtsp_url" {
    type    = text
    null    = false
    comment = "Full RTSP URL including credentials (plaintext v1; TODO: AES-GCM)"
  }
  column "enabled" {
    type    = boolean
    null    = false
    default = true
  }
  column "codec" {
    type    = text
    null    = true
    comment = "Detected codec: h264 | h265 | null"
  }
  column "created_at" {
    type    = timestamptz
    null    = false
    default = sql("now()")
  }
  column "updated_at" {
    type    = timestamptz
    null    = false
    default = sql("now()")
  }
  primary_key {
    columns = [column.id]
  }
  index "cameras_name_key" {
    unique  = true
    columns = [column.name]
  }
}

table "recordings" {
  schema = schema.public
  column "id" {
    type = bigserial
    null = false
  }
  column "camera_id" {
    type = bigint
    null = false
  }
  column "path" {
    type    = text
    null    = false
    comment = "Absolute path to the fMP4 segment file"
  }
  column "started_at" {
    type    = timestamptz
    null    = false
    comment = "Parsed from segment filename"
  }
  column "ended_at" {
    type = timestamptz
    null = true
  }
  column "size_bytes" {
    type = bigint
    null = true
  }
  column "thumbnail_path" {
    type = text
    null = true
  }
  column "duration_secs" {
    type = integer
    null = true
  }
  column "created_at" {
    type    = timestamptz
    null    = false
    default = sql("now()")
  }
  primary_key {
    columns = [column.id]
  }
  foreign_key "recordings_camera_id_fkey" {
    columns     = [column.camera_id]
    ref_columns = [table.cameras.column.id]
    on_delete   = CASCADE
  }
  index "recordings_path_key" {
    unique  = true
    columns = [column.path]
  }
  index "recordings_camera_started_idx" {
    columns = [column.camera_id, column.started_at]
  }
}

schema "public" {}
