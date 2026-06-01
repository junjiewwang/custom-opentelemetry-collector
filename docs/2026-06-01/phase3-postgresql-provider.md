# Phase 3 实施进展记录

> 日期: 2026-06-01
> 需求文档: `docs/2026-05-29/unified-observability-storage-design.md`
> 状态: ✅ Phase 3 核心完成

## 已完成任务

### Task 3.1: PG Provider 连接管理 + Schema Migration

**实施内容:**

1. **连接池管理** (`client.go`)
   - 使用 `pgxpool` 创建连接池
   - 配置: MaxConns, MinConns, MaxConnLifetime, MaxConnIdleTime
   - 基础操作: Ping, Exec, Query, QueryRow, Close
   - 辅助: GetVersion, HasTimescaleDB, DatabaseSize, TableSize, TableRowCount

2. **Schema Migration** (`migrate.go`)
   - 使用 `golang-migrate/v4` + embedded SQL files
   - `pgx5://` driver 适配
   - Up/Down/Version 方法
   - 启动时自动 migrate

3. **SQL Migrations** (`migrations/`)
   - `000001_create_traces_table`: 分区表 + service/operation/duration/attributes 索引
   - `000002_create_metrics_table`: 分区表 + GIN labels 索引
   - `000003_create_logs_table`: 分区表 + tsvector 全文搜索 + 自动触发器

### Task 3.2: PG TraceWriter/Reader

**Writer** (`trace_writer.go`):
- `ptrace.Traces` → `traceRow` 转换
- `pgx.CopyFrom` COPY 协议高吞吐写入
- 后台 flush loop + batch size 触发

**Reader** (`trace_reader.go`):
- `SearchTraces`: 动态 WHERE 构建 + GROUP BY trace_id + 分页
- `GetTrace`: 按 trace_id 获取全部 span
- `GetServices`: DISTINCT service_name + COUNT
- `GetOperations`: DISTINCT operation_name + span_kind
- `GetDependencies`: 自连接 parent→child 关系统计

### Task 3.3: PG MetricWriter/Reader

**Writer** (`metric_writer.go`):
- Gauge/Sum/Histogram 三种类型支持
- Labels 作为 JSONB 存储
- COPY 协议写入

**Reader** (`metric_reader.go`):
- `Query`: DISTINCT ON (labels) 取最新值
- `QueryRange`: 时间分桶聚合 (AVG)
- `ListMetricNames`: DISTINCT metric_name
- `ListLabelNames`: `jsonb_object_keys`
- `ListLabelValues`: `labels->>$1`

### Task 3.4: PG LogWriter/Reader

**Writer** (`log_writer.go`):
- `plog.Logs` → `logRow` 转换
- body_tsv 由 DB 触发器自动填充
- COPY 协议写入

**Reader** (`log_reader.go`):
- `SearchLogs`: 全文搜索 `plainto_tsquery('simple', ...)` + 多条件过滤
- `GetLogContext`: 前后 N 行（同 service）
- `ListLogFields`: service 字段统计
- `GetLogStats`: severity/service 分布

### Task 3.5: PG StorageAdmin

**Admin** (`admin.go`):
- `InitSchema`: 委托 Migrator.Up()
- `GetStatus`: PG 版本 + TimescaleDB 检测
- `GetIndicesStats`: 表大小 + 行数
- `SetRetention`: TimescaleDB 用 `add_retention_policy`, 普通 PG 记录策略
- `Purge` / `PurgeByApp`: DELETE WHERE timestamp < $1 [AND app_id = $2]
- `DropOldPartitions`: 自动删除过期分区

### Task 3.1 补充: Extension 层集成

**Config** (`config.go`):
- 新增 `PostgreSQLConfig` + `PGTableConfig` 结构体
- Validate() 支持 `type: "postgresql"`
- ApplyDefaults() 设置 PG 默认值

**Extension** (`extension.go`):
- 引入 `internalProvider` 接口，解耦具体实现
- `createProvider()` 支持 ES/PG 双分支
- `convertPGConfig()` 配置转换
- 所有 GetXxxReader/GetStorageAdmin 按 config.Type 分发

**PG Adapter** (`pg_reader_adapter.go`):
- `pgTraceReaderAdapter`: PG TraceReader → 公共 TraceReader
- `pgMetricReaderAdapter`: PG MetricReader → 公共 MetricReader
- `pgLogReaderAdapter`: PG LogReader → 公共 LogReader
- `pgStorageAdminAdapter`: PG Admin → 公共 StorageAdmin

## 遗留任务

| # | 内容 | 优先级 | 状态 |
|---|------|--------|------|
| 3.6 | HybridProvider (路由: trace→ES, metric→PG, log→ES) | P0 | ✅ 完成 |
| 3.7 | 配置热切换能力 (不重启切换 Provider) | P2 | Phase 3+ |
| 3.8 | PG 实例连通性 + 集成测试 | P1 | ✅ 完成 |
| 3.9 | 集成测试脱敏 + 自动创建数据库 | P1 | ✅ 完成 |

### Task 3.8: PostgreSQL 集成测试

**测试环境:**
- PG 版本: PostgreSQL 15.1 (x86_64-pc-linux-gnu)
- 数据库: otel_test (独立测试库)
- TimescaleDB: 未安装

**测试结果 (全部通过):**

| 测试用例 | 耗时 | 验证内容 |
|----------|------|----------|
| TestIntegration_Connectivity | 0.76s | Ping + Version + DatabaseSize |
| TestIntegration_SchemaMigration | 2.39s | 4个 migration 全部应用, dirty=false |
| TestIntegration_ProviderFullLifecycle | 6.32s | Start → EnsureDB → HealthCheck → Shutdown |
| TestIntegration_TraceWriter | ~2.5s | 2 spans写入并 flush |
| TestIntegration_TraceReader_WriteAndQuery | ~3.1s | SearchTraces/GetTrace/GetServices/GetOperations/GetDependencies |
| TestIntegration_MetricWriter | ~2.5s | 3 metrics写入并 flush |
| TestIntegration_MetricReader_WriteAndQuery | 3.34s | ListMetricNames/Query/QueryRange/ListLabelNames/ListLabelValues |
| TestIntegration_LogWriter | 2.55s | 3 logs写入并 flush |
| TestIntegration_LogReader_WriteAndQuery | ~3.1s | SearchLogs + FTS "connection timeout" + GetLogStats + ListLogFields |
| TestIntegration_Admin_GetStatus | ~2.9s | GetStatus |
| TestIntegration_Admin_GetIndicesStats | 2.98s | GetIndicesStats (4 entries) |
| TestIntegration_PartitionManagement | 3.10s | 5 partitions (today+tomorrow x traces/logs + metrics) |
| TestIntegration_WritePerformance | 3.33s | 50 spans: ~342 spans/sec, SearchTraces: ~279ms |

**修复的问题:**
- **Migration 004**: 修复 metrics 表主键冲突。原主键 `(metric_name, service_name, timestamp)` 无法区分同一时间不同 labels 的数据点，改用 BIGSERIAL `(id, timestamp)` 复合主键。

**性能基线 (远程 PG, 非局域网):**
- 写入吞吐: ~342 spans/sec (单 goroutine, batch=1000, COPY 协议)
- 搜索延迟: ~279ms (SearchTraces, top 20)
- 注: 局域网部署预计 3-5x 提升

### Task 3.9: 集成测试脱敏 + 自动创建数据库

**3.9.1 集成测试脱敏重构**

将集成测试从硬编码凭证改为完全环境变量驱动，与 ES 集成测试保持一致的模式：

- **Gate 机制**: `PG_INTEGRATION_TEST=true` 环境变量控制，未设置则自动 Skip
- **配置来源**: 
  - `PG_DSN` — 完整 DSN (优先级最高)
  - `PG_HOST/PORT/USER/PASSWORD/DATABASE/SSLMODE` — 分字段配置
- **Helper 函数**: `skipIfNoPG(t)`, `envOrDefault()`, `integrationConfig()`, `setupTestProvider(t)`
- **代码风格**: 使用 `zaptest.NewLogger(t)` + `testify/require,assert` + `t.Cleanup()`

**运行方式:**
```bash
# 环境变量方式运行集成测试
PG_INTEGRATION_TEST=true PG_HOST=localhost PG_PORT=5432 \
  PG_USER=postgres PG_PASSWORD=mypass PG_DATABASE=otel_test \
  go test -v -run TestIntegration -timeout 60s \
  ./extension/observabilitystorageext/provider/postgresql/...

# DSN 方式运行
PG_INTEGRATION_TEST=true PG_DSN="postgres://user:pass@host:5432/db?sslmode=disable" \
  go test -v -run TestIntegration -timeout 60s \
  ./extension/observabilitystorageext/provider/postgresql/...

# 不设置 PG_INTEGRATION_TEST 时自动 Skip
go test -v -run TestIntegration ./extension/observabilitystorageext/provider/postgresql/...
# Output: "Skipping integration test: set PG_INTEGRATION_TEST=true to enable"
```

**3.9.2 自动创建数据库 (EnsureDatabase)**

**功能**: 当 Provider 启动时，自动检测目标数据库是否存在，不存在则自动创建。

**实现** (`ensure_database.go`):
- `EnsureDatabase(ctx, dsn, logger)`: 主入口函数
- `parseDSNForDatabase(dsn)`: 从 DSN URL 解析目标数据库名和 system DSN
- `quoteIdentifier(name)`: SQL 标识符安全引用（防注入）
- 连接到 `postgres` 系统数据库 → `SELECT EXISTS(... pg_database ...)` → `CREATE DATABASE`
- 竞态安全: 处理 "already exists" 错误（多进程并发创建场景）

**集成位置**: `Provider.Start()` 中，在 `NewClient()` 之前调用

**单元测试** (`ensure_database_test.go`):
- `TestParseDSNForDatabase`: 6 个用例覆盖各种 DSN 格式
- `TestQuoteIdentifier`: 4 个用例覆盖特殊字符

**验证结果**: 用不存在的数据库 `otel_auto_create_test` 测试，日志确认自动创建成功：
```
INFO  ensure-db  Checking if database exists  {"database": "otel_auto_create_test"}
INFO  ensure-db  Database created successfully  {"database": "otel_auto_create_test"}
```

### Task 3.6: HybridProvider (Composite Pattern)

**核心实现** (`provider/hybrid/provider.go`):
- `Config` 结构体: per-signal routing (Trace/Metric/Log/Admin → "elasticsearch"/"postgresql")
- `Provider` 结构体: 持有可选的 ES + PG 子 provider
- `Start()`: 按需初始化子 provider (仅启动被路由到的后端)
- Write/Flush: 按 signal 路由到对应后端
- `HealthCheck()`: 聚合所有子 provider 健康状态
- Accessor 方法: ESProvider(), PGProvider(), TraceBackend() 等

**Extension 层集成**:
- `config.go`: 新增 `HybridConfig` 结构体 + `Validate()` (检查子配置完整性) + `ApplyDefaults()`
- `extension.go`: 
  - 新增 `hybridProvider` 字段
  - `createProvider()` 新增 `case "hybrid"` 分支
  - 4 个 `getHybridXxx()` 辅助方法 (TraceReader/MetricReader/LogReader/StorageAdmin)
  - 各 GetXxxReader/GetStorageAdmin 新增 `case "hybrid"` 分支

**设计决策**:
- Hybrid Start() 后将子 provider 回写到 extension 的 esProvider/pgProvider 字段，复用已有的 adapter 逻辑
- Validate 在 config 层检查: 若某信号路由到 ES/PG，则对应的子配置必须存在且合法
- 默认路由: trace→ES, metric→PG, log→ES, admin→PG (充分利用两种存储优势)

## 文件变更清单

### 新增文件
- `extension/observabilitystorageext/provider/postgresql/config.go`
- `extension/observabilitystorageext/provider/postgresql/client.go`
- `extension/observabilitystorageext/provider/postgresql/migrate.go`
- `extension/observabilitystorageext/provider/postgresql/provider.go`
- `extension/observabilitystorageext/provider/postgresql/admin.go`
- `extension/observabilitystorageext/provider/postgresql/model.go`
- `extension/observabilitystorageext/provider/postgresql/trace_writer.go`
- `extension/observabilitystorageext/provider/postgresql/trace_reader.go`
- `extension/observabilitystorageext/provider/postgresql/metric_writer.go`
- `extension/observabilitystorageext/provider/postgresql/metric_reader.go`
- `extension/observabilitystorageext/provider/postgresql/log_writer.go`
- `extension/observabilitystorageext/provider/postgresql/log_reader.go`
- `extension/observabilitystorageext/provider/postgresql/integration_test.go`
- `extension/observabilitystorageext/provider/postgresql/ensure_database.go`
- `extension/observabilitystorageext/provider/postgresql/ensure_database_test.go`
- `extension/observabilitystorageext/provider/postgresql/migrations/000001_create_traces_table.up.sql`
- `extension/observabilitystorageext/provider/postgresql/migrations/000001_create_traces_table.down.sql`
- `extension/observabilitystorageext/provider/postgresql/migrations/000002_create_metrics_table.up.sql`
- `extension/observabilitystorageext/provider/postgresql/migrations/000002_create_metrics_table.down.sql`
- `extension/observabilitystorageext/provider/postgresql/migrations/000003_create_logs_table.up.sql`
- `extension/observabilitystorageext/provider/postgresql/migrations/000003_create_logs_table.down.sql`
- `extension/observabilitystorageext/provider/postgresql/migrations/000004_fix_metrics_primary_key.up.sql`
- `extension/observabilitystorageext/provider/postgresql/migrations/000004_fix_metrics_primary_key.down.sql`
- `extension/observabilitystorageext/pg_reader_adapter.go`
- `extension/observabilitystorageext/provider/hybrid/provider.go`

### 修改文件
- `extension/observabilitystorageext/config.go` — 添加 PostgreSQLConfig + HybridConfig
- `extension/observabilitystorageext/extension.go` — 重构为多 provider 支持 (ES/PG/Hybrid)
- `go.mod` / `go.sum` — 添加 pgx v5 + golang-migrate 依赖

## 配置示例

### PostgreSQL 单独模式
```yaml
observability_storage:
  type: "postgresql"
  postgresql:
    dsn: "postgres://postgres_admin:****@21.214.43.93:5432/otel?sslmode=disable"
    max_conns: 20
    min_conns: 5
    batch_size: 5000
    flush_interval: 3s
    use_timescaledb: false
    traces:
      table_name: otel_traces
      partition_interval: 24h
      retention: 168h  # 7 days
    metrics:
      table_name: otel_metrics
      partition_interval: 6h
      retention: 720h  # 30 days
    logs:
      table_name: otel_logs
      partition_interval: 24h
      retention: 336h  # 14 days
```

### Hybrid 混合模式 (推荐)
```yaml
observability_storage:
  type: "hybrid"
  hybrid:
    trace: "elasticsearch"   # ES 擅长全文搜索和链路分析
    metric: "postgresql"     # PG 擅长时序聚合
    log: "elasticsearch"     # ES 擅长日志全文搜索
    admin: "postgresql"      # PG 管理操作
  elasticsearch:
    addresses: ["http://es-node:9200"]
    username: "elastic"
    password: "****"
    batch_size: 5000
    flush_interval: 3s
  postgresql:
    dsn: "postgres://postgres_admin:****@21.214.43.93:5432/otel?sslmode=disable"
    max_conns: 20
    min_conns: 5
    batch_size: 5000
    flush_interval: 3s
```

---

## ES App-Scoped 索引命名优化

> 日期: 2026-06-01
> 状态: ✅ 已完成

### 背景

原始索引命名格式 `{prefix}-{date}` 将所有 app 的数据混合在同一个索引中，导致：
1. **数据隔离差**: 无法按 app 快速定位或删除数据
2. **查询效率低**: 跨 app 查询必须扫描所有文档后 filter
3. **生命周期管理困难**: 不同 app 可能需要不同的保留策略

### 改造方案

索引命名格式改为 `{prefix}-{appID}-{date}`，例如：
- `otel-traces-app001-2026.06.01`
- `otel-metrics-myservice-2026.06.01`
- `otel-logs-_default-2026.06.01`

### 改动清单

#### Writer 端（索引写入路由）

| 文件 | 改动 |
|------|------|
| `model.go` | 新增 `getAppID(resource)` + `sanitizeAppID(id)` + `defaultAppID` 常量 |
| `trace_writer.go` | `getIndexName(appID, t)` → `{prefix}-{appID}-{date}` |
| `metric_writer.go` | `getIndexName(appID, t)` → `{prefix}-{appID}-{date}` |
| `log_writer.go` | `getIndexName(appID, t)` → `{prefix}-{appID}-{date}` |

#### Reader 端（索引查询路由）

| 文件 | 改动 |
|------|------|
| `types_reader.go` | 所有 Query 结构体新增 `AppID string` 字段 |
| `trace_reader.go` | `indexPattern(appID ...string)` 支持 app-scoped 模式 |
| `metric_reader.go` | `indexPattern(appID ...string)` 支持 app-scoped 模式 |
| `log_reader.go` | `indexPattern(appID ...string)` 支持 app-scoped 模式 |

#### Admin 端

| 文件 | 改动 |
|------|------|
| `admin.go` | `GetIndicesStats` 使用配置前缀替代硬编码 `otel-*` |
| `admin.go` | `PurgeByApp` 使用 `{prefix}-{appID}-*` app-scoped 模式 |
| `admin.go` | trace template 添加顶级 `app_id` keyword 字段 |

#### 代码清理

| 文件 | 改动 |
|------|------|
| `metric_writer.go` | 移除 `gaugeToDoc/sumToDoc/histogramToDoc/summaryToDoc` 中冗余的 per-type `app_id` 赋值（已在 `WriteMetrics` 级别统一设置） |

### indexPattern 路由逻辑

```go
// 当 appID 有值时，返回 app-scoped 模式（精确到具体 app 的所有日期索引）
func (r *TraceReader) indexPattern(appID ...string) string {
    if len(appID) > 0 && appID[0] != "" {
        return r.config.Traces.IndexPrefix + "-" + appID[0] + "-*"
    }
    return r.config.Traces.IndexPrefix + "-*"  // 全局搜索
}
```

### 兼容性

- **写入强制**: `app_id`（`app_id` 或 `app.id`）为空时直接拒绝写入，返回错误
- **核心搜索强制**: `SearchTraces`、`Query`、`QueryRange`、`SearchLogs`、`GetLogStats` 要求 `AppID` 不为空，否则返回错误
- **元数据发现允许全局**: `GetServices`、`GetOperations`、`ListMetricNames` 等保持全局查询（用于 UI 下拉框选项发现）
- **精确 ID 查询允许全局**: `GetTrace(traceID)`、`GetLogContext(logID)` 按 ID 精确查找，允许跨 app
- **数据层次**: App → Service → Instance，`app_id` 是最顶层的应用/业务系统标识

### 编译验证

```
$ go build ./...
# 编译通过，无报错
```

### 测试用例修复

App-Scoped 索引命名改造后，修复了所有相关测试用例：

| 测试文件 | 修复内容 |
|----------|----------|
| `trace_writer_test.go` | `getIndexName("payment-app", ts)` 签名更新；E2E 测试添加 `app_id`；期望索引名改为 `otel-traces-payment-app-2026.05.29`；新增 `RejectsWithoutAppID` 测试 |
| `metric_writer_test.go` | `getIndexName("monitor-app", ts)` 签名更新；E2E/MultipleDataPoints 测试添加 `app_id`；`GaugeToDoc` 断言从 `doc["app_id"]` 改为 `resource["app_id"]`；`AppIDExtracted` 改为 E2E 验证；新增 `RejectsWithoutAppID` 测试 |
| `log_writer_test.go` | `getIndexName("auth-app", ts)` 签名更新；E2E/MultipleRecords 测试添加 `app_id`；`LogRecordToDoc_BasicFields` 断言从 `doc["app_id"]` 改为 `resource["app_id"]`；新增 `RejectsWithoutAppID` 测试 |

**运行结果**: 28 个测试全部通过
```
ok  go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/elasticsearch  0.638s
```
