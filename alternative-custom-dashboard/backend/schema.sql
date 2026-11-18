CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS devices (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    machine_id  text NOT NULL UNIQUE,          -- stable hardware id reported by the laptop
    name        text NOT NULL,                 -- label the tech team sees
    hostname    text NOT NULL DEFAULT '',
    temple_id   text NOT NULL,                 -- kratos temple id (kratos stays the owner of hubs and temples)
    os          text NOT NULL DEFAULT '',
    specs       jsonb NOT NULL DEFAULT '{}',   -- processor, cores, ram, os version
    active      boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
-- the newest reading is kept on the device row so the hub and temple pages never scan the samples table
ALTER TABLE devices ADD COLUMN IF NOT EXISTS last_seen_at timestamptz;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS latest jsonb NOT NULL DEFAULT '{}';
CREATE INDEX IF NOT EXISTS devices_temple_idx ON devices (temple_id) WHERE active;

-- one row per reading (every 15 seconds per laptop); old rows are deleted automatically
CREATE TABLE IF NOT EXISTS samples (
    device_id   uuid NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    ts          timestamptz NOT NULL,
    cpu         real,    -- percent
    mem         real,    -- percent
    net_up      real,    -- bits per second
    net_down    real,    -- bits per second
    net_err     real,    -- interface errors + dropped packets per minute
    latency_ms  real,    -- how long the agent's last report took to reach us
    PRIMARY KEY (device_id, ts)
);
CREATE INDEX IF NOT EXISTS samples_ts_idx ON samples (ts);
