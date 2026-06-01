-- 000003_create_logs_table (rollback)
DROP TRIGGER IF EXISTS trg_otel_logs_tsv ON otel_logs;
DROP FUNCTION IF EXISTS otel_logs_tsv_trigger();
DROP TABLE IF EXISTS otel_logs CASCADE;
