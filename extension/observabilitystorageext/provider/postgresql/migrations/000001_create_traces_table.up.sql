-- 000001_create_traces_table
-- Create the traces table for storing span data.

CREATE TABLE IF NOT EXISTS otel_traces (
    trace_id        TEXT        NOT NULL,
    span_id         TEXT        NOT NULL,
    parent_span_id  TEXT,
    operation_name  TEXT        NOT NULL,
    service_name    TEXT        NOT NULL,
    span_kind       TEXT,
    status_code     TEXT,
    status_message  TEXT,
    start_time      TIMESTAMPTZ NOT NULL,
    end_time        TIMESTAMPTZ NOT NULL,
    duration_ms     DOUBLE PRECISION NOT NULL,
    app_id          TEXT,
    attributes      JSONB       DEFAULT '{}',
    resource        JSONB       DEFAULT '{}',
    events          JSONB       DEFAULT '[]',
    links           JSONB       DEFAULT '[]',
    created_at      TIMESTAMPTZ DEFAULT NOW(),

    PRIMARY KEY (trace_id, span_id, start_time)
) PARTITION BY RANGE (start_time);

-- Create indexes for common query patterns
CREATE INDEX IF NOT EXISTS idx_traces_service_time
    ON otel_traces (service_name, start_time DESC);

CREATE INDEX IF NOT EXISTS idx_traces_operation_time
    ON otel_traces (service_name, operation_name, start_time DESC);

CREATE INDEX IF NOT EXISTS idx_traces_app_time
    ON otel_traces (app_id, start_time DESC)
    WHERE app_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_traces_duration
    ON otel_traces (duration_ms DESC, start_time DESC);

CREATE INDEX IF NOT EXISTS idx_traces_status
    ON otel_traces (status_code, start_time DESC)
    WHERE status_code = 'ERROR';

-- GIN index on attributes for tag-based queries
CREATE INDEX IF NOT EXISTS idx_traces_attributes
    ON otel_traces USING GIN (attributes jsonb_path_ops);

-- Create a default partition for data that doesn't fit other partitions
-- Actual time-based partitions will be created dynamically by the admin.
CREATE TABLE IF NOT EXISTS otel_traces_default
    PARTITION OF otel_traces DEFAULT;
