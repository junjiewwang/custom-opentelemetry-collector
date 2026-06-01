-- 000004_fix_metrics_primary_key (down)
-- Revert to original schema with composite primary key

DROP TABLE IF EXISTS otel_metrics CASCADE;

CREATE TABLE IF NOT EXISTS otel_metrics (
    metric_name     TEXT        NOT NULL,
    metric_type     TEXT        NOT NULL,
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

    PRIMARY KEY (metric_name, service_name, timestamp)
) PARTITION BY RANGE (timestamp);

CREATE INDEX IF NOT EXISTS idx_metrics_name_time
    ON otel_metrics (metric_name, timestamp DESC);

CREATE INDEX IF NOT EXISTS idx_metrics_service_time
    ON otel_metrics (service_name, timestamp DESC);

CREATE INDEX IF NOT EXISTS idx_metrics_app_time
    ON otel_metrics (app_id, timestamp DESC)
    WHERE app_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_metrics_labels
    ON otel_metrics USING GIN (labels jsonb_path_ops);

CREATE TABLE IF NOT EXISTS otel_metrics_default
    PARTITION OF otel_metrics DEFAULT;
