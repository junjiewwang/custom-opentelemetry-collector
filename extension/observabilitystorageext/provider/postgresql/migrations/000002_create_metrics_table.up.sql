-- 000002_create_metrics_table
-- Create the metrics table for storing metric data points.
-- If TimescaleDB is available, the admin will convert this to a hypertable after migration.

CREATE TABLE IF NOT EXISTS otel_metrics (
    metric_name     TEXT        NOT NULL,
    metric_type     TEXT        NOT NULL, -- gauge, sum, histogram, summary
    service_name    TEXT        NOT NULL,
    app_id          TEXT,
    timestamp       TIMESTAMPTZ NOT NULL,
    value           DOUBLE PRECISION,
    -- For histogram: bounds and counts
    histogram_min   DOUBLE PRECISION,
    histogram_max   DOUBLE PRECISION,
    histogram_sum   DOUBLE PRECISION,
    histogram_count BIGINT,
    -- Exemplars (optional)
    exemplars       JSONB       DEFAULT '[]',
    -- Labels as JSONB for flexible filtering
    labels          JSONB       DEFAULT '{}',
    resource        JSONB       DEFAULT '{}',
    created_at      TIMESTAMPTZ DEFAULT NOW(),

    PRIMARY KEY (metric_name, service_name, timestamp)
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
