-- 000004_fix_metrics_primary_key
-- Fix: The metrics table needs to handle multiple data points with the same
-- metric_name, service_name, and timestamp but different labels.
-- Replace the restrictive PRIMARY KEY with a surrogate key (id).

-- Drop the existing primary key constraint by recreating the table structure.
-- Since PG partitioned tables can't ALTER PRIMARY KEY directly, we drop and recreate.

-- Step 1: Drop all partitions and the parent table
DROP TABLE IF EXISTS otel_metrics CASCADE;

-- Step 2: Recreate with id-based primary key
CREATE TABLE IF NOT EXISTS otel_metrics (
    id              BIGSERIAL,
    metric_name     TEXT        NOT NULL,
    metric_type     TEXT        NOT NULL, -- gauge, sum, histogram, summary
    service_name    TEXT        NOT NULL,
    app_id          TEXT,
    timestamp       TIMESTAMPTZ NOT NULL,
    value           DOUBLE PRECISION,
    histogram_min   DOUBLE PRECISION,
    histogram_max   DOUBLE PRECISION,
    histogram_sum   DOUBLE PRECISION,
    histogram_count BIGINT,
    exemplars       JSONB       DEFAULT '[]',
    labels          JSONB       DEFAULT '{}',
    resource        JSONB       DEFAULT '{}',
    created_at      TIMESTAMPTZ DEFAULT NOW(),

    PRIMARY KEY (id, timestamp)
) PARTITION BY RANGE (timestamp);

-- Indexes for common metric query patterns
CREATE INDEX IF NOT EXISTS idx_metrics_name_time
    ON otel_metrics (metric_name, timestamp DESC);

CREATE INDEX IF NOT EXISTS idx_metrics_service_time
    ON otel_metrics (service_name, timestamp DESC);

CREATE INDEX IF NOT EXISTS idx_metrics_app_time
    ON otel_metrics (app_id, timestamp DESC)
    WHERE app_id IS NOT NULL;

-- GIN index on labels for label-based filtering
CREATE INDEX IF NOT EXISTS idx_metrics_labels
    ON otel_metrics USING GIN (labels jsonb_path_ops);

-- Create a default partition
CREATE TABLE IF NOT EXISTS otel_metrics_default
    PARTITION OF otel_metrics DEFAULT;
