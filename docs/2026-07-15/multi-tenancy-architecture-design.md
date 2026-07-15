# 多租户（Multi-Tenancy）架构设计方案

> 文档创建时间：2026-07-15  
> 状态：方案设计阶段（未实施）

---

## 1. 需求背景

### 1.1 当前问题

当前系统的 **Admin Extension**（Tempo/Prometheus 兼容 API）使用**静态 API Key 列表**进行认证：

```yaml
auth:
  type: api_key
  api_key:
    header: "X-API-Key"
    keys: ["key1", "key2"]  # 全局配置，无法区分租户
```

**问题**：
1. API Key 是全局配置，只能鉴权但无法识别用户身份
2. Grafana 配置 Tempo/Prometheus 数据源时，所有 Key 等价，查询无 App 隔离
3. 无法实现"租户 A 只看自己的 App 数据"的需求

### 1.2 目标

实现多租户机制：
- 每个**租户（Tenant）**可以创建多个 **App**
- App 归属于租户
- Grafana 通过配置 **Tenant API Key** 查询时，自动限定到该租户拥有的 App 数据
- 实现存储层（Elasticsearch）的**数据隔离**

---

## 2. 现有架构分析

### 2.1 已有的多租户基础设施（写入路径）

系统在**数据写入路径**已实现 App 级别的完整隔离：

```
Agent (Bearer Token) → AgentGatewayReceiver → TokenAuthProcessor → ES Writer
                           ↓                        ↓                    ↓
                    提取 token              验证 token → app_id    index: otel-traces-{app_id}-{date}
```

| 层级 | 组件 | 隔离能力 |
|------|------|----------|
| 数据上报 | AgentGatewayReceiver | Token → app_id 注入 |
| Pipeline | TokenAuthProcessor | Token 验证 + app_id 富化 |
| 存储写入 | ES Writers | **强制 app_id** → per-app index |
| 数据清理 | Provider.PurgeByApp() | 按 app_id 删除 |

**ES 索引命名**：
```
otel-traces-{app_id}-2026.07.15
otel-metrics-{app_id}-2026.07.15
otel-logs-{app_id}-2026.07.15
```

### 2.2 查询路径的缺失

查询层虽然 Reader 支持 AppID 参数，但 **Handler 层没有将租户身份传递到 Reader**：

```go
// tempo_handler.go — handleTempoSearch
plan, query, err := parseTempoSearchParams(r)
// ❌ query.AppID 从未被设置！
// 导致查询 index pattern: "otel-traces-*" (全局)
```

```go
// prometheus_handler.go — handlePromQuery / handlePromQueryRange
// ❌ MetricQuery 也没有 AppID
query := observabilitystorageext.MetricQuery{
    MetricName: expr.MetricName,
    Labels:     filterInternalLabels(labels),
    Time:       evalTime,
    // AppID: ??? (缺失)
}
```

### 2.3 AppManager 已有的能力

`controlplaneext/appmanager` 已实现：
- App CRUD（Create/Read/Update/Delete）
- Token 生成与验证（`ValidateToken() → AppID`）
- Redis 持久化（`otel:apps` hash）
- 状态管理（active/disabled）

---

## 3. 架构设计

### 3.1 核心概念模型

```
┌─────────────────────────────────────────────────────┐
│                    Tenant (租户)                      │
│  - ID: string (Base62, 16 chars)                    │
│  - Name: string                                     │
│  - APIKeys: []TenantAPIKey  ← Grafana 数据源认证   │
│  - Status: active | disabled                        │
├─────────────────────────────────────────────────────┤
│  owns ↓                                             │
│  ┌──────────────┐  ┌──────────────┐                 │
│  │ App (现有)    │  │ App (现有)    │  ...            │
│  │ ID: string   │  │ ID: string   │                 │
│  │ Token: str   │  │ Token: str   │                 │
│  │ TenantID: str│  │ TenantID: str│  ← 新增字段     │
│  └──────────────┘  └──────────────┘                 │
└─────────────────────────────────────────────────────┘
```

### 3.2 认证架构：混合模式（静态超级 Key + Redis 动态 Key）

系统采用**混合认证模式**，兼顾安全性、分布式便利性和故障恢复能力：

```
┌─────────────────────────────────────────────────────────────────┐
│                 认证层（基于前缀的智能路由）                        │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  Step 1: 提取 X-API-Key（或 Authorization: Bearer）              │
│     ↓                                                           │
│  Step 2: 识别 Key 前缀，路由到对应验证逻辑                        │
│                                                                 │
│     sk_ 前缀 → 匹配静态超级 Key（ConfigMap，不查 Redis）          │
│        → Yes: Admin 全局模式                                     │
│                                                                 │
│     ok_ 前缀 → SHA-256(key) → Redis 查找 admin 租户 Key          │
│        → Yes: Admin 全局模式（运营用户）                          │
│                                                                 │
│     tk_ 前缀 → SHA-256(key) → Redis 查找普通租户 Key              │
│        → Yes: Tenant 隔离模式（注入 TenantID）                   │
│                                                                 │
│     无前缀   → fallback 全路径匹配（向后兼容旧 Key）              │
│                                                                 │
│     以上均未匹配 → 401 Unauthorized                              │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

**三种角色与 Key 来源**：

| 角色 | 前缀 | Key 来源 | 权限范围 | 典型使用者 | 管理方式 |
|------|------|----------|----------|-----------|----------|
| **超级管理员** | `sk_` | ConfigMap/K8s Secret（1-2 把固定） | 全局 + 无法被页面吊销 | 系统管理员/应急恢复 | K8s 运维操作 |
| **运营用户** | `ok_` | Redis（admin 租户的动态 Key） | 全局（等效 Admin） | 运营团队日常使用 | 管理页面自助创建/吊销 |
| **租户用户** | `tk_` | Redis（各租户的动态 Key） | 仅本租户 App 数据 | 外部租户/Grafana 对接 | 管理页面自助创建/吊销 |

**Key 前缀规范**：

采用结构化前缀区分 Key 类型（参考 Stripe `sk_live_`、GitHub `ghp_` 业界惯例）：

| 前缀 | 含义 | 格式示例 | 说明 |
|------|------|----------|------|
| `sk_` | **S**uper **K**ey | `sk_Kj9mNpQrStUvWxYzAb23Cd45Ef67` | 超级管理员，静态配置 |
| `ok_` | **O**perator **K**ey | `ok_7mNpQrStUvWxYz01Ab23Cd45Ef67` | 运营用户，admin 租户动态 Key |
| `tk_` | **T**enant **K**ey | `tk_xUXCbjcSnSy5LZUJab12cd34ef56` | 普通租户，动态 Key |

前缀带来的好处：
- **快速识别**：日志中脱敏展示 `tk_xUXC****` 一眼可知是租户 Key
- **认证路由优化**：中间件根据前缀直接路由到对应验证逻辑，避免无效匹配
- **安全审计**：发现泄露 Key 时可立即判断影响范围（超级管理员 vs 运营 vs 租户）
- **向后兼容**：无前缀的旧 Key 走 fallback 全路径匹配

**为什么采用混合模式而非纯 Redis？**

| 问题 | 纯 ConfigMap | 纯 Redis | 混合模式（推荐） |
|------|-------------|----------|----------------|
| 分布式变更便利性 | ❌ 需改 ConfigMap + 重启 | ✅ 实时生效 | ✅ 运营 Key 实时，超级 Key 固定 |
| Redis 故障时可用性 | ✅ 不依赖 Redis | ❌ 完全不可用 | ✅ 超级 Key 仍可用（应急通道） |
| 运营自助化 | ❌ 每次加/减人员要运维操作 | ✅ 页面自助 | ✅ 日常走页面自助 |
| 鸡生蛋问题 | 无 | ❌ 第一把 Key 怎么创建？ | ✅ 超级 Key 引导初始化 |
| 安全性 | ⚠️ 明文存 ConfigMap | ✅ SHA-256 Hash | ✅ 超级 Key 走 K8s Secret + 运营 Key Hash 存储 |

### 3.3 认证与查询流程

```
Grafana Tempo/Prometheus DataSource
│
│  HTTP Header: X-API-Key: <tenant_api_key>
│        或者:  Authorization: Bearer <tenant_api_key>
▼
┌─────────────────────────────────────────────────────┐
│ Admin Extension — tenantAuthMiddleware (混合模式)     │
│                                                      │
│  1. 匹配静态超级 Key？→ Admin 模式（跳过 Redis）    │
│  2. 否则 → ValidateAPIKey() → TenantID             │
│  3. 注入 context: ctx = WithTenantID(ctx, tenantID) │
└──────────────────────────┬──────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────┐
│ Tempo/Prometheus Handler                             │
│                                                      │
│  tenantID := TenantIDFromContext(ctx)               │
│  appIDs := tenantManager.GetAppsByTenant(tenantID)  │
│  query.AppIDs = appIDs  ← 限定查询范围              │
└──────────────────────────┬──────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────┐
│ ES Reader                                            │
│                                                      │
│  indexPattern = buildMultiAppPattern(appIDs)         │
│  → "otel-traces-app1-*,otel-traces-app2-*"         │
│  或 filter: appId IN [app1, app2]                   │
└─────────────────────────────────────────────────────┘
```

### 3.4 Elasticsearch 索引策略

**现有索引命名已支持多租户**，无需在索引上额外加 TenantID：

```
otel-traces-{app_id}-{date}     # app_id 已是天然的隔离边界
otel-metrics-{app_id}-{date}
otel-logs-{app_id}-{date}
```

**为什么不需要在索引上加 TenantID？**
1. 索引已按 `app_id` 分片，App 归属 Tenant → 通过 App 列表即可实现 Tenant 级隔离
2. 避免索引重建/迁移的巨大成本
3. 保持与现有写入路径的兼容

**查询隔离实现**：
```go
// 方案 A：多 Index Pattern（推荐，ES 原生支持）
indexPattern := "otel-traces-app1-*,otel-traces-app2-*,otel-traces-app3-*"

// 方案 B：通配 + filter（适合 App 数量多的场景）
indexPattern := "otel-traces-*"
filter := {"terms": {"appId": ["app1", "app2", "app3"]}}
```

**选择策略**：
- App 数量 ≤ 20：使用方案 A（Index Pattern 列表），性能最优
- App 数量 > 20：使用方案 B（通配 + terms filter），避免 URL 过长

---

## 4. 详细设计

### 4.1 数据模型变更

#### 4.1.1 新增 Tenant 模型

```go
// extension/controlplaneext/tenantmanager/model.go

type Tenant struct {
    ID          string            `json:"id"`           // Base62, 16 chars
    Name        string            `json:"name"`
    Description string            `json:"description,omitempty"`
    Status      string            `json:"status"`       // "active", "disabled"
    Metadata    map[string]string `json:"metadata,omitempty"`
    CreatedAt   time.Time         `json:"created_at"`
    UpdatedAt   time.Time         `json:"updated_at"`
}

type TenantAPIKey struct {
    ID        string    `json:"id"`          // Base62, 16 chars
    TenantID  string    `json:"tenant_id"`
    KeyHash   string    `json:"key_hash"`    // SHA-256 Hash（不存明文）
    KeyPrefix string    `json:"key_prefix"`  // 类型前缀 + 前 8 位随机部分（如 "tk_xUXCbjcS"）
    KeyType   string    `json:"key_type"`    // "sk" | "ok" | "tk"（对应超级/运营/租户）
    Name      string    `json:"name"`        // 描述性名称，如 "Grafana Tempo"
    Scopes    []string  `json:"scopes"`      // 权限范围: ["trace:read", "metric:read", "log:read"]
    Status    string    `json:"status"`      // "active", "revoked"
    ExpiresAt *time.Time `json:"expires_at,omitempty"` // 可选过期时间
    CreatedAt time.Time `json:"created_at"`
}
```

#### 4.1.2 扩展 AppInfo

```go
// extension/controlplaneext/appmanager/model.go — 在 AppInfo 中新增字段

type AppInfo struct {
    ID          string            `json:"id"`
    Name        string            `json:"name"`
    Token       string            `json:"token"`
    TenantID    string            `json:"tenant_id"`    // ← 新增：归属租户
    // ... 其余字段不变
}
```

#### 4.1.3 Redis 存储设计

```
# Tenant 数据
otel:tenants                          # Hash: tenantID → JSON(Tenant)
otel:tenant_keys:{tenantID}           # Hash: keyID → JSON(TenantAPIKey)  (不含明文Key)
otel:tenant:{tenantID}:apps           # Set: 该租户拥有的 appID 列表

# 认证索引（基于 Key Hash 的 O(1) 查找）
otel:tenant_key_idx:{sha256(key)}     # String: → JSON{tenantID, keyID, scopes, status}

# 默认租户
otel:default_tenant                   # String: → "admin" (默认租户ID)
```

**分布式缓存一致性策略**：

多 Collector 实例场景下，各实例通过 Redis 共享 Tenant/Key 数据，本地使用两级缓存：

| 缓存层 | TTL | 数据 | 说明 |
|--------|-----|------|------|
| L1 本地缓存（sync.Map） | 30s | `keyHash → TenantAPIKeyValidation` | 热点认证路径，减少 Redis 调用 |
| L2 Redis | 永久 | Tenant/Key/App 关系数据 | 单一真相源（Source of Truth） |

- **Key 吊销生效延迟**：最大 30s（等待各实例本地缓存过期）
- **新增 Key 立即生效**：首次验证时 L1 未命中 → 查 Redis → 写入 L1
- **Tenant-App 关系变更**：同样 30s 最终一致性（GetTenantApps 结果缓存 30s）

### 4.2 TenantManager 接口

```go
// extension/controlplaneext/tenantmanager/manager.go

type Manager interface {
    // Tenant CRUD
    CreateTenant(ctx context.Context, req CreateTenantRequest) (*Tenant, error)
    GetTenant(ctx context.Context, tenantID string) (*Tenant, error)
    ListTenants(ctx context.Context) ([]*Tenant, error)
    UpdateTenant(ctx context.Context, tenantID string, req UpdateTenantRequest) (*Tenant, error)
    DeleteTenant(ctx context.Context, tenantID string) error

    // Tenant API Key 管理
    // CreateAPIKey 返回包含明文 Key 的响应（仅此一次），后续不可恢复
    CreateAPIKey(ctx context.Context, tenantID string, req CreateAPIKeyRequest) (*CreateAPIKeyResponse, error)
    ListAPIKeys(ctx context.Context, tenantID string) ([]*TenantAPIKey, error) // 仅返回 KeyPrefix，无明文
    RevokeAPIKey(ctx context.Context, keyID string) error
    // LookupAPIKey 通过明文 Key 查找对应信息（管理页面使用）
    LookupAPIKey(ctx context.Context, key string) (*TenantAPIKey, error)

    // 核心认证方法 — 从 API Key 解析租户身份（含本地缓存）
    ValidateAPIKey(ctx context.Context, key string) (*TenantAPIKeyValidation, error)

    // Tenant-App 关系
    GetTenantApps(ctx context.Context, tenantID string) ([]string, error) // 返回 appID 列表
    AssignAppToTenant(ctx context.Context, tenantID, appID string) error
    UnassignApp(ctx context.Context, tenantID, appID string) error

    // 统计信息（用于后续决策）
    GetTenantStats(ctx context.Context, tenantID string) (*TenantStats, error)
}

type CreateAPIKeyResponse struct {
    TenantAPIKey                      // 嵌入基础信息
    PlainKey     string `json:"key"` // 明文 Key（仅创建时返回一次）
}

type TenantAPIKeyValidation struct {
    Valid    bool     `json:"valid"`
    TenantID string  `json:"tenant_id"`
    Scopes   []string `json:"scopes"`
    Reason   string  `json:"reason,omitempty"`
}

// TenantStats 租户统计信息（用于未来配额决策）
type TenantStats struct {
    TenantID   string `json:"tenant_id"`
    AppCount   int    `json:"app_count"`    // 拥有的 App 数量
    KeyCount   int    `json:"key_count"`    // 活跃 Key 数量
    QueryCount int64  `json:"query_count"`  // 累计查询次数（周期性快照）
}
```

### 4.3 认证中间件增强（混合模式 + 前缀路由）

```go
// extension/adminext/middleware.go — 混合认证模式：基于 Key 前缀的智能路由

import (
    "crypto/subtle"
    "net/http"
    "strings"
)

// Key 类型前缀常量
const (
    KeyPrefixSuper    = "sk_" // 超级管理员 Key
    KeyPrefixOperator = "ok_" // 运营用户 Key（admin 租户）
    KeyPrefixTenant   = "tk_" // 普通租户 Key
)

type tenantContextKey struct{}

// WithTenantID injects tenant ID into context.
func WithTenantID(ctx context.Context, tenantID string) context.Context {
    return context.WithValue(ctx, tenantContextKey{}, tenantID)
}

// TenantIDFromContext extracts tenant ID from context.
// Returns empty string if no tenant (admin/super-admin mode).
func TenantIDFromContext(ctx context.Context) string {
    if v, ok := ctx.Value(tenantContextKey{}).(string); ok {
        return v
    }
    return ""
}

// tenantAuthMiddleware 混合认证中间件（基于前缀的智能路由）
//
// 路由策略：
//   sk_ 前缀 → 仅匹配静态超级 Key（不查 Redis）
//   ok_ 前缀 → 查 Redis，期望匹配 admin 租户 → Admin 全局模式
//   tk_ 前缀 → 查 Redis，期望匹配普通租户 → Tenant 隔离模式
//   无前缀   → fallback 全路径匹配（向后兼容旧 Key）
func (e *Extension) tenantAuthMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        key := extractAPIKey(r)
        if key == "" {
            http.Error(w, "Unauthorized", http.StatusUnauthorized)
            return
        }

        switch {
        case strings.HasPrefix(key, KeyPrefixSuper):
            // ── sk_ 前缀：仅匹配静态超级 Key（不依赖 Redis） ──
            if e.isSuperAdminKey(key) {
                next.ServeHTTP(w, r)
                return
            }

        case strings.HasPrefix(key, KeyPrefixOperator):
            // ── ok_ 前缀：运营用户 Key → 查 Redis，期望 admin 租户 ──
            if e.tenantManager != nil {
                if v, err := e.tenantManager.ValidateAPIKey(r.Context(), key); err == nil && v.Valid {
                    // ok_ Key 必须属于 admin 租户（双重校验）
                    if v.TenantID == "admin" {
                        next.ServeHTTP(w, r) // 全局模式
                        return
                    }
                }
            }

        case strings.HasPrefix(key, KeyPrefixTenant):
            // ── tk_ 前缀：租户 Key → 查 Redis，注入 TenantID ──
            if e.tenantManager != nil {
                if v, err := e.tenantManager.ValidateAPIKey(r.Context(), key); err == nil && v.Valid {
                    ctx := WithTenantID(r.Context(), v.TenantID)
                    next.ServeHTTP(w, r.WithContext(ctx))
                    return
                }
            }

        default:
            // ── 无前缀：向后兼容旧版 Key（全路径 fallback） ──
            if e.isSuperAdminKey(key) {
                next.ServeHTTP(w, r)
                return
            }
            if e.tenantManager != nil {
                if v, err := e.tenantManager.ValidateAPIKey(r.Context(), key); err == nil && v.Valid {
                    if v.TenantID == "admin" {
                        next.ServeHTTP(w, r)
                        return
                    }
                    ctx := WithTenantID(r.Context(), v.TenantID)
                    next.ServeHTTP(w, r.WithContext(ctx))
                    return
                }
            }
        }

        http.Error(w, "Unauthorized", http.StatusUnauthorized)
    })
}

// extractAPIKey 从请求中提取 API Key（支持两种 Header 格式）
func extractAPIKey(r *http.Request) string {
    if key := r.Header.Get("X-API-Key"); key != "" {
        return key
    }
    if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
        return auth[7:]
    }
    return ""
}

// isSuperAdminKey 检查是否为 ConfigMap/Secret 中配置的静态超级 Key
func (e *Extension) isSuperAdminKey(key string) bool {
    for _, staticKey := range e.config.Auth.APIKey.Keys {
        if subtle.ConstantTimeCompare([]byte(key), []byte(staticKey)) == 1 {
            return true
        }
    }
    return false
}

// GenerateTenantAPIKey 生成带类型前缀的 API Key
func GenerateTenantAPIKey(keyType string) (string, error) {
    var prefix string
    switch keyType {
    case "sk":
        prefix = KeyPrefixSuper
    case "ok":
        prefix = KeyPrefixOperator
    case "tk":
        prefix = KeyPrefixTenant
    default:
        prefix = KeyPrefixTenant
    }
    // 生成 32 位 Base62 随机部分（~190 bits 熵）
    randomPart, err := generateBase62String(32)
    if err != nil {
        return "", err
    }
    return prefix + randomPart, nil // 总长度: 3 (前缀) + 32 (随机) = 35 chars
}
```

### 4.4 查询层改造

#### 4.4.1 Tempo Handler

```go
// extension/adminext/tempo_handler.go — handleTempoSearch

func (e *Extension) handleTempoSearch(w http.ResponseWriter, r *http.Request) {
    // ... 现有逻辑 ...

    plan, query, err := parseTempoSearchParams(r)
    if err != nil { ... }

    // ── 多租户隔离 ──
    if tenantID := TenantIDFromContext(r.Context()); tenantID != "" {
        appIDs, err := e.tenantManager.GetTenantApps(r.Context(), tenantID)
        if err != nil {
            e.writeError(w, http.StatusInternalServerError, "failed to resolve tenant apps")
            return
        }
        if len(appIDs) == 0 {
            // 租户没有 App，返回空结果
            e.writeJSON(w, http.StatusOK, tempoSearchResponse{Traces: []tempoSearchTrace{}})
            return
        }
        query.AppIDs = appIDs // ← 新增字段，支持多 App 查询
    }

    // ... 继续执行查询 ...
}
```

#### 4.4.2 Prometheus Handler

```go
// extension/adminext/prometheus_handler.go — handlePromQuery

func (e *Extension) handlePromQuery(w http.ResponseWriter, r *http.Request) {
    // ... 现有逻辑 ...

    // ── 多租户隔离 ──
    var appIDs []string
    if tenantID := TenantIDFromContext(r.Context()); tenantID != "" {
        var err error
        appIDs, err = e.tenantManager.GetTenantApps(r.Context(), tenantID)
        if err != nil { ... }
    }

    query := observabilitystorageext.MetricQuery{
        AppIDs:     appIDs,  // ← 新增
        MetricName: expr.MetricName,
        Labels:     filterInternalLabels(labels),
        Time:       evalTime,
    }
    // ...
}
```

#### 4.4.3 TraceQuery / MetricQuery 扩展

```go
// extension/observabilitystorageext/types.go

type TraceQuery struct {
    AppID   string   `json:"appId,omitempty"`    // 单 App（向后兼容）
    AppIDs  []string `json:"appIds,omitempty"`   // ← 新增：多 App（租户级查询）
    // ... 其余字段不变
}

type MetricQuery struct {
    AppIDs     []string `json:"appIds,omitempty"` // ← 新增
    MetricName string   `json:"metricName"`
    // ...
}
```

#### 4.4.4 ES Reader Index Pattern 构建

```go
// extension/observabilitystorageext/provider/elasticsearch/query/pattern.go

// IndexPatternMulti generates a comma-separated index pattern for multiple app IDs.
func IndexPatternMulti(prefix string, appIDs []string) string {
    if len(appIDs) == 0 {
        return prefix + "-*" // 全局（admin 模式）
    }
    if len(appIDs) == 1 {
        return prefix + "-" + appIDs[0] + "-*"
    }
    // 多 App 场景：逗号分隔
    patterns := make([]string, len(appIDs))
    for i, id := range appIDs {
        patterns[i] = prefix + "-" + id + "-*"
    }
    return strings.Join(patterns, ",")
}
```

### 4.5 Admin API — 租户管理端点

```
# Tenant CRUD
POST   /api/v2/tenants                    — 创建租户
GET    /api/v2/tenants                    — 列出所有租户
GET    /api/v2/tenants/{id}               — 获取租户详情（含统计信息）
PUT    /api/v2/tenants/{id}               — 更新租户
DELETE /api/v2/tenants/{id}               — 删除租户

# Tenant API Key 管理
POST   /api/v2/tenants/{id}/keys          — 创建 API Key（返回明文，仅一次）
GET    /api/v2/tenants/{id}/keys          — 列出 API Keys（仅显示 prefix + 元信息）
DELETE /api/v2/tenants/{id}/keys/{keyId}  — 吊销 API Key
POST   /api/v2/keys/lookup                — 通过明文 Key 查询所属 Tenant（管理页面用）

# Tenant-App 关系
POST   /api/v2/tenants/{id}/apps/{appId}  — 分配 App 到租户
DELETE /api/v2/tenants/{id}/apps/{appId}  — 取消分配
GET    /api/v2/tenants/{id}/apps          — 列出租户的 Apps

# 统计信息
GET    /api/v2/tenants/{id}/stats         — 获取租户统计（App数、Key数、查询次数）
```

---

## 5. 实现方案对比

### 5.1 方案 A：Tenant API Key 模式（推荐）

**机制**：每个租户创建独立的 API Key，Grafana 数据源配置该 Key。

| 维度 | 评估 |
|------|------|
| 实现复杂度 | 中等 — 需新增 TenantManager + Key 验证 |
| 兼容性 | 高 — 与 Grafana 原生 API Key 认证完美兼容 |
| 安全性 | 高 — Key 可独立吊销、设置过期时间、限定 Scopes |
| 运维成本 | 低 — 无需修改 ES 索引结构 |
| 性能影响 | 极低 — 仅增加一次 Redis 查询（可缓存） |

### 5.2 方案 B：X-Scope-OrgID Header 模式

**机制**：沿用 Grafana Mimir/Tempo 原生多租户 Header。

| 维度 | 评估 |
|------|------|
| 实现复杂度 | 低 — 只需从 Header 读取 OrgID |
| 兼容性 | 中 — 需要 Grafana Enterprise 或 Nginx 注入 Header |
| 安全性 | 低 — Header 可被客户端伪造（除非有网关强制注入） |
| 适用性 | 不适合 — 我们的 OrgID 概念不同于 Grafana 原生 |

### 5.3 方案 C：Basic Auth Username 作为 Tenant 标识

**机制**：Grafana Basic Auth 的 Username 映射为 TenantID。

| 维度 | 评估 |
|------|------|
| 实现复杂度 | 低 |
| 兼容性 | 高 — Grafana 所有数据源都支持 Basic Auth |
| 安全性 | 中 — Password 相当于 Tenant Key |
| 灵活性 | 低 — 一个数据源只能对应一个 Tenant |

### 5.4 推荐方案

**选择方案 A（Tenant API Key）**，原因：
1. 安全性最高（Key 可独立管理、吊销、设过期）
2. 与 Grafana 配置方式最自然（Custom HTTP Headers: `X-API-Key: xxx`）
3. 支持细粒度权限控制（Scopes: trace:read, metric:read 等）
4. 无需修改 ES 索引结构

---

## 6. 实施计划

### Sprint 1：基础设施层（预计 3-4 天）

- [ ] 实现 `tenantmanager` 包（Tenant CRUD + Redis 持久化）
- [ ] 实现 `TenantAPIKey` 管理（Key 生成 + SHA-256 Hash 存储 + 吊销）
- [ ] 实现 `ValidateAPIKey()` 方法（Hash 匹配 + 本地 TTL=30s 缓存）
- [ ] 实现 `LookupAPIKey()` 方法（管理页面通过明文 Key 查询）
- [ ] 扩展 `AppInfo` 添加 `TenantID` 字段
- [ ] 系统启动时自动创建默认 `admin` 租户 + 迁移现有 App
- [ ] 实现 `TenantStats` 统计能力（App/Key 数量 + 查询计数器）

**验收标准**：
- 单元测试覆盖 Tenant CRUD + Key Hash 验证 + 缓存过期
- Redis 存储正确性验证
- 默认 admin 租户自动创建 + 现有 App 自动分配

### Sprint 2：认证层改造（预计 2-3 天）

- [ ] 实现混合认证中间件 `tenantAuthMiddleware`（三级优先级）
  - [ ] 优先级 1：静态超级 Key 匹配（ConfigMap/Secret，使用 ConstantTimeCompare）
  - [ ] 优先级 2：Redis admin 租户 Key → Admin 全局模式
  - [ ] 优先级 3：Redis 普通租户 Key → Tenant 隔离模式
- [ ] 实现 `WithTenantID` / `TenantIDFromContext` context 传递
- [ ] 集成 TenantManager 到 Extension 启动流程
- [ ] 配置结构扩展：静态超级 Key 支持从环境变量/K8s Secret 注入

**验收标准**：
- 静态超级 Key：Redis 不可用时仍能认证成功（应急通道验证）
- admin 租户动态 Key：认证成功后无 TenantID（全局模式）
- 普通租户动态 Key：认证成功后 ctx 包含 TenantID
- 三种 Key 优先级正确，无混淆

### Sprint 3：查询隔离（预计 3-4 天）

- [ ] `TraceQuery` / `MetricQuery` 新增 `AppIDs` 字段
- [ ] ES Reader 支持 `IndexPatternMulti` 多 App 查询
- [ ] Tempo Handler 注入 AppIDs
- [ ] Prometheus Handler 注入 AppIDs
- [ ] 端到端测试（Grafana → Tempo/Prom → ES）

**验收标准**：
- Tenant A 的 Key 只能查到 Tenant A 拥有的 App 数据
- Admin Key 可查看所有数据

### Sprint 4：管理 API + 文档（预计 2 天）

- [ ] 实现 Tenant 管理 HTTP API
- [ ] 实现 Tenant-App 关系管理 API
- [ ] API 文档更新
- [ ] Grafana 数据源配置指南

---

## 7. 实现难度评估

| 模块 | 难度 | 说明 |
|------|------|------|
| TenantManager (CRUD + Redis) | ⭐⭐ 低 | 参照现有 AppManager 实现 |
| Tenant API Key 认证 | ⭐⭐ 低 | 在现有 middleware 基础上扩展 |
| AppInfo 添加 TenantID | ⭐ 极低 | 一个字段 + 迁移逻辑 |
| TraceQuery/MetricQuery 多 App | ⭐⭐ 低 | 增加字段 + Reader 适配 |
| ES IndexPatternMulti | ⭐⭐ 低 | 字符串拼接 |
| Tempo Handler 注入 | ⭐⭐ 低 | 从 context 取值注入 |
| Prometheus Handler 注入 | ⭐⭐⭐ 中 | Prometheus handler 查询路径较分散 |
| 端到端测试 | ⭐⭐⭐ 中 | 需要 Redis + ES 环境 |
| 向后兼容性保障 | ⭐⭐⭐ 中 | 确保 Admin Key 无 Tenant 限制 |

**总体评估**：中等难度，预计 2-3 周完成全部 Sprint。

---

## 8. 风险与考量

### 8.1 性能

| 操作 | 影响 | 缓解措施 |
|------|------|----------|
| ValidateAPIKey | 每次请求 +1 Redis 查询 | 本地 TTL 缓存（同 TokenAuthProcessor） |
| GetTenantApps | 每次查询 +1 Redis 查询 | 缓存 Tenant→Apps 映射（TTL 60s） |
| Multi-App Index Pattern | URL 变长 | App 数量 > 20 时切换 terms filter |

### 8.2 安全

- **Key 格式规范**：`{prefix}_{base62_random}`，总长度 35 字符（3 位前缀 + 32 位随机部分，~190 bits 熵）
  - `sk_` — 超级管理员 Key（静态配置）
  - `ok_` — 运营用户 Key（admin 租户动态 Key）
  - `tk_` — 普通租户 Key（各租户动态 Key）
- **Key Hash 存储方案**：
  - Redis 中仅存储 SHA-256 Hash，不存明文
  - 仅在首次创建时返回一次明文，之后不可恢复
  - **查询索引**：Redis 使用 `otel:tenant_key_idx:{sha256(key)}` → `tenantID` 实现 O(1) 查找
  - **管理页面查询**：用户在页面输入完整 Key 明文 → 前端计算 SHA-256 → 后端匹配 Hash → 返回对应 Tenant 信息
  - Key 前缀展示：列表页面展示前缀 + 前 8 位随机部分（如 `tk_xUXCbjcS...`），不暴露完整值
- **前缀带来的安全增强**：
  - 泄露 Key 时可根据前缀立即判断影响范围（`sk_` = 最高危，需立即轮换）
  - 中间件基于前缀路由，`sk_` 前缀只走静态匹配不查 Redis，减少攻击面
  - 日志脱敏输出带前缀（如 `ok_7mNp****`），方便审计追溯
- 支持 Key 过期与吊销
- **静态超级 Key 安全策略**：
  - 存储在 K8s Secret 中（非明文 ConfigMap），通过环境变量或 Volume 挂载注入
  - 必须以 `sk_` 前缀开头（生成时强制）
  - 数量固定（1-2 把），几乎不需要变更
  - 使用 `crypto/subtle.ConstantTimeCompare` 防止时序攻击
  - 仅用于应急恢复和系统初始化，日常操作走 `ok_` 运营 Key
- 认证路由隔离：基于前缀分流，`sk_` → 静态匹配，`ok_`/`tk_` → Redis 动态验证，互不干扰

### 8.3 向后兼容

- 现有 Agent 上报流程**完全不受影响**（Token → app_id 机制不变）
- 现有 ConfigMap 中的静态 Admin Key **继续工作**（作为超级管理员，无 Tenant 限制）
- ES 索引结构无需修改

**迁移策略**：
- 系统启动时自动创建默认 `admin` 租户（如不存在）
- 现有所有 App 自动分配到 `admin` 租户
- 后续通过管理 API 将 App 重新分配到新租户
- `admin` 租户的动态 Key 可查看所有 App（等效于超级管理员的行为）
- 运营人员逐步从使用 ConfigMap 静态 Key 迁移到使用 admin 租户的动态 Key（页面自助管理）

### 8.4 扩展性考虑

**当前实现的统计能力**（为后续决策提供数据支撑）：
- 每租户 App 数量统计
- 每租户活跃 Key 数量统计
- 查询次数累计统计（周期性快照到 Redis）

**Future 扩展方向**：
- **Tenant 配额管理** — 基于统计数据设定阈值（每租户最大 App 数、最大查询 QPS）
- **RBAC** — 基于 Scopes 的细粒度权限（只读/读写/管理）
- **Tenant 自助门户** — 租户自行创建 App、管理 Key
- **审计日志** — 当前不需要，后续如有合规需求可追加

---

## 9. Grafana 配置示例

### 9.1 Tempo 数据源

```yaml
# Grafana Data Source 配置
Type: Tempo
URL: http://<collector>:8088/api/v2/tempo
Access: Server (proxy)

# Custom HTTP Headers（实现多租户隔离）
X-API-Key: <tenant_api_key>
```

### 9.2 Prometheus 数据源

```yaml
Type: Prometheus
URL: http://<collector>:8088/api/v2/prometheus
Access: Server (proxy)

# Custom HTTP Headers
X-API-Key: <tenant_api_key>
```

---

## 10. 架构全景图

```
                  ┌─────────────────────────────┐
                  │         Grafana              │
                  │  ┌────────┐  ┌───────────┐  │
                  │  │ Tempo  │  │Prometheus │  │
                  │  │  DS    │  │   DS      │  │
                  │  └───┬────┘  └─────┬─────┘  │
                  └──────┼─────────────┼────────┘
                         │ X-API-Key   │ X-API-Key
                         ▼             ▼
┌────────────────────────────────────────────────────────────────┐
│                     Admin Extension (:8088)                      │
│                                                                  │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │ tenantAuthMiddleware（前缀路由）                          │    │
│  │  sk_ → 静态超级 Key (ConfigMap) → Admin 全局             │    │
│  │  ok_ → Redis (admin 租户) → Admin 全局（运营用户）       │    │
│  │  tk_ → Redis (普通租户) → ctx = WithTenantID(tenantID)  │    │
│  │  无前缀 → fallback 全路径匹配（向后兼容）                │    │
│  └─────────────────────────┬───────────────────────────────┘    │
│                             │                                    │
│  ┌──────────────────────────┼──────────────────────────────┐    │
│  │                          ▼                               │    │
│  │  Tempo Handler ─── TenantID → GetTenantApps → AppIDs   │    │
│  │  Prom Handler  ─── TenantID → GetTenantApps → AppIDs   │    │
│  └──────────────────────────┬──────────────────────────────┘    │
│                             │                                    │
│  ┌──────────────────────────┼──────────────────────────────┐    │
│  │ Storage Reader           ▼                               │    │
│  │  TraceReader.Search(ctx, query{AppIDs: [...]})          │    │
│  │  MetricReader.Query(ctx, query{AppIDs: [...]})          │    │
│  └──────────────────────────┬──────────────────────────────┘    │
└─────────────────────────────┼──────────────────────────────────┘
                              │
                              ▼
┌────────────────────────────────────────────────────────────────┐
│                     Elasticsearch                                │
│                                                                  │
│  Tenant A (Apps: app1, app2):                                   │
│    otel-traces-app1-2026.07.*                                   │
│    otel-traces-app2-2026.07.*                                   │
│    otel-metrics-app1-2026.07.*                                  │
│    otel-metrics-app2-2026.07.*                                  │
│                                                                  │
│  Tenant B (Apps: app3):                                         │
│    otel-traces-app3-2026.07.*                                   │
│    otel-metrics-app3-2026.07.*                                  │
└────────────────────────────────────────────────────────────────┘
```

---

## 11. 关键设计决策总结

| # | 决策 | 理由 |
|---|------|------|
| 1 | **不在 ES 索引名中加 TenantID** | 现有 `app_id` 分片已实现物理隔离，通过 Tenant→Apps 映射实现逻辑隔离即可 |
| 2 | **Tenant API Key 独立于 App Token** | 关注点分离：App Token 用于数据上报，Tenant Key 用于查询认证 |
| 3 | **使用 X-API-Key header** | 与 Grafana Custom HTTP Headers 配置方式一致，无需 Basic Auth |
| 4 | **混合认证模式（静态超级 Key + Redis 动态 Key）** | 兼顾分布式便利性（运营 Key 页面自助管理）和故障恢复（Redis 挂了超级 Key 仍可用） |
| 5 | **TenantManager 独立于 AppManager** | SRP 原则，职责分离：App 管理 vs 租户关系管理 |
| 6 | **缓存 Tenant→Apps 映射** | 避免每次查询都访问 Redis，TTL 缓存（30s）足够 |
| 7 | **Key SHA-256 Hash 存储** | 安全性优先，页面通过输入明文计算 Hash 后匹配查询 |
| 8 | **Redis TTL 保证分布式一致性** | 多 Collector 实例通过本地缓存 TTL=30s 实现最终一致性 |
| 9 | **默认 admin 租户迁移** | 现有 App 全部归属 admin 租户，零成本平滑迁移 |
| 10 | **先统计后限额** | 先实现 App/Key/Query 统计能力，为后续配额决策提供数据支撑 |
| 11 | **admin 租户 Key 等效超级管理员** | 运营用户通过 admin 租户的动态 Key 日常操作，避免 ConfigMap 频繁变更 |
| 12 | **Key 前缀区分类型（sk_/ok_/tk_）** | 参考业界惯例（Stripe/GitHub），通过前缀实现快速识别、路由优化、安全审计、向后兼容 |

---

## 12. 决策记录

以下为针对遗留问题的决策结果（2026-07-15）：

| # | 问题 | 决策 | 备注 |
|---|------|------|------|
| 1 | Key Hash 存储 | ✅ 采用 SHA-256 Hash 存储 | 页面需支持通过明文 Key 查询（即用输入的 Key 计算 Hash 后匹配） |
| 2 | 多 Collector 实例缓存一致性 | ✅ 通过 Redis TTL 实现 | 分布式场景下各实例通过 Redis 共享状态，本地缓存使用 TTL 过期机制保证最终一致性 |
| 3 | 迁移策略 | ✅ 创建默认 "admin" 租户 | 现有 App 全部分配到默认 admin 租户，后续按需迁移 |
| 4 | 审计日志 | ❌ 不需要 | 当前阶段不记录 Tenant 查询行为 |
| 5 | 配额限制 | ❌ 暂不实施，做统计 | 先不限制 App 数量和查询 QPS，但需实现统计能力，方便后续决策 |
| 6 | Admin Key 管理方式 | ✅ 混合模式 | ConfigMap 保留 1-2 把静态超级 Key（应急+初始化）；运营用户通过 admin 租户动态 Key 自助管理（页面创建/吊销），解决分布式场景下配置不便的问题 |
| 7 | API Key 前缀规范 | ✅ 采用 `sk_`/`ok_`/`tk_` 三种前缀 | 超级管理员 `sk_`、运营用户 `ok_`、租户 `tk_`；中间件基于前缀智能路由，减少无效匹配；无前缀的旧 Key 走 fallback 兼容 |
