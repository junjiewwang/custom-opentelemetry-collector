-- 000003_create_logs_table
-- Create the logs table for storing log records.
-- Uses full-text search via tsvector for efficient log searching.

CREATE TABLE IF NOT EXISTS otel_logs (
    id              BIGINT GENERATED ALWAYS AS IDENTITY,
    timestamp       TIMESTAMPTZ NOT NULL,
    observed_time   TIMESTAMPTZ,
    severity_number INT,
    severity_text   TEXT,
    body            TEXT,
    service_name    TEXT        NOT NULL,
    app_id          TEXT,
    trace_id        TEXT,
    span_id         TEXT,
    attributes      JSONB       DEFAULT '{}',
    resource        JSONB       DEFAULT '{}',
    -- Full-text search vector, populated by trigger
    body_tsv        TSVECTOR,
    created_at      TIMESTAMPTZ DEFAULT NOW(),

    PRIMARY KEY (id, timestamp)
) PARTITION BY RANGE (timestamp);

-- Indexes for common log query patterns
CREATE INDEX IF NOT EXISTS idx_logs_service_time
    ON otel_logs (service_name, timestamp DESC);

CREATE INDEX IF NOT EXISTS idx_logs_severity_time
    ON otel_logs (severity_number, timestamp DESC);

CREATE INDEX IF NOT EXISTS idx_logs_trace_id
    ON otel_logs (trace_id, timestamp DESC)
    WHERE trace_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_logs_app_time
    ON otel_logs (app_id, timestamp DESC)
    WHERE app_id IS NOT NULL;

-- GIN index for full-text search on log body
CREATE INDEX IF NOT EXISTS idx_logs_body_fts
    ON otel_logs USING GIN (body_tsv);

-- GIN index on attributes for attribute-based filtering
CREATE INDEX IF NOT EXISTS idx_logs_attributes
    ON otel_logs USING GIN (attributes jsonb_path_ops);

-- Create a default partition
CREATE TABLE IF NOT EXISTS otel_logs_default
    PARTITION OF otel_logs DEFAULT;

-- Trigger function to auto-populate body_tsv on INSERT
CREATE OR REPLACE FUNCTION otel_logs_tsv_trigger() RETURNS TRIGGER AS $$
BEGIN
    NEW.body_tsv := to_tsvector('simple', COALESCE(NEW.body, ''));
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE TRIGGER trg_otel_logs_tsv
    BEFORE INSERT ON otel_logs
    FOR EACH ROW
    EXECUTE FUNCTION otel_logs_tsv_trigger();
