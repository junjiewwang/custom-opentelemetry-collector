# App-Tenant 关系与租户认证授权设计（v2）

> 文档创建时间：2026-09-09
> 状态：方案设计阶段（未实施）
> 关联：[多租户架构 v2](./multi-tenancy-architecture-design-v2.md)
> 本版修订：**`tenant_id` 落库是确定需求**（非条件优化），据此把数据模型、写路径、查询路径按「tenant_id 为一等公民」重新设计，并给出不返工的分阶段路径。

---

## 1. 范围与设计原则

**做：**
1. App↔Tenant 关系模型与管理（1:N，稳定不重指派）
2. 租户认证（读路径 API Key）+ 授权（租户只看自己的 App）
3. **目标数据模型：`tenant_id` 写入存储**（确定需求，非条件优化）

**分阶段落地（避免返工）：**
- **Phase 1**：关系 + 认证 + **预留 tenant_id schema**，隔离先用 app 级别（`app_id` 过滤）
- **Phase 2**：`tenant_id` 写入 + 一次性回填 + 查询切换为 `tenant_id` 等值

**核心不变量：**
- `tenant_id` 由 `AppInfo.TenantID` 在**写入时**派生，且**不可变**（无重指派）→ 反规范化的 `tenant_id` 永不与真相源漂移。
- `app_id` 与 `tenant_id` 同源（都来自同一个 `AppInfo`），写入时天然一致。

---

## 2. App↔Tenant 关系模型（不变）

- **1 Tenant : N App**，一个 App 归属且仅归属一个 Tenant；关系**稳定不重指派**。
- 默认 `admin` 租户，现有 App 迁移归入。
- 单一真相源 = `AppInfo.TenantID`（不在 Redis 另建 app-set，避免双源 desync）。
- 写路径（Agent 上报）仍走 `Token → app_id`，Agent 不感知 tenant；tenant 只影响读路径。

```go
// extension/controlplaneext/appmanager/model.go — 已有结构，加一个字段
type AppInfo struct {
    ID       string `json:"id"`
    Name     string `json:"name"`
    Token    string `json:"token"`
    TenantID string `json:"tenant_id"`   // ← 新增：归属租户（默认 "admin"）
    // ... 其余不变
}
```

---

## 3. 目标数据模型：`tenant_id` 落库（一等公民）

### 3.1 存储 schema（现在预留，Phase 2 填值）

| 信号 | 存储 | 现有字段 | 新增字段（预留） |
|---|---|---|---|
| Trace | ES | 顶层 `appId` | 顶层 **`tenantId`** |
| Log | ES | 顶层 `appId` | 顶层 **`tenantId`** |
| Metric | VM | `app_id` label | **`tenant_id`** label |

```go
// ES 存储模型（extension/observabilitystorageext/storedmodel/）
type StoredLogRecord struct {
    // ...
    AppID    string `json:"appId,omitempty"`
    TenantID string `json:"tenantId,omitempty"`  // ← 预留
}

// VM 写路径（provider/victoriametrics/metric_writer.go）
func baseLabels(pt storedmodel.StoredMetricDataPoint, extra map[string]string) map[string]string {
    labels := make(map[string]string)
    if pt.AppID != "" { labels["app_id"] = pt.AppID }
    if pt.TenantID != "" { labels["tenant_id"] = pt.TenantID }  // ← 预留
    // ...
}
```

### 3.2 写路径穿线（token → app_id + tenant_id）

```go
// extension/controlplaneext/appmanager — TokenValidationResult 加字段
type TokenValidationResult struct {
    AppID    string
    TenantID string   // ← 新增：由 app.TenantID 派生
    // ...
}

// ValidateToken 内部已持有 app 对象（service.go:390 AppID: app.ID），
// 扩展为 TenantID: app.TenantID 即可，一行改动。
```

- Phase 1：`ValidateToken` 已能返回 `TenantID`，但 writer 暂不落 `tenant_id`（只落 `app_id`）。
- Phase 2：writer 打开 `tenant_id` 注入（schema 早已预留），无需改 `ConvertOTLPLog`/`baseLabels` 签名。

---

## 4. 查询路径：`tenant_id` 等值（目标）/ `app_id` 列表（过渡）

### 4.1 目标形态（Phase 2）

```go
tenantID := TenantIDFromContext(ctx)   // tk_ → "X"；sk_/ok_ → ""（全局）
query.TenantID = tenantID
// ES  : filter tenantId == tenantID            （等值，O(1)）
// VM  : inject tenant_id="X" matcher           （等值）
```

- 租户隔离 = `tenant_id` 等值，**与 app 数量无关**。
- app 下钻 = `app_id` 等值（保留）。

### 4.2 过渡形态（Phase 1，存储尚无 tenant_id）

```go
tenantID := TenantIDFromContext(ctx)
appIDs, _ := tenantManager.ListTenantApps(ctx, tenantID)   // ListApps 过滤
query.AppIDs = appIDs
// ES : appId IN appIDs（terms / index-pattern 列表）
// VM : app_id=~"a|b|c"
```

### 4.3 过渡一致性（Phase 2 迁移期间）

Phase 2 回填期间，存在「新数据有 `tenant_id`、旧数据没有」的窗口。查询需同时覆盖：

```
tenant_id="X"  OR  (tenant_id 缺失  AND  app_id ∈ X 的 apps)
```

- 由于回填是一次性、数据量小（当前 2 app、~2 周），建议**回填窗口内短暂双写查询**（先按 tenant_id，再按 app_id 兜底），回填完成后去掉兜底。

---

## 5. 租户认证（读路径）

| 前缀 | 角色 | 来源 | 认证结果 |
|---|---|---|---|
| `sk_` | 超级管理员 | ConfigMap/K8s Secret（静态，ConstantTimeCompare） | 全局，ctx 无 TenantID |
| `ok_` | 运营用户 | Redis（admin 租户动态 Key） | 全局，ctx 无 TenantID |
| `tk_` | 租户用户 | Redis（各租户动态 Key） | ctx 注入 TenantID |

- Key 存 SHA-256；Redis `otel:tenant_key_idx:{sha256(key)}` → `{tenantID, scopes, status}`。
- L1 本地缓存 TTL=30s；`RevokeAPIKey` 删索引；`DisableTenant` 使其 Key 全失效。

---

## 6. 授权 + 隔离执行

| 身份 | 数据范围 | 管理能力 |
|---|---|---|
| `sk_` / `ok_`（无 TenantID） | 全部 App | tenant/app CRUD |
| `tk_`（TenantID=X） | 仅 X 的 App | 只读查询 |

- 隔离单元最终是 `tenant_id`（Phase 2），过渡期是 `app_id`（Phase 1）。
- 两者都由「租户 Key → TenantID → 范围」这一条授权链导出。

---

## 7. 关系管理（CRUD）

| 操作 | 权限 |
|---|---|
| `CreateTenant` / `GetTenant` / `ListTenants` / `UpdateTenant` | sk_ / ok_ |
| `DisableTenant`（status=disabled → Key 失效） | sk_ / ok_ |
| `DeleteTenant`（仅空租户） | sk_ / ok_ |
| `CreateApp(tenantID)`（默认 admin） | sk_ / ok_ |
| `ListTenantApps(tenantID)` | sk_ / ok_ / tk_ |
| ~~ReassignApp~~ | **不提供**（关系稳定） |

API：`POST/GET/PUT/DELETE /api/v2/tenants`、`GET /api/v2/tenants/{id}/apps`。

---

## 8. 分阶段实施（不返工）

| 阶段 | 内容 | 涉及 |
|---|---|---|
| **Phase 1** | ① `AppInfo.TenantID` + 迁移到 `admin`；② `tenantmanager` + Tenant API Key + 认证中间件；③ **预留 schema**（`StoredLogRecord/StoredSpan.TenantID`、VM `tenant_id`、`TokenValidationResult.TenantID`）；④ 隔离用 app 级别 | appmanager / tenantmanager / adminext / storedmodel |
| **Phase 2** | ⑤ writer 打开 `tenant_id` 注入；⑥ 一次性回填（ES reindex `tenantId`、VM export→relabel→re-import）；⑦ 查询切 `tenant_id` 等值；⑧ 回填窗口双写查询 → 完成后去兜底 | writer / 回填工具 / reader |

**为什么这样分不返工**：Phase 1 就把 schema 和写路径 seam 预留好，Phase 2 只是「打开注入 + 回填 + 切查询过滤」，不碰任何接口签名。

---

## 9. 明确不做 / 延后

- **app 跨租户重指派**：不会发生（用户确认）。历史数据按 `TenantA + appA` 绑定仍可查；「管理权 vs 数据归属」正交，后续单独分析。
- **大租户独立物理链路**：见 v2 §7 L3，本轮只留路由 seam。
