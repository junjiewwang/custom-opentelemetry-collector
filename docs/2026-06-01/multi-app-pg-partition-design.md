# PostgreSQL 多应用分区架构设计

> 日期: 2026-06-01  
> 状态: 📋 技术方案（待评审）  
> 前置: Phase 3 PG Provider 核心完成  
> 关联: `docs/2026-06-01/phase3-postgresql-provider.md`

## 1. 背景与目标

### 1.1 现状

当前 PG Provider 采用 **单表 + 时间分区** 策略：

```
otel_traces (PARTITION BY RANGE start_time)
├── otel_traces_p20260601
├── otel_traces_p20260602
└── otel_traces_default
```

- `app_id` 作为普通字段 + 条件索引存储
- 删除某 App 数据需要 `DELETE WHERE app_id = ?`（大量数据时慢、产生 WAL bloat）
- 三种信号的保留策略统一配置在 `PGTableConfig.Retention`

### 1.2 问题

| 问题 | 影响 |
|------|------|
| App 数量增长（100~1000+） | 按 app_id DELETE 性能劣化 |
| 删除某 App 全量数据 | 需要全表扫描 + 大量 WAL |
| 不同 App 保留策略不同 | 统一分区无法按 App 独立清理 |
| 三信号保留时长不同 | Traces 3d, Metrics 30d, Logs 7d 需要独立管理 |

### 1.3 目标

1. **App 级隔离**: 数据按 App 物理隔离，删除操作 O(1)
2. **信号级保留**: Traces/Metrics/Logs 独立配置保留时长和分区粒度
3. **清理操作毫秒级**: 使用 `DROP TABLE`/`DROP SCHEMA` 而非 `DELETE`
4. **与 ES 对齐**: PG 侧 Schema ≈ ES 侧 Index Prefix 的语义
5. **可扩展**: 支持 200+ App，单库不超过 PG 分区性能上限

---

## 2. 架构设计

### 2.1 核心策略：Schema per App + 时间分区

```
Database: otel_storage (单库)
│
├── Schema: public                    ← 全局管理
│   ├── app_registry                  ← App 注册表
│   ├── signal_retention_config       ← 保留策略配置
│   └── schema_migrations             ← 全局 migration 版本
│
├── Schema: app_a1b2c3d4              ← App "payment-service"
│   ├── otel_traces (PARTITION BY RANGE start_time)
│   │   ├── otel_traces_p20260530    retention: 3d
│   │   ├── otel_traces_p20260531
│   │   └── otel_traces_p20260601
│   ├── otel_metrics (PARTITION BY RANGE timestamp)
│   │   ├── otel_metrics_pw20260519  retention: 30d (weekly)
│   │   ├── otel_metrics_pw20260526
│   │   └── otel_metrics_pw20260602
│   └── otel_logs (PARTITION BY RANGE timestamp)
│       ├── otel_logs_p20260525      retention: 7d
│       └── ...
│
├── Schema: app_e5f6g7h8              ← App "user-center"
│   └── (同上结构，独立保留策略)
│
└── Schema: app_default               ← 未注册 App 的 fallback
    └── (同上结构，使用默认保留策略)
```

### 2.2 与 ES 索引策略对齐

| 维度 | ES | PG (新方案) |
|------|----|----|
| App 隔离 | Index prefix: `otel-traces-{date}` + `app_id` 字段 | Schema per App |
| 时间轮转 | 按天创建新 Index | 按天/周创建新 Partition |
| 删除 App | DELETE by query (app_id=X) | `DROP SCHEMA CASCADE` (瞬间) |
| 删除某天 | `DELETE /otel-traces-2026.06.01` | `DROP TABLE otel_traces_p20260601` |
| 保留策略 | ILM per index pattern | Cron DROP expired partitions |
| App 查询 | `term: {app_id: X}` | 路由到对应 schema |

### 2.3 分区策略

**关键原则**: 清理粒度 = 分区粒度，`DROP PARTITION` 是原子操作。

| 保留天数 | 推荐分区粒度 | 分区命名格式 | 最大分区数/表 |
|---------|------------|-------------|-------------|
| 1-7 天 | 1 day | `_p20260601` | 8-9 |
| 8-30 天 | 7 days | `_pw20260602` (week start) | 5-6 |
| 31-90 天 | 7 days | `_pw20260602` | 14-15 |
| 91+ 天 | 30 days | `_pm202606` (month) | 4-5 |

### 2.4 容量评估

```
假设: 200 个 App, 默认策略

每个 App 的分区数:
  traces:  7天 / 1天 + 2 (next + default) = 9
  metrics: 30天 / 7天 + 2                 = 7
  logs:    14天 / 1天 + 2                 = 16
  
单 App 总表数: 3 parent + 32 partitions = 35

200 App 总: 200 × 35 = 7,000 个表

PG 15+ 在 10K~20K 表内性能稳定 → ✅ 安全

上限: ~500 App (单库), 超过后考虑分库
```

---

## 3. 数据模型

### 3.1 全局管理表 (public schema)

```sql
-- Migration 005: Multi-app isolation support

-- App 注册表
CREATE TABLE public.app_registry (
    app_id          TEXT PRIMARY KEY,
    schema_name     TEXT UNIQUE NOT NULL,
    display_name    TEXT,
    status          TEXT NOT NULL DEFAULT 'active',  -- active/suspended/archived
    created_at      TIMESTAMPTZ DEFAULT NOW(),
    updated_at      TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_app_registry_status ON public.app_registry (status);

-- 信号保留策略配置 (per app × per signal)
CREATE TABLE public.signal_retention_config (
    app_id              TEXT NOT NULL REFERENCES public.app_registry(app_id) ON DELETE CASCADE,
    signal_type         TEXT NOT NULL,  -- 'traces' / 'metrics' / 'logs'
    retention_days      INT NOT NULL,
    partition_interval  TEXT NOT NULL,  -- '1d' / '7d' / '30d'
    
    PRIMARY KEY (app_id, signal_type)
);

-- 默认保留策略 (新 App 继承)
CREATE TABLE public.default_retention_config (
    signal_type         TEXT PRIMARY KEY,
    retention_days      INT NOT NULL,
    partition_interval  TEXT NOT NULL
);

INSERT INTO public.default_retention_config VALUES
    ('traces',  7,   '1d'),
    ('metrics', 30,  '7d'),
    ('logs',    14,  '1d');
```

### 3.2 App Schema 内的表结构

每个 App Schema 内的表结构与当前 migration 001-004 **完全一致**，唯一区别是：
- 表创建在各自 schema 下而非 public
- `app_id` 列保留（用于数据校验/审计），但不再作为查询过滤条件
- 索引 `idx_*_app_time` 不再需要（schema 隔离已替代）

---

## 4. 核心实现

### 4.1 新增文件清单

```
extension/observabilitystorageext/provider/postgresql/
├── schema_manager.go          ← Schema 生命周期管理
├── schema_manager_test.go
├── retention_manager.go       ← 保留策略执行
├── retention_manager_test.go
├── multi_app_writer.go        ← 多应用写入路由
├── multi_app_reader.go        ← 多应用读取路由
├── migrations/
│   └── 000005_multi_app_isolation.up.sql
│   └── 000005_multi_app_isolation.down.sql
└── schema_migrations/         ← App schema 内部的 migration SQL
    ├── 000001_create_tables.up.sql    (合并 001-004)
    └── 000001_create_tables.down.sql
```

### 4.2 SchemaManager

```go
// schema_manager.go

package postgresql

import (
    "context"
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "sync"

    "go.uber.org/zap"
)

// SchemaManager manages per-app schema lifecycle.
type SchemaManager struct {
    client   *Client
    logger   *zap.Logger
    cache    sync.Map  // appID -> schemaName
}

func NewSchemaManager(client *Client, logger *zap.Logger) *SchemaManager {
    return &SchemaManager{
        client: client,
        logger: logger.Named("schema-mgr"),
    }
}

// EnsureAppSchema ensures the schema for the given app exists with all required tables.
// Returns the schema name. This method is idempotent and safe for concurrent calls.
func (m *SchemaManager) EnsureAppSchema(ctx context.Context, appID string) (string, error) {
    // Fast path: check cache
    if schema, ok := m.cache.Load(appID); ok {
        return schema.(string), nil
    }

    schemaName := AppSchemaName(appID)

    // 1. Create schema (idempotent)
    _, err := m.client.Exec(ctx,
        fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteIdentifier(schemaName)))
    if err != nil {
        return "", fmt.Errorf("create schema %s: %w", schemaName, err)
    }

    // 2. Create tables within the schema (idempotent, using schema-specific migrations)
    if err := m.ensureTablesInSchema(ctx, schemaName); err != nil {
        return "", fmt.Errorf("ensure tables in %s: %w", schemaName, err)
    }

    // 3. Register app (upsert)
    _, err = m.client.Exec(ctx, `
        INSERT INTO public.app_registry (app_id, schema_name, status)
        VALUES ($1, $2, 'active')
        ON CONFLICT (app_id) DO UPDATE SET updated_at = NOW()`,
        appID, schemaName)
    if err != nil {
        return "", fmt.Errorf("register app %s: %w", appID, err)
    }

    // 4. Initialize default retention config (if not exists)
    if err := m.ensureRetentionConfig(ctx, appID); err != nil {
        m.logger.Warn("Failed to set default retention, using system defaults",
            zap.String("app_id", appID), zap.Error(err))
    }

    m.cache.Store(appID, schemaName)
    m.logger.Info("App schema ready",
        zap.String("app_id", appID),
        zap.String("schema", schemaName))
    return schemaName, nil
}

// AppSchemaName generates a safe schema name from app_id.
// Format: "app_{first 8 hex chars of sha256(appID)}"
// This ensures: fixed length, no special chars, deterministic.
func AppSchemaName(appID string) string {
    if appID == "" {
        return "app_default"
    }
    h := sha256.Sum256([]byte(appID))
    return fmt.Sprintf("app_%s", hex.EncodeToString(h[:])[:8])
}

// ResolveSchema returns the schema name for an app (from cache or DB).
func (m *SchemaManager) ResolveSchema(ctx context.Context, appID string) (string, error) {
    if appID == "" {
        return "app_default", nil
    }
    if schema, ok := m.cache.Load(appID); ok {
        return schema.(string), nil
    }
    // Query from registry
    var schemaName string
    err := m.client.QueryRow(ctx,
        "SELECT schema_name FROM public.app_registry WHERE app_id = $1 AND status = 'active'",
        appID).Scan(&schemaName)
    if err != nil {
        return "", fmt.Errorf("resolve schema for app %s: %w", appID, err)
    }
    m.cache.Store(appID, schemaName)
    return schemaName, nil
}

// DropAppSchema removes all data for an app (millisecond operation regardless of data size).
func (m *SchemaManager) DropAppSchema(ctx context.Context, appID string) error {
    schemaName := AppSchemaName(appID)

    // DROP SCHEMA CASCADE — removes all tables, indexes, partitions instantly
    _, err := m.client.Exec(ctx,
        fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", quoteIdentifier(schemaName)))
    if err != nil {
        return fmt.Errorf("drop schema %s: %w", schemaName, err)
    }

    // Remove from registry
    _, err = m.client.Exec(ctx,
        "DELETE FROM public.app_registry WHERE app_id = $1", appID)
    if err != nil {
        return fmt.Errorf("unregister app %s: %w", appID, err)
    }

    m.cache.Delete(appID)
    m.logger.Info("App schema dropped", zap.String("app_id", appID), zap.String("schema", schemaName))
    return nil
}

// ListActiveApps returns all active app registrations.
func (m *SchemaManager) ListActiveApps(ctx context.Context) ([]AppRegistration, error) {
    rows, err := m.client.Query(ctx,
        "SELECT app_id, schema_name, display_name, created_at FROM public.app_registry WHERE status = 'active'")
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var apps []AppRegistration
    for rows.Next() {
        var app AppRegistration
        if err := rows.Scan(&app.AppID, &app.SchemaName, &app.DisplayName, &app.CreatedAt); err != nil {
            continue
        }
        apps = append(apps, app)
    }
    return apps, nil
}

// ensureTablesInSchema creates the OTEL tables within the given schema.
func (m *SchemaManager) ensureTablesInSchema(ctx context.Context, schemaName string) error {
    // Set search_path to target schema
    _, err := m.client.Exec(ctx,
        fmt.Sprintf("SET LOCAL search_path TO %s", quoteIdentifier(schemaName)))
    if err != nil {
        return err
    }
    // Run schema-specific migrations (traces, metrics, logs tables)
    // Uses embedded SQL from schema_migrations/ directory
    return m.runSchemaMigrations(ctx, schemaName)
}

// ensureRetentionConfig copies default retention config for a new app.
func (m *SchemaManager) ensureRetentionConfig(ctx context.Context, appID string) error {
    _, err := m.client.Exec(ctx, `
        INSERT INTO public.signal_retention_config (app_id, signal_type, retention_days, partition_interval)
        SELECT $1, signal_type, retention_days, partition_interval
        FROM public.default_retention_config
        ON CONFLICT DO NOTHING`, appID)
    return err
}
```

### 4.3 RetentionManager

```go
// retention_manager.go

package postgresql

import (
    "context"
    "fmt"
    "regexp"
    "strconv"
    "time"

    "go.uber.org/zap"
)

// SignalRetention holds the retention config for one signal of one app.
type SignalRetention struct {
    AppID             string
    SchemaName        string
    SignalType         string // "traces" / "metrics" / "logs"
    RetentionDays     int
    PartitionInterval string // "1d" / "7d" / "30d"
    TableName         string // "otel_traces" / "otel_metrics" / "otel_logs"
}

// RetentionManager handles time-based data cleanup using DROP TABLE.
type RetentionManager struct {
    client *Client
    logger *zap.Logger
}

func NewRetentionManager(client *Client, logger *zap.Logger) *RetentionManager {
    return &RetentionManager{
        client: client,
        logger: logger.Named("retention-mgr"),
    }
}

// EnforceAll loads all retention configs and drops expired partitions.
// Should be called periodically (e.g., every hour by a background goroutine or cron).
func (m *RetentionManager) EnforceAll(ctx context.Context) error {
    retentions, err := m.loadAllRetentions(ctx)
    if err != nil {
        return fmt.Errorf("load retention configs: %w", err)
    }

    now := time.Now().UTC()
    totalDropped := 0

    for _, r := range retentions {
        cutoff := now.AddDate(0, 0, -r.RetentionDays)
        dropped, err := m.dropExpiredPartitions(ctx, r.SchemaName, r.TableName, cutoff)
        if err != nil {
            m.logger.Warn("Failed to drop expired partitions",
                zap.String("app", r.AppID),
                zap.String("signal", r.SignalType),
                zap.Error(err))
            continue
        }
        totalDropped += dropped
    }

    if totalDropped > 0 {
        m.logger.Info("Retention enforcement completed",
            zap.Int("total_dropped", totalDropped))
    }
    return nil
}

// EnforceForApp enforces retention for a specific app.
func (m *RetentionManager) EnforceForApp(ctx context.Context, appID string) error {
    retentions, err := m.loadAppRetentions(ctx, appID)
    if err != nil {
        return err
    }

    now := time.Now().UTC()
    for _, r := range retentions {
        cutoff := now.AddDate(0, 0, -r.RetentionDays)
        dropped, err := m.dropExpiredPartitions(ctx, r.SchemaName, r.TableName, cutoff)
        if err != nil {
            return err
        }
        if dropped > 0 {
            m.logger.Info("Dropped expired partitions",
                zap.String("app", appID),
                zap.String("signal", r.SignalType),
                zap.Int("count", dropped),
                zap.Time("cutoff", cutoff))
        }
    }
    return nil
}

// UpdateRetention updates the retention config for a specific app + signal.
func (m *RetentionManager) UpdateRetention(ctx context.Context, appID, signalType string, retentionDays int, partitionInterval string) error {
    _, err := m.client.Exec(ctx, `
        INSERT INTO public.signal_retention_config (app_id, signal_type, retention_days, partition_interval)
        VALUES ($1, $2, $3, $4)
        ON CONFLICT (app_id, signal_type) DO UPDATE 
        SET retention_days = EXCLUDED.retention_days,
            partition_interval = EXCLUDED.partition_interval`,
        appID, signalType, retentionDays, partitionInterval)
    return err
}

// dropExpiredPartitions drops partitions whose date is before the cutoff.
func (m *RetentionManager) dropExpiredPartitions(ctx context.Context, schema, table string, cutoff time.Time) (int, error) {
    // Find all time-based partitions: "{table}_p{yyyyMMdd}" or "{table}_pw{yyyyMMdd}"
    rows, err := m.client.Query(ctx, `
        SELECT tablename 
        FROM pg_tables 
        WHERE schemaname = $1 
          AND tablename LIKE $2
          AND tablename != $3
        ORDER BY tablename`,
        schema, table+"_%", table+"_default")
    if err != nil {
        return 0, err
    }
    defer rows.Close()

    dropped := 0
    for rows.Next() {
        var partName string
        if err := rows.Scan(&partName); err != nil {
            continue
        }

        partDate, err := parsePartitionDate(partName)
        if err != nil {
            continue // Skip non-date partitions (e.g., _default)
        }

        if partDate.Before(cutoff) {
            _, err := m.client.Exec(ctx,
                fmt.Sprintf("DROP TABLE IF EXISTS %s.%s",
                    quoteIdentifier(schema), quoteIdentifier(partName)))
            if err != nil {
                m.logger.Warn("Failed to drop partition",
                    zap.String("partition", schema+"."+partName),
                    zap.Error(err))
                continue
            }
            dropped++
            m.logger.Debug("Dropped expired partition",
                zap.String("partition", schema+"."+partName),
                zap.Time("cutoff", cutoff))
        }
    }
    return dropped, nil
}

// loadAllRetentions loads retention config for all active apps.
func (m *RetentionManager) loadAllRetentions(ctx context.Context) ([]SignalRetention, error) {
    rows, err := m.client.Query(ctx, `
        SELECT r.app_id, a.schema_name, r.signal_type, r.retention_days, r.partition_interval
        FROM public.signal_retention_config r
        JOIN public.app_registry a ON r.app_id = a.app_id
        WHERE a.status = 'active'`)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var results []SignalRetention
    for rows.Next() {
        var r SignalRetention
        if err := rows.Scan(&r.AppID, &r.SchemaName, &r.SignalType, &r.RetentionDays, &r.PartitionInterval); err != nil {
            continue
        }
        r.TableName = signalTableName(r.SignalType)
        results = append(results, r)
    }
    return results, nil
}

// loadAppRetentions loads retention config for a specific app.
func (m *RetentionManager) loadAppRetentions(ctx context.Context, appID string) ([]SignalRetention, error) {
    rows, err := m.client.Query(ctx, `
        SELECT r.app_id, a.schema_name, r.signal_type, r.retention_days, r.partition_interval
        FROM public.signal_retention_config r
        JOIN public.app_registry a ON r.app_id = a.app_id
        WHERE r.app_id = $1 AND a.status = 'active'`, appID)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var results []SignalRetention
    for rows.Next() {
        var r SignalRetention
        if err := rows.Scan(&r.AppID, &r.SchemaName, &r.SignalType, &r.RetentionDays, &r.PartitionInterval); err != nil {
            continue
        }
        r.TableName = signalTableName(r.SignalType)
        results = append(results, r)
    }
    return results, nil
}

// Helpers

func signalTableName(signalType string) string {
    switch signalType {
    case "traces":  return "otel_traces"
    case "metrics": return "otel_metrics"
    case "logs":    return "otel_logs"
    default:        return ""
    }
}

var partDateRegex = regexp.MustCompile(`_p[w]?(\d{8})$`)

func parsePartitionDate(partName string) (time.Time, error) {
    matches := partDateRegex.FindStringSubmatch(partName)
    if len(matches) < 2 {
        return time.Time{}, fmt.Errorf("no date in partition name: %s", partName)
    }
    return time.Parse("20060102", matches[1])
}

// parsePartitionInterval converts "1d"/"7d"/"30d" to time.Duration.
func parsePartitionInterval(interval string) time.Duration {
    if len(interval) < 2 {
        return 24 * time.Hour // default 1 day
    }
    num, err := strconv.Atoi(interval[:len(interval)-1])
    if err != nil {
        return 24 * time.Hour
    }
    unit := interval[len(interval)-1:]
    switch unit {
    case "d": return time.Duration(num) * 24 * time.Hour
    case "h": return time.Duration(num) * time.Hour
    default:  return 24 * time.Hour
    }
}
```

### 4.4 MultiAppWriter

```go
// multi_app_writer.go

package postgresql

import (
    "context"
    "fmt"
    "sync"

    "github.com/jackc/pgx/v5"
    "go.opentelemetry.io/collector/pdata/pcommon"
    "go.opentelemetry.io/collector/pdata/plog"
    "go.opentelemetry.io/collector/pdata/pmetric"
    "go.opentelemetry.io/collector/pdata/ptrace"
    "go.uber.org/zap"
)

// MultiAppWriter routes write operations to the correct app schema.
type MultiAppWriter struct {
    client        *Client
    schemaManager *SchemaManager
    config        *Config
    logger        *zap.Logger

    // Per-schema writers (lazy-initialized)
    traceWriters  sync.Map  // schemaName -> *TraceWriter
    metricWriters sync.Map  // schemaName -> *MetricWriter
    logWriters    sync.Map  // schemaName -> *LogWriter
}

func NewMultiAppWriter(client *Client, schemaManager *SchemaManager, config *Config, logger *zap.Logger) *MultiAppWriter {
    return &MultiAppWriter{
        client:        client,
        schemaManager: schemaManager,
        config:        config,
        logger:        logger.Named("mt-writer"),
    }
}

// WriteTraces groups traces by app_id and writes to the corresponding schema.
func (w *MultiAppWriter) WriteTraces(ctx context.Context, td ptrace.Traces) error {
    groups := groupTracesByApp(td)
    for appID, traces := range groups {
        schema, err := w.schemaManager.EnsureAppSchema(ctx, appID)
        if err != nil {
            return fmt.Errorf("ensure schema for app %s: %w", appID, err)
        }
        writer := w.getTraceWriter(schema)
        if err := writer.WriteTraces(ctx, traces); err != nil {
            return fmt.Errorf("write traces for app %s: %w", appID, err)
        }
    }
    return nil
}

// WriteMetrics groups metrics by app_id and writes to the corresponding schema.
func (w *MultiAppWriter) WriteMetrics(ctx context.Context, md pmetric.Metrics) error {
    groups := groupMetricsByApp(md)
    for appID, metrics := range groups {
        schema, err := w.schemaManager.EnsureAppSchema(ctx, appID)
        if err != nil {
            return fmt.Errorf("ensure schema for app %s: %w", appID, err)
        }
        writer := w.getMetricWriter(schema)
        if err := writer.WriteMetrics(ctx, metrics); err != nil {
            return fmt.Errorf("write metrics for app %s: %w", appID, err)
        }
    }
    return nil
}

// WriteLogs groups logs by app_id and writes to the corresponding schema.
func (w *MultiAppWriter) WriteLogs(ctx context.Context, ld plog.Logs) error {
    groups := groupLogsByApp(ld)
    for appID, logs := range groups {
        schema, err := w.schemaManager.EnsureAppSchema(ctx, appID)
        if err != nil {
            return fmt.Errorf("ensure schema for app %s: %w", appID, err)
        }
        writer := w.getLogWriter(schema)
        if err := writer.WriteLogs(ctx, logs); err != nil {
            return fmt.Errorf("write logs for app %s: %w", appID, err)
        }
    }
    return nil
}

// FlushAll flushes all active writers across all schemas.
func (w *MultiAppWriter) FlushAll(ctx context.Context) error {
    var errs []error
    w.traceWriters.Range(func(_, val any) bool {
        if err := val.(*TraceWriter).Flush(ctx); err != nil {
            errs = append(errs, err)
        }
        return true
    })
    w.metricWriters.Range(func(_, val any) bool {
        if err := val.(*MetricWriter).Flush(ctx); err != nil {
            errs = append(errs, err)
        }
        return true
    })
    w.logWriters.Range(func(_, val any) bool {
        if err := val.(*LogWriter).Flush(ctx); err != nil {
            errs = append(errs, err)
        }
        return true
    })
    if len(errs) > 0 {
        return fmt.Errorf("flush errors: %v", errs)
    }
    return nil
}

// getTraceWriter returns (or creates) a TraceWriter for the given schema.
func (w *MultiAppWriter) getTraceWriter(schema string) *TraceWriter {
    if writer, ok := w.traceWriters.Load(schema); ok {
        return writer.(*TraceWriter)
    }
    // Create a schema-specific config (table = schema.otel_traces)
    cfg := *w.config
    cfg.Traces.TableName = fmt.Sprintf("%s.%s", schema, w.config.Traces.TableName)
    writer := NewTraceWriter(w.client, &cfg, w.logger)
    writer.Start()
    w.traceWriters.Store(schema, writer)
    return writer
}

// getMetricWriter returns (or creates) a MetricWriter for the given schema.
func (w *MultiAppWriter) getMetricWriter(schema string) *MetricWriter {
    if writer, ok := w.metricWriters.Load(schema); ok {
        return writer.(*MetricWriter)
    }
    cfg := *w.config
    cfg.Metrics.TableName = fmt.Sprintf("%s.%s", schema, w.config.Metrics.TableName)
    writer := NewMetricWriter(w.client, &cfg, w.logger)
    writer.Start()
    w.metricWriters.Store(schema, writer)
    return writer
}

// getLogWriter returns (or creates) a LogWriter for the given schema.
func (w *MultiAppWriter) getLogWriter(schema string) *LogWriter {
    if writer, ok := w.logWriters.Load(schema); ok {
        return writer.(*LogWriter)
    }
    cfg := *w.config
    cfg.Logs.TableName = fmt.Sprintf("%s.%s", schema, w.config.Logs.TableName)
    writer := NewLogWriter(w.client, &cfg, w.logger)
    writer.Start()
    w.logWriters.Store(schema, writer)
    return writer
}

// ═══ Grouping Helpers ═══

func groupTracesByApp(td ptrace.Traces) map[string]ptrace.Traces {
    groups := make(map[string]ptrace.Traces)
    rss := td.ResourceSpans()
    for i := 0; i < rss.Len(); i++ {
        rs := rss.At(i)
        appID := extractAppID(rs.Resource())
        if appID == "" {
            appID = "_default"
        }
        if _, ok := groups[appID]; !ok {
            groups[appID] = ptrace.NewTraces()
        }
        rs.CopyTo(groups[appID].ResourceSpans().AppendEmpty())
    }
    return groups
}

func groupMetricsByApp(md pmetric.Metrics) map[string]pmetric.Metrics {
    groups := make(map[string]pmetric.Metrics)
    rms := md.ResourceMetrics()
    for i := 0; i < rms.Len(); i++ {
        rm := rms.At(i)
        appID := extractAppIDFromResource(rm.Resource())
        if appID == "" {
            appID = "_default"
        }
        if _, ok := groups[appID]; !ok {
            groups[appID] = pmetric.NewMetrics()
        }
        rm.CopyTo(groups[appID].ResourceMetrics().AppendEmpty())
    }
    return groups
}

func groupLogsByApp(ld plog.Logs) map[string]plog.Logs {
    groups := make(map[string]plog.Logs)
    rls := ld.ResourceLogs()
    for i := 0; i < rls.Len(); i++ {
        rl := rls.At(i)
        appID := extractAppIDFromResource(rl.Resource())
        if appID == "" {
            appID = "_default"
        }
        if _, ok := groups[appID]; !ok {
            groups[appID] = plog.NewLogs()
        }
        rl.CopyTo(groups[appID].ResourceLogs().AppendEmpty())
    }
    return groups
}

func extractAppIDFromResource(resource pcommon.Resource) string {
    if v, ok := resource.Attributes().Get("app_id"); ok {
        return v.AsString()
    }
    if v, ok := resource.Attributes().Get("app.id"); ok {
        return v.AsString()
    }
    return ""
}
```

### 4.5 MultiAppReader

```go
// multi_app_reader.go

package postgresql

import (
    "context"
    "fmt"

    "go.uber.org/zap"
)

// MultiAppReader routes read operations to the correct app schema.
type MultiAppReader struct {
    client        *Client
    schemaManager *SchemaManager
    config        *Config
    logger        *zap.Logger
}

func NewMultiAppReader(client *Client, schemaManager *SchemaManager, config *Config, logger *zap.Logger) *MultiAppReader {
    return &MultiAppReader{
        client:        client,
        schemaManager: schemaManager,
        config:        config,
        logger:        logger.Named("mt-reader"),
    }
}

// TraceReaderForApp returns a TraceReader scoped to a specific app's schema.
func (r *MultiAppReader) TraceReaderForApp(ctx context.Context, appID string) (*TraceReader, error) {
    schema, err := r.schemaManager.ResolveSchema(ctx, appID)
    if err != nil {
        return nil, fmt.Errorf("resolve schema for app %s: %w", appID, err)
    }
    cfg := *r.config
    cfg.Traces.TableName = fmt.Sprintf("%s.%s", schema, r.config.Traces.TableName)
    return NewTraceReader(r.client, &cfg, r.logger), nil
}

// MetricReaderForApp returns a MetricReader scoped to a specific app's schema.
func (r *MultiAppReader) MetricReaderForApp(ctx context.Context, appID string) (*MetricReader, error) {
    schema, err := r.schemaManager.ResolveSchema(ctx, appID)
    if err != nil {
        return nil, fmt.Errorf("resolve schema for app %s: %w", appID, err)
    }
    cfg := *r.config
    cfg.Metrics.TableName = fmt.Sprintf("%s.%s", schema, r.config.Metrics.TableName)
    return NewMetricReader(r.client, &cfg, r.logger), nil
}

// LogReaderForApp returns a LogReader scoped to a specific app's schema.
func (r *MultiAppReader) LogReaderForApp(ctx context.Context, appID string) (*LogReader, error) {
    schema, err := r.schemaManager.ResolveSchema(ctx, appID)
    if err != nil {
        return nil, fmt.Errorf("resolve schema for app %s: %w", appID, err)
    }
    cfg := *r.config
    cfg.Logs.TableName = fmt.Sprintf("%s.%s", schema, r.config.Logs.TableName)
    return NewLogReader(r.client, &cfg, r.logger), nil
}

// AdminForApp returns an Admin scoped to a specific app's schema.
func (r *MultiAppReader) AdminForApp(ctx context.Context, appID string) (*Admin, error) {
    schema, err := r.schemaManager.ResolveSchema(ctx, appID)
    if err != nil {
        return nil, fmt.Errorf("resolve schema for app %s: %w", appID, err)
    }
    cfg := *r.config
    cfg.Traces.TableName = fmt.Sprintf("%s.%s", schema, r.config.Traces.TableName)
    cfg.Metrics.TableName = fmt.Sprintf("%s.%s", schema, r.config.Metrics.TableName)
    cfg.Logs.TableName = fmt.Sprintf("%s.%s", schema, r.config.Logs.TableName)
    return NewAdmin(r.client, &cfg, r.logger, false), nil
}
```

### 4.6 Config 层扩展

```go
// 新增到 extension/observabilitystorageext/config.go

// MultiAppConfig holds multi-app isolation specific configuration.
type MultiAppConfig struct {
    // Enabled activates schema-per-app isolation.
    Enabled bool `mapstructure:"enabled"`

    // DefaultRetention is the default retention policy for new apps.
    DefaultRetention MultiAppRetention `mapstructure:"default_retention"`

    // RetentionCheckInterval is how often the retention cron runs.
    RetentionCheckInterval time.Duration `mapstructure:"retention_check_interval"`
}

// MultiAppRetention holds per-signal default retention config.
type MultiAppRetention struct {
    Traces  SignalRetentionConfig `mapstructure:"traces"`
    Metrics SignalRetentionConfig `mapstructure:"metrics"`
    Logs    SignalRetentionConfig `mapstructure:"logs"`
}

// SignalRetentionConfig holds retention for one signal type.
type SignalRetentionConfig struct {
    Retention         time.Duration `mapstructure:"retention"`
    PartitionInterval time.Duration `mapstructure:"partition_interval"`
}
```

---

## 5. 配置示例

### 5.1 YAML 配置

```yaml
observability_storage:
  type: "postgresql"
  postgresql:
    dsn: "postgres://admin:****@pg-host:5432/otel_storage?sslmode=disable"
    max_conns: 30
    min_conns: 5
    batch_size: 5000
    flush_interval: 3s
    
    # 多应用隔离模式
    multi_app:
      enabled: true
      retention_check_interval: 1h  # 每小时检查过期分区
      
      # 新 App 默认保留策略
      default_retention:
        traces:
          retention: 168h      # 7 天
          partition_interval: 24h
        metrics:
          retention: 720h      # 30 天
          partition_interval: 168h   # 7 天一个分区
        logs:
          retention: 336h      # 14 天
          partition_interval: 24h
```

### 5.2 运行时 API 动态配置

```
# 修改某 App 的保留策略
POST /api/admin/retention
{
  "app_id": "payment-service",
  "traces": { "retention_days": 3 },
  "metrics": { "retention_days": 90 },
  "logs": { "retention_days": 7 }
}

# 删除某 App 全量数据
DELETE /api/admin/apps/payment-service/data
→ DROP SCHEMA app_a1b2c3d4 CASCADE (~ms)

# 查看 App 保留配置
GET /api/admin/apps/payment-service/retention
→ { "traces": {"retention_days": 3, "partition_interval": "1d"}, ... }
```

---

## 6. 实施计划

### Phase 4.1: 基础设施 (Schema Manager + Migration)

| 步骤 | 内容 | 预计耗时 |
|------|------|----------|
| 1 | 编写 Migration 005: 创建管理表 (`app_registry`, `signal_retention_config`) | 0.5h |
| 2 | 实现 `SchemaManager` (EnsureAppSchema, DropAppSchema, ResolveSchema) | 2h |
| 3 | 实现 schema 内部 migration (合并 001-004 为单文件) | 1h |
| 4 | 单元测试 (Schema 创建/删除/幂等性) | 1h |

### Phase 4.2: 写入路由 (MultiAppWriter)

| 步骤 | 内容 | 预计耗时 |
|------|------|----------|
| 1 | 实现 `MultiAppWriter` (groupByApp + schema routing) | 2h |
| 2 | 改造 `Provider.Start()` 支持 multi_app 模式 | 1h |
| 3 | Per-schema partition 创建逻辑 | 1h |
| 4 | 集成测试 (多 App 写入 + 验证 schema 隔离) | 1.5h |

### Phase 4.3: 读取路由 (MultiAppReader)

| 步骤 | 内容 | 预计耗时 |
|------|------|----------|
| 1 | 实现 `MultiAppReader` (ForApp 方法) | 1.5h |
| 2 | 适配 Extension 层 GetXxxReader (增加 appID 参数) | 1h |
| 3 | 集成测试 (跨 schema 读取验证) | 1h |

### Phase 4.4: 保留策略 (RetentionManager)

| 步骤 | 内容 | 预计耗时 |
|------|------|----------|
| 1 | 实现 `RetentionManager` (EnforceAll, EnforceForApp) | 2h |
| 2 | 后台 cron goroutine (周期执行) | 0.5h |
| 3 | Admin API (UpdateRetention, GetRetention) | 1h |
| 4 | 集成测试 (创建分区 → 设置保留 → 验证 DROP) | 1.5h |

### Phase 4.5: 向后兼容 + 迁移

| 步骤 | 内容 | 预计耗时 |
|------|------|----------|
| 1 | `multi_app.enabled = false` 时完全走旧逻辑 | 0.5h |
| 2 | 数据迁移工具: 从旧单表按 app_id 拆分到对应 schema | 2h |
| 3 | Feature flag + 灰度切换 | 0.5h |

**总计**: ~20h 开发 + 测试

---

## 7. 风险与缓解

| 风险 | 影响 | 缓解措施 |
|------|------|----------|
| Schema 数量增长过快 | pg_class 膨胀，查询规划变慢 | 监控 pg_class 行数，500+ App 时告警 |
| 写入时 Schema 创建竞争 | 并发写入同一新 App | `CREATE SCHEMA IF NOT EXISTS` 幂等 + sync.Map 缓存 |
| Per-schema Writer 内存消耗 | 每个 schema 3 个 writer buffer | 设置 idle timeout，长时间不写的 schema 释放 writer |
| 旧数据迁移 | 历史数据在旧表中 | 提供一次性迁移工具，旧表保留为 `legacy_*` |
| CopyFrom 需要 schema 前缀 | pgx CopyFrom 的 Identifier 需要 schema | `pgx.Identifier{schema, tableName}` 两段式 |

---

## 8. 关键设计决策记录

| 决策 | 选项 | 选择 | 原因 |
|------|------|------|------|
| 隔离粒度 | DB / Schema / Table | **Schema** | 单连接池、DROP CASCADE 快、权限隔离够用 |
| Schema 命名 | `app_{id}` / `app_{hash}` | **app_{hash8}** | 避免特殊字符、固定长度、确定性 |
| 分区维度 | 时间 / App+时间 / 纯 App | **Schema(App) + 时间分区** | 两层解耦，各自独立管理 |
| 保留粒度 | 全局统一 / per-App / per-App-Signal | **per-App-Signal** | 最灵活，符合实际业务需求 |
| 兼容模式 | 强制迁移 / Feature Flag | **Feature Flag** | `multi_app.enabled` 渐进式 |

---

## 9. 验收标准

- [ ] `multi_app.enabled=true` 时，写入自动按 app_id 路由到对应 schema
- [ ] 新 App 首次写入自动创建 schema + 表 + 分区
- [ ] `DROP SCHEMA CASCADE` 可在 <100ms 内删除任意 App 的全量数据
- [ ] 三种信号的保留策略独立配置、独立清理
- [ ] Retention cron 正确 DROP 过期分区（不影响其他 App）
- [ ] `multi_app.enabled=false` 时完全走旧逻辑，无 regression
- [ ] 200 App 场景下 pg_class 行数 < 10,000
- [ ] 写入吞吐不低于单表模式的 80%（Schema 路由开销可控）
