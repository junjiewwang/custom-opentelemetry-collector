# ES 索引模板自动校验与修复（Template Reconciler）设计方案

> 文档创建日期：2026-07-23  
> 状态：方案设计中
> 关联问题：模板推送失败/被覆盖/被删除导致新索引 mapping 错误

---

## 附：LogQL Regex Filter 修复 (2026-07-23 已实施)

### 根因
Grafana `|~ "(?i)order"` 正则过滤器被错误地转换为 ES `match` 查询：
- LogQL: `{service_name="test-java-order-service"} |~ "(?i)order"`
- 旧 ES 查询: `{"match": {"body": {"query": "/(?i)order/", "operator": "and"}}}`
- ES `match` 查询对 `body` (text/standard analyzer) 分词: `["i", "order"]`
- `operator: "and"` 要求两个 token 都匹配 → 0 结果

### 修复方案
1. `LogQuery` 新增 `RegexFilters []string` 和 `NotRegexFilters []string` 字段
2. Evaluator 将 `|~` / `!~` 过滤器路由到新字段（不再进入 `Query`）
3. `buildLogSearchQuery` 对 `RegexFilters` 生成 ES `regexp` 查询（`case_insensitive: true`）
4. `convertLokiRegex()` 转换 PCRE 标志 → ES 参数

### ES 验证结果
| 查询方式 | 结果 | 
|---------|------|
| 旧方式 (match + `"/(?i)order/"`) | total=0 ❌ |
| 新方式 (regexp + `case_insensitive`) | total=1592 ✅ |

### 改动文件
- `extension/observabilitystorageext/types.go` — LogQuery 新增字段
- `extension/observabilitystorageext/provider/elasticsearch/types_reader.go` — ES LogQuery 新增字段
- `extension/adminext/logql/evaluator.go` — 正则过滤器路由到新字段
- `extension/observabilitystorageext/provider/elasticsearch/log_reader.go` — ES regexp 查询构建 + convertLokiRegex
- `extension/observabilitystorageext/reader_adapter.go` — 字段拷贝补全
- `extension/adminext/logql/evaluator_test.go` — 新增 6 个测试
- `extension/observabilitystorageext/provider/elasticsearch/log_reader_test.go` — 新增 convertLokiRegex 测试

---

## 1. 问题陈述

### 1.1 当前架构缺陷

| # | 缺陷 | 后果 |
|---|------|------|
| 1 | `InitSchema` 失败仅打 Warn，不阻塞启动也无重试 | ES 临时不可达时模板丢失，后续写入触发 dynamic mapping |
| 2 | 无模板版本管理 | 无法判断 ES 上的模板是否与代码一致 |
| 3 | 无后台 Reconcile | 运行时被外部删除/覆盖后无法自愈 |
| 4 | 模板只管新索引，不管已有索引 mapping | 旧索引 mapping 漂移后数据写入不报错但查询失败 |
| 5 | Writer 无模板就绪前置检查 | "裸写" 创建的索引使用 dynamic mapping |

### 1.2 目标

- **保证模板正确**：确保 ES 上的索引模板始终与代码定义一致
- **自动修复**：运行时发现不一致时自动修复，无需人工介入
- **已有索引保护**：对关键字段的 mapping 进行校验，发现不可修复的漂移时告警
- **可测试**：所有核心逻辑可通过 mock 进行单元测试

---

## 2. 设计原则

| 原则 | 体现 |
|------|------|
| **SRP** | `TemplateReconciler` 只负责模板生命周期管理，不承担写入/查询职责 |
| **OCP** | 新增信号类型（如 Profile）只需新增 `TemplateSpec`，无需修改 Reconciler 核心逻辑 |
| **DIP** | Reconciler 依赖 `TemplateClient` 接口而非具体 `*Client`，便于单测 mock |
| **ISP** | `TemplateClient` 仅暴露 Get/Put/GetMapping 三个方法，不暴露全量 ES 操作 |
| **高内聚** | 模板定义（Spec）、模板比较（Differ）、修复执行（Reconciler）各自内聚 |
| **低耦合** | Reconciler 通过接口与 ES Client 交互，通过 channel/callback 与 Provider 通信 |
| **健壮性** | 带指数退避重试、circuit breaker、并发安全 |
| **可扩展** | 通过 `ReconcilePolicy` 支持不同策略（仅告警/自动修复/自动修复+reindex） |

---

## 3. 架构设计

### 3.1 组件拆分

```
┌─────────────────────────────────────────────────────────────────┐
│                          Provider                                │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │              TemplateReconciler (核心)                      │  │
│  │  ┌──────────┐  ┌──────────────┐  ┌────────────────────┐  │  │
│  │  │ Registry │  │   Differ     │  │   ReconcileLoop    │  │  │
│  │  │(模板注册) │  │ (差异比较)   │  │   (后台循环)        │  │  │
│  │  └──────────┘  └──────────────┘  └────────────────────┘  │  │
│  └───────────────────────────────┬───────────────────────────┘  │
│                                  │ 依赖                          │
│  ┌───────────────────────────────▼───────────────────────────┐  │
│  │              TemplateClient (接口)                          │  │
│  │   GetIndexTemplate() / PutIndexTemplate() / GetMapping()   │  │
│  └───────────────────────────────┬───────────────────────────┘  │
│                                  │ 实现                          │
│  ┌───────────────────────────────▼───────────────────────────┐  │
│  │              *Client (ES HTTP 客户端)                       │  │
│  └───────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
```

### 3.2 核心类型定义

```go
// TemplateClient 是 Reconciler 对 ES 的最小依赖接口（DIP + ISP）
type TemplateClient interface {
    // GetIndexTemplate 获取已存在的索引模板，不存在返回 nil, nil
    GetIndexTemplate(ctx context.Context, name string) (*IndexTemplateResponse, error)
    // PutIndexTemplate 创建或覆盖索引模板
    PutIndexTemplate(ctx context.Context, name string, template map[string]any) error
    // GetIndexMapping 获取已存在索引的 mapping，索引不存在返回 nil, nil
    GetIndexMapping(ctx context.Context, indexName string) (map[string]any, error)
}

// TemplateSpec 描述一个索引模板的期望状态（Registry 中注册的单元）
type TemplateSpec struct {
    // Name 模板名称（如 "otel-logs"）
    Name string
    // Version 模板版本号，每次代码变更时递增
    Version int
    // Priority ES composable template priority（高优先级覆盖低优先级）
    Priority int
    // Body 完整的模板内容（index_patterns + template.settings + template.mappings）
    Body map[string]any
    // CriticalFields 关键字段及其期望类型，用于已有索引的 mapping 校验
    CriticalFields []FieldSpec
}

// FieldSpec 描述一个关键字段的期望 mapping
type FieldSpec struct {
    // Path 字段路径（如 "timeUnixNano"、"resource.service.name"）
    Path string
    // ExpectedType 期望的 ES 字段类型（如 "long"、"keyword"）
    ExpectedType string
    // MustBeIndexed 是否必须可索引（index != false）
    MustBeIndexed bool
}

// ReconcilePolicy 控制 Reconciler 的修复行为
type ReconcilePolicy int

const (
    // PolicyAlertOnly 仅记录日志和指标，不自动修复
    PolicyAlertOnly ReconcilePolicy = iota
    // PolicyAutoFix 自动修复模板，但不处理已有索引
    PolicyAutoFix
    // PolicyAutoFixWithReindex 自动修复模板 + 对已有索引执行 reindex（危险，默认不启用）
    PolicyAutoFixWithReindex
)

// ReconcileResult 单次 reconcile 的结果
type ReconcileResult struct {
    TemplateName string
    Status       ReconcileStatus
    Message      string
    Drift        []DriftDetail // 如果有漂移，记录详情
}

type ReconcileStatus int

const (
    StatusOK           ReconcileStatus = iota // 模板一致
    StatusFixed                               // 检测到漂移并已修复
    StatusDriftDetected                       // 检测到漂移但未修复（PolicyAlertOnly）
    StatusError                               // reconcile 过程出错
)

// DriftDetail 描述一个具体的漂移
type DriftDetail struct {
    Field    string // 字段路径
    Expected string // 期望值
    Actual   string // 实际值
    Severity string // "critical" | "warning"
}
```

### 3.3 TemplateReconciler 结构

```go
// TemplateReconciler 负责索引模板的生命周期管理
type TemplateReconciler struct {
    client   TemplateClient
    logger   *zap.Logger
    policy   ReconcilePolicy

    // registry 存储所有需要管理的模板 spec
    registry []*TemplateSpec

    // 后台循环控制
    interval time.Duration
    stopCh   chan struct{}
    doneCh   chan struct{}

    // 状态：最近一次 reconcile 结果（并发安全读取）
    mu          sync.RWMutex
    lastResults map[string]*ReconcileResult
    ready       bool // 是否至少成功 reconcile 过一次
}
```

---

## 4. 详细设计

### 4.1 模板注册（Registry）

模板定义从 `admin.go` 的 `createXxxTemplate()` 私有方法中抽取为独立的 `TemplateSpec` 构造函数：

```go
// template_specs.go

// TraceTemplateSpec 构造 trace 索引模板的期望状态
func TraceTemplateSpec(cfg IndexConfig) *TemplateSpec {
    return &TemplateSpec{
        Name:     cfg.IndexPrefix,
        Version:  1, // 每次修改 mapping 时递增
        Priority: 100,
        Body: map[string]any{
            "index_patterns": []string{cfg.IndexPrefix + "-*"},
            "priority":       100,
            "version":        1,
            "template": map[string]any{ /* ... */ },
        },
        CriticalFields: []FieldSpec{
            {Path: "startTimeUnixNano", ExpectedType: "long", MustBeIndexed: true},
            {Path: "endTimeUnixNano", ExpectedType: "long", MustBeIndexed: true},
            {Path: "traceId", ExpectedType: "keyword", MustBeIndexed: true},
            {Path: "spanId", ExpectedType: "keyword", MustBeIndexed: true},
        },
    }
}

// LogTemplateSpec 构造 log 索引模板的期望状态
func LogTemplateSpec(cfg IndexConfig) *TemplateSpec { /* ... */ }

// MetricTemplateSpec 构造 metric 索引模板的期望状态
func MetricTemplateSpec(cfg IndexConfig) *TemplateSpec { /* ... */ }
```

### 4.2 模板差异比较（Differ）

```go
// template_differ.go

// DiffTemplate 比较 ES 上的模板与期望 spec 的差异
// 返回 nil 表示一致
func DiffTemplate(spec *TemplateSpec, actual *IndexTemplateResponse) []DriftDetail {
    var drifts []DriftDetail

    // 1. 版本号比较（快速路径）
    if actual.Version != nil && *actual.Version >= spec.Version {
        return nil // ES 上的版本 >= 代码版本，无需更新
    }

    // 2. 关键字段 mapping 比较
    actualMappings := extractMappings(actual)
    for _, field := range spec.CriticalFields {
        actualType := getFieldType(actualMappings, field.Path)
        if actualType != field.ExpectedType {
            drifts = append(drifts, DriftDetail{
                Field:    field.Path,
                Expected: field.ExpectedType,
                Actual:   actualType,
                Severity: "critical",
            })
        }
    }

    // 3. 如果版本号不匹配，即使字段暂无差异也标记需要更新
    if len(drifts) == 0 {
        drifts = append(drifts, DriftDetail{
            Field:    "_version",
            Expected: fmt.Sprintf("%d", spec.Version),
            Actual:   fmt.Sprintf("%v", actual.Version),
            Severity: "warning",
        })
    }

    return drifts
}
```

### 4.3 Reconcile 核心逻辑

```go
// reconciler.go

// ReconcileOnce 执行一次完整的 reconcile 循环
func (r *TemplateReconciler) ReconcileOnce(ctx context.Context) []ReconcileResult {
    var results []ReconcileResult

    for _, spec := range r.registry {
        result := r.reconcileOne(ctx, spec)
        results = append(results, result)
    }

    // 更新内部状态
    r.mu.Lock()
    for i := range results {
        r.lastResults[results[i].TemplateName] = &results[i]
    }
    allOK := true
    for _, res := range results {
        if res.Status == StatusError {
            allOK = false
            break
        }
    }
    if allOK {
        r.ready = true
    }
    r.mu.Unlock()

    return results
}

func (r *TemplateReconciler) reconcileOne(ctx context.Context, spec *TemplateSpec) ReconcileResult {
    // 1. 获取 ES 上当前模板
    actual, err := r.client.GetIndexTemplate(ctx, spec.Name)
    if err != nil {
        return ReconcileResult{
            TemplateName: spec.Name,
            Status:       StatusError,
            Message:      fmt.Sprintf("failed to get template: %v", err),
        }
    }

    // 2. 模板不存在 → 直接创建
    if actual == nil {
        r.logger.Warn("Index template not found, creating",
            zap.String("template", spec.Name),
            zap.Int("version", spec.Version),
        )
        if r.policy == PolicyAlertOnly {
            return ReconcileResult{
                TemplateName: spec.Name,
                Status:       StatusDriftDetected,
                Message:      "template missing",
            }
        }
        if err := r.client.PutIndexTemplate(ctx, spec.Name, spec.Body); err != nil {
            return ReconcileResult{
                TemplateName: spec.Name,
                Status:       StatusError,
                Message:      fmt.Sprintf("failed to create template: %v", err),
            }
        }
        return ReconcileResult{
            TemplateName: spec.Name,
            Status:       StatusFixed,
            Message:      "template created",
        }
    }

    // 3. 模板存在 → 比较差异
    drifts := DiffTemplate(spec, actual)
    if len(drifts) == 0 {
        return ReconcileResult{
            TemplateName: spec.Name,
            Status:       StatusOK,
        }
    }

    // 4. 有漂移 → 根据策略处理
    r.logger.Warn("Index template drift detected",
        zap.String("template", spec.Name),
        zap.Int("drift_count", len(drifts)),
    )

    if r.policy == PolicyAlertOnly {
        return ReconcileResult{
            TemplateName: spec.Name,
            Status:       StatusDriftDetected,
            Message:      fmt.Sprintf("%d drifts detected", len(drifts)),
            Drift:        drifts,
        }
    }

    // AutoFix: 覆盖模板
    if err := r.client.PutIndexTemplate(ctx, spec.Name, spec.Body); err != nil {
        return ReconcileResult{
            TemplateName: spec.Name,
            Status:       StatusError,
            Message:      fmt.Sprintf("failed to fix template: %v", err),
            Drift:        drifts,
        }
    }

    return ReconcileResult{
        TemplateName: spec.Name,
        Status:       StatusFixed,
        Message:      fmt.Sprintf("fixed %d drifts", len(drifts)),
        Drift:        drifts,
    }
}
```

### 4.4 后台 Reconcile 循环

```go
// Start 启动后台 reconcile 循环
func (r *TemplateReconciler) Start(ctx context.Context) {
    // 立即执行一次（启动时保证模板正确）
    r.ReconcileOnce(ctx)

    go func() {
        ticker := time.NewTicker(r.interval)
        defer ticker.Stop()
        defer close(r.doneCh)

        for {
            select {
            case <-r.stopCh:
                return
            case <-ticker.C:
                r.ReconcileOnce(ctx)
            }
        }
    }()
}

// Stop 停止后台循环
func (r *TemplateReconciler) Stop() {
    close(r.stopCh)
    <-r.doneCh
}

// Ready 返回是否至少成功 reconcile 过一次（Writer 启动前置检查）
func (r *TemplateReconciler) Ready() bool {
    r.mu.RLock()
    defer r.mu.RUnlock()
    return r.ready
}

// LastResults 返回最近一次 reconcile 结果（用于 health check）
func (r *TemplateReconciler) LastResults() map[string]*ReconcileResult {
    r.mu.RLock()
    defer r.mu.RUnlock()
    // 返回副本
    cp := make(map[string]*ReconcileResult, len(r.lastResults))
    for k, v := range r.lastResults {
        cp[k] = v
    }
    return cp
}
```

### 4.5 与 Provider 集成

```go
// provider.go 改造

func (p *Provider) Start(ctx context.Context) error {
    // ... client 初始化 ...

    // 构建模板 Reconciler（替代原有 admin.InitSchema）
    reconciler := NewTemplateReconciler(
        p.client, // 实现 TemplateClient 接口
        p.logger,
        ReconcilerConfig{
            Policy:   PolicyAutoFix,
            Interval: 5 * time.Minute, // 每 5 分钟检查一次
        },
        // 注册所有模板
        TraceTemplateSpec(p.config.Traces),
        LogTemplateSpec(p.config.Logs),
        MetricTemplateSpec(p.config.Metrics),
    )

    // 启动时强制执行一次 reconcile（带重试）
    if err := p.ensureTemplatesWithRetry(ctx, reconciler, 3); err != nil {
        return fmt.Errorf("failed to ensure index templates after retries: %w", err)
    }

    // 启动后台循环
    reconciler.Start(ctx)
    p.reconciler = reconciler

    // ... 初始化 writers/readers ...
}

// ensureTemplatesWithRetry 带重试的启动校验
func (p *Provider) ensureTemplatesWithRetry(ctx context.Context, r *TemplateReconciler, maxRetries int) error {
    var lastErr error
    for i := 0; i <= maxRetries; i++ {
        results := r.ReconcileOnce(ctx)
        allOK := true
        for _, res := range results {
            if res.Status == StatusError {
                allOK = false
                lastErr = fmt.Errorf("template %s: %s", res.TemplateName, res.Message)
            }
        }
        if allOK {
            return nil
        }
        if i < maxRetries {
            backoff := time.Duration(1<<uint(i)) * time.Second // 1s, 2s, 4s
            p.logger.Warn("Template reconcile failed, retrying",
                zap.Int("attempt", i+1),
                zap.Duration("backoff", backoff),
                zap.Error(lastErr),
            )
            time.Sleep(backoff)
        }
    }
    return lastErr
}
```

### 4.6 已有索引 Mapping 校验（可选增强）

```go
// index_mapping_checker.go

// CheckActiveIndexMappings 校验当前活跃索引（今日+昨日）的 mapping 是否正确
// 这是 ReconcileOnce 的可选增强步骤
func (r *TemplateReconciler) CheckActiveIndexMappings(ctx context.Context) []MappingIssue {
    var issues []MappingIssue

    for _, spec := range r.registry {
        // 获取最近的索引（按日期匹配）
        // 检查 CriticalFields 的类型是否正确
        // 如果发现不一致 → 记录 issue（不自动 reindex，风险太大）
    }

    return issues
}

type MappingIssue struct {
    IndexName string
    Field     string
    Expected  string
    Actual    string
    Severity  string
}
```

---

## 5. 文件结构

```
extension/observabilitystorageext/provider/elasticsearch/
├── admin.go                    # [修改] 移除 createXxxTemplate()，改为调用 Reconciler
├── client_admin.go             # [修改] 新增 GetIndexTemplate() / GetIndexMapping()
├── config.go                   # [不变]
├── fields.go                   # [不变]
├── provider.go                 # [修改] 集成 Reconciler 替代 InitSchema 直接调用
├── template_spec.go            # [新建] TemplateSpec 定义 + 构造函数
├── template_differ.go          # [新建] DiffTemplate() 差异比较逻辑
├── template_reconciler.go      # [新建] Reconciler 核心 + 后台循环
├── template_reconciler_test.go # [新建] 单元测试（mock TemplateClient）
├── template_differ_test.go     # [新建] Differ 单元测试
└── template_spec_test.go       # [新建] Spec 构造函数测试
```

---

## 6. 接口与可测试性

### 6.1 Mock 设计

```go
// 测试用 mock
type mockTemplateClient struct {
    templates map[string]*IndexTemplateResponse
    putCalls  []putCall
    putErr    error
    getErr    error
}

func (m *mockTemplateClient) GetIndexTemplate(ctx context.Context, name string) (*IndexTemplateResponse, error) {
    if m.getErr != nil {
        return nil, m.getErr
    }
    return m.templates[name], nil
}

func (m *mockTemplateClient) PutIndexTemplate(ctx context.Context, name string, body map[string]any) error {
    m.putCalls = append(m.putCalls, putCall{name: name, body: body})
    return m.putErr
}
```

### 6.2 关键测试用例

| 测试场景 | 输入 | 期望行为 |
|---------|------|---------|
| 模板不存在 | `GetIndexTemplate` 返回 nil | 调用 `PutIndexTemplate` 创建 |
| 模板版本一致 | version 相同 | 不调用 Put，返回 StatusOK |
| 模板版本落后 | ES version < 代码 version | 调用 Put 覆盖，返回 StatusFixed |
| 关键字段类型漂移 | `timeUnixNano: double` vs 期望 `long` | 返回 critical drift |
| ES 不可达 | Get 返回 error | 返回 StatusError，不 panic |
| PolicyAlertOnly 策略 | 发现漂移 | 只记录不修复 |
| 并发安全 | 多 goroutine 调用 Ready() | 无 race condition |
| 重试成功 | 第 1 次失败第 2 次成功 | ensureTemplatesWithRetry 返回 nil |

---

## 7. 配置设计

在 `Config` 中新增 Reconciler 配置段：

```go
type ReconcilerConfig struct {
    // Enabled 是否启用后台 reconcile（默认 true）
    Enabled bool
    // Policy 修复策略（默认 PolicyAutoFix）
    Policy ReconcilePolicy
    // Interval 后台检查间隔（默认 5min）
    Interval time.Duration
    // StartupRetries 启动时最大重试次数（默认 3）
    StartupRetries int
    // StartupTimeout 单次 reconcile 超时（默认 30s）
    StartupTimeout time.Duration
}
```

对应 YAML 配置：

```yaml
elasticsearch:
  reconciler:
    enabled: true
    policy: "auto_fix"          # alert_only | auto_fix
    interval: "5m"
    startup_retries: 3
    startup_timeout: "30s"
```

---

## 8. 可观测性

### 8.1 日志

| 级别 | 场景 |
|------|------|
| INFO | 首次 reconcile 成功 |
| WARN | 检测到漂移 / 自动修复 |
| ERROR | reconcile 失败（ES 不可达等） |

### 8.2 指标（可选，后续扩展）

```
otel_collector_template_reconcile_total{status="ok|fixed|error|drift_detected"}
otel_collector_template_reconcile_duration_seconds
otel_collector_template_drift_detected{template="otel-logs", field="timeUnixNano"}
```

### 8.3 Health Check 集成

`Provider.HealthCheck()` 中加入 Reconciler 状态：

```go
func (p *Provider) HealthCheck(ctx context.Context) (bool, string, map[string]any) {
    // ... existing ping + cluster health ...

    // Template reconciler status
    if p.reconciler != nil {
        details["template_reconciler"] = map[string]any{
            "ready":        p.reconciler.Ready(),
            "last_results": p.reconciler.LastResults(),
        }
    }
}
```

---

## 9. 实施计划

### Sprint 1：核心 Reconciler（高优先级）

- [ ] 新建 `template_spec.go`：抽取模板定义为 `TemplateSpec`
- [ ] 新建 `template_differ.go`：实现 `DiffTemplate()`
- [ ] 新建 `template_reconciler.go`：实现 Reconciler 核心逻辑
- [ ] 修改 `client_admin.go`：新增 `GetIndexTemplate()` 方法
- [ ] 修改 `provider.go`：集成 Reconciler 替代直接 `InitSchema`
- [ ] 新建单元测试文件

### Sprint 2：启动强校验 + 重试

- [ ] 实现 `ensureTemplatesWithRetry`（指数退避）
- [ ] 启动失败时阻止 Writer 启动（而非 Warn 后继续）
- [ ] `Ready()` 信号与 Writer 联动

### Sprint 3：已有索引 Mapping 校验 + 可观测性

- [ ] 实现 `CheckActiveIndexMappings`
- [ ] 集成到 HealthCheck
- [ ] 添加可观测性指标（可选）

---

## 10. 分布式场景设计

### 10.1 核心不变式

> **Version 只升不降**：Reconciler 发现 ES 上模板的 version ≥ 代码中 spec.Version 时，不执行 PUT。

这保证了：
- **灰度发布安全**：新实例（v2）上线后推送高版本模板，旧实例（v1）检测到 ES version=2 ≥ 自身 version=1 → 不覆盖
- **滚动更新安全**：无论新旧实例启动顺序如何，最终模板版本收敛为集群中最高的代码版本
- **无需分布式锁**：PUT /_index_template 是 ES 原生幂等操作，多实例并发 PUT 同一内容只是重复写入

### 10.2 场景分析

| 场景 | 行为 | 安全性 |
|------|------|:------:|
| N 个相同版本实例并发启动 | 全部 PUT 相同内容，幂等 | ✅ |
| 新实例(v2)先启动，旧实例(v1)后启动 | 旧实例发现 ES version=2 ≥ 1 → 跳过 | ✅ |
| 旧实例(v1)先启动，新实例(v2)后启动 | 新实例发现 ES version=1 < 2 → 覆盖 | ✅ |
| 旧实例(v1) reconcile 循环中，新模板已被 v2 推送 | 旧实例下次 reconcile 发现版本更高 → 跳过 | ✅ |
| 外部手动修改模板（无 version 字段） | version=nil < spec.Version → 覆盖修复 | ✅ |

### 10.3 ES Master 压力控制

N 个实例每 `interval` 都执行 GET /_index_template，正常运行时：
- GET 只读 cluster state，压力极小
- 版本一致时不 PUT，无写压力

**Jitter 优化**：避免 N 个实例在完全相同的时刻发起请求：

```go
// reconcile 间隔 = 基础间隔 + 随机 jitter（0~20%）
func (r *TemplateReconciler) nextInterval() time.Duration {
    jitter := time.Duration(rand.Int63n(int64(r.interval / 5)))
    return r.interval + jitter
}
```

### 10.4 可选增强：Leader Election

当实例数极大（>50）时，可通过以下方式收敛为单实例 reconcile：
- K8s LeaderElection（通过 coordination.k8s.io/v1 Lease 对象）
- ES 文档锁（在专用索引中抢占 TTL 文档）

**当前不实施**：目标场景 2-5 个实例，jitter + 版本号机制已足够。作为 `ReconcilerConfig.LeaderElection` 预留配置项。

---

## 11. 风险与约束

| 风险 | 缓解措施 |
|------|---------|
| PUT 模板不影响已有索引 | `CheckActiveIndexMappings` 提供告警 + 人工决策 |
| 频繁 PUT 模板影响 ES master | 版本号比对一致时跳过 + jitter 分散请求 |
| 启动阻塞导致 Collector 无法启动 | 配置化 `StartupRetries`，超出后降级为 Warn（可配） |
| 多实例并发 reconcile | PUT 幂等 + Version 只升不降不变式 |
| 灰度期间版本回退 | 旧实例检测到高版本自动跳过，不覆盖 |
| 模板 priority 冲突 | 使用高 priority (100) 确保代码定义的模板优先 |
| 实例数极大时 GET 请求集中 | jitter 分散 + 预留 LeaderElection 扩展点 |

---

## 12. 遗留问题

1. **已有索引 mapping 不一致如何自动修复？** — ES 不支持修改已有字段的类型，只能 reindex 或删除重建。当前方案选择"告警 + 人工决策"，不自动 reindex。
2. **多 Collector 实例场景** — PUT template 是幂等操作，多实例并发推送不会冲突，但需确保版本号一致（同一代码版本部署）。
3. **模板 version 管理的自动化** — 当前依赖开发者手动递增 version，未来可考虑基于 mapping hash 自动判断。
