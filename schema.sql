-- Add new schema named "public"
CREATE SCHEMA IF NOT EXISTS "public";
-- Set comment to schema: "public"
COMMENT ON SCHEMA "public" IS 'standard public schema';
-- Create "cameras" table
CREATE TABLE "public"."cameras" (
  "id" bigserial NOT NULL,
  "name" text NOT NULL,
  "rtsp_url" text NOT NULL,
  "enabled" boolean NOT NULL DEFAULT true,
  "codec" text NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id")
);
-- Create index "cameras_name_key" to table: "cameras"
CREATE UNIQUE INDEX "cameras_name_key" ON "public"."cameras" ("name");
-- Set comment to column: "name" on table: "cameras"
COMMENT ON COLUMN "public"."cameras"."name" IS 'URL-safe slug; also the mediamtx path name';
-- Set comment to column: "rtsp_url" on table: "cameras"
COMMENT ON COLUMN "public"."cameras"."rtsp_url" IS 'Full RTSP URL including credentials (plaintext v1; TODO: AES-GCM)';
-- Set comment to column: "codec" on table: "cameras"
COMMENT ON COLUMN "public"."cameras"."codec" IS 'Detected codec: h264 | h265 | null';
-- Create "users" table
CREATE TABLE "public"."users" (
  "id" bigserial NOT NULL,
  "username" text NOT NULL,
  "password_hash" text NOT NULL,
  "role" text NOT NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id")
);
-- Create index "users_username_key" to table: "users"
CREATE UNIQUE INDEX "users_username_key" ON "public"."users" ("username");
-- Set comment to column: "role" on table: "users"
COMMENT ON COLUMN "public"."users"."role" IS 'admin | viewer';
-- Create "recordings" table
CREATE TABLE "public"."recordings" (
  "id" bigserial NOT NULL,
  "camera_id" bigint NOT NULL,
  "path" text NOT NULL,
  "started_at" timestamptz NOT NULL,
  "ended_at" timestamptz NULL,
  "size_bytes" bigint NULL,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "thumbnail_path" text NULL,
  "duration_secs" integer NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "recordings_camera_id_fkey" FOREIGN KEY ("camera_id") REFERENCES "public"."cameras" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "recordings_camera_started_idx" to table: "recordings"
CREATE INDEX "recordings_camera_started_idx" ON "public"."recordings" ("camera_id", "started_at");
-- Create index "recordings_path_key" to table: "recordings"
CREATE UNIQUE INDEX "recordings_path_key" ON "public"."recordings" ("path");
-- Set comment to column: "path" on table: "recordings"
COMMENT ON COLUMN "public"."recordings"."path" IS 'Absolute path to the fMP4 segment file';
-- Set comment to column: "started_at" on table: "recordings"
COMMENT ON COLUMN "public"."recordings"."started_at" IS 'Parsed from segment filename';
