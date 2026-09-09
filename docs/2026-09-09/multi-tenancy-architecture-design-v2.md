# 多租户（Multi-Tenancy）架构设计 v2（修订版）

> 文档创建时间：2026-09-09
> 状态：方案设计阶段（未实施）
> 前身：[2026-07-15 版](./../2026-07-15/multi-tenancy-architecture-design.md)（本版修正其 3 个已过时前提，并给出关键决策点的优缺点分析与最优解）

---

## 1. 修订背景

2026-07-15 版多租户设计发布后，代码库发生了三处影响该设计的演进：

1. **指标后端从 Elasticsearch 迁到 VictoriaMetrics（vmsingle）**——指标隔离维度由「ES 索引分片」变为「VM series 上的 label」。
2. **查询路径已不是零隔离**——`prometheus_handler.go` 已通过 `extractAppID` 从 PromQL 的 `app_id="X"` label 提取单 app 做路由（但这是**客户端自报**，非服务端强制）。
3. **`AppInfo` 已带 per-signal `RetentionPolicy`**——与未来 tenant 配额/计费直接相关。

本版在原设计基础上：保留仍成立的决策（认证混合模式、App Token 与 Tenant Key 分离、索引不加 TenantID 等），**修正隔离机制**，并引入一个核心改进：**写入时把 `tenant_id` 直接落库（denormalize）**，使租户查询从「展开 app 列表」退化为「`tenant_id` 等值过滤」。

---

## 2. 现状核对（设计 vs 已落地）

| 要素 | 2026-07-15 设计 | 当前代码 | 结论 |
|---|---|---|---|
| 写路径隔离（Token→app_id） | §2.1 已有 | ✅ `tokenauthprocessor` + `AppService.ValidateToken` + `AppInfo` | 一致 |
| `AppInfo.TenantID` 字段 | §4.1.2 新增 | ❌ `AppInfo` 无 `TenantID` | **未实施** |
| `tenantmanager` 包 | §4.2 新增 | ❌ 不存在 | **未实施** |
| 混合认证中间件（sk_/ok_/tk_） | §4.3 新增 | ❌ 无 `tenantAuthMiddleware` / 前缀路由 | **未实施** |
| 读路径认证 | §1.1 静态 key | ✅ `adminext/middleware.go` `authenticateAPIKey` 仍是静态 key | 与 §1.1 一致 |
| `TraceQuery/MetricQuery.AppIDs` | §4.4.3 新增 | ❌ 只有单 `AppID` | **未实施** |
| 多 App index pattern | §4.4.4 | ❌ 无 `IndexPatternMulti` | **未实施** |

**核心结论：多租户设计整体仍处于「设计阶段」，0 行落地；且 3 个前提已过时。**

---

## 3. 更新后的架构设计

### 3.1 概念模型（不变）

```
Tenant (1) ──owns──> (N) App
  - ID (Base62)
  - Name / Status
  - APIKeys[]              ← 读路径凭证（Grafana 数据源）
  - Scopes[]               ← 权限范围（trace:read / metric:read / log:read）

App：新增 TenantID 字段       ← 归属租户（单一真相源，供写入时解析）
```

- 写入路径仍走 `Agent Token → app_id`，Agent 不感知 tenant。
- 租户只存在于**读路径**。「App Token 与 Tenant Key 分离」仍成立。

### 3.2 认证：混合模式（不变，明确与写路径解耦）

沿用原设计 §3.2/§4.3：

| 前缀 | 角色 | 来源 | 权限 |
|---|---|---|---|
| `sk_` | 超级管理员 | ConfigMap/K8s Secret（静态） | 全局 + 不可页面吊销 |
| `ok_` | 运营用户 | Redis（admin 租户动态 Key） | 全局 |
| `tk_` | 租户用户 | Redis（各租户动态 Key） | 仅本租户 App 数据 |

- Key 存 SHA-256 hash；L1 本地缓存 TTL=30s；`crypto/subtle.ConstantTimeCompare` 防时序攻击。
- 这是**读路径**认证，与写路径 `Authorization: Bearer <agent_token>` 是两套独立凭证。

### 3.3 核心改进：写入时落 `tenant_id`（denormalize）

**写路径**在 token 解析出 `app_id` 时，顺手解析出 `tenant_id`（`AppInfo.TenantID` 已存在），两者一并写入存储：

| 信号 | 存储 | 写入字段 | 单 app 查询 | 单 tenant 查询 |
|---|---|---|---|---|
| Trace | ES | 顶层 `appId` + **`tenantId`** | `appId="X"` | **`tenantId="X"`（等值）** |
| Log | ES | 顶层 `appId` + **`tenantId`** | `appId="X"` | **`tenantId="X"`（等值）** |
| Metric | VM | `app_id` + **`tenant_id`** label | `app_id="X"` | **`tenant_id="X"`（等值）** |

- VM series 已由 `metric_writer.go` 的 `baseLabels` 注入 `app_id` label（`labels["app_id"] = pt.AppID`），`tenant_id` 同理并行注入。
- ES 文档已有顶层 `appId` 字段，`tenantId` 同理并行。

**为什么 denormalize 而非查询时展开 app 列表？**

| | 查询时展开 AppIDs | 写入时落 tenant_id（本方案） |
|---|---|---|
| 查询复杂度 | O(N) app 列表展开（正则/terms/index-pattern） | **O(1) `tenant_id="X"` 等值** |
| 多 app 扩展性 | ⚠️ app 多时退化 | ✅ 与 app 数无关 |
| 查询路径 Redis 往返 | 每次 `GetTenantApps` | **零** |
| 写路径改动 | 无 | 仅多打一个 label/字段（token→app 已解析出 TenantID） |

### 3.4 查询隔离

```go
tenantID := TenantIDFromContext(ctx)   // 空 = admin 全局
query.TenantID = tenantID              // 直接等值过滤，无需 GetTenantApps
// ES  : filter tenantId == tenantID
// VM  : inject tenant_id="X" matcher（等值）
```

- tenant 级隔离 = `tenant_id` 等值；app 级下钻 = `app_id` 等值。两个维度各司其职。
- 查询路径**不再需要** `GetTenantApps`（该映射仅用于写入时解析 app→tenant，与管理页面展示）。

---

## 4. 关键决策点：优缺点分析与最优解

### 决策 1：指标 tenant 隔离机制 —— **写入时落 `tenant_id` label + 查询时等值**

**方案 A：`app_id=~"a|b|c"` 正则 matcher（查询时展开）**

| 维度 | 分析 |
|---|---|
| 优点 | ① 无需改写入路径；② 与 ES 的 AppIDs 语义统一 |
| 缺点 | ① **app 多时正则/index-pattern 变长，退化**；② 每次查询需解析/改写 PromQL；③ 查询路径需 `GetTenantApps` 往返 |

**方案 B：写入时落 `tenant_id`（denormalize）+ 查询时 `tenant_id="X"` 等值（推荐）**

| 维度 | 分析 |
|---|---|
| 优点 | ① **O(1) 等值，与 app 数无关**；② 查询路径零 Redis 往返、零列表展开；③ 写路径几乎零成本（token 已解析出 TenantID）；④ ES/VM 两条路径语义完全统一 |
| 缺点 | ① 需回填现有数据（ES reindex + VM re-import）；② app 重指派需回填历史数据 |

**最优解：方案 B（写入时落 `tenant_id`）**
理由：把租户隔离从「查询时 join」降为「写入时 denormalize」，直接消除「某租户下 app 很多」的扩展瓶颈。代价（回填）在数据量小、重指派罕见的前提下有界，且**越早做越便宜**。

---

### 决策 2：Tenant→App 关系存储 —— **`AppInfo.TenantID`（单一真相源）**

| 方案 | 分析 |
|---|---|
| A：独立 tenantmanager + Redis set | ① SRP 干净；② O(1) 查列表。但**双源真相**，app 删除不同步 → 漏隔离 |
| B：`AppInfo.TenantID` 字段（推荐） | ① 单一真相源，app 删了关系自动消失；② 迁移简单（默认 `admin`）；③ 实现最少。缺点是「tenant 的 app 列表」需 scan（量级小，trivial） |

**最优解：方案 B**
理由：多租户是安全边界，双源真相的 desync 会直接造成隔离泄露，危害大于代码耦合。且本方案中 `TenantID` 的角色是**写入时解析**（不是查询时 join），字段即可胜任。**修正原设计决策 #5。**

---

### 决策 3：落地范围与时序 —— **先定稿设计，再实施**

理由：决策 1/2 的选型直接决定 Sprint 1 的数据模型。本版文档即为定稿产物。

---

## 5. 重指派与回填（denormalize 的代价与对策）

`tenant_id` 写死进历史数据后，app 重指派不会自动追溯：

- **风险**：app X 从租户 A 换到租户 B，若不回填 → X 历史数据仍带 `tenant_id=A` → **租户 A 仍能查到 X 的历史（隔离泄露）**。

**对策：**

1. **重指派 = 显式管理操作**，强制触发该 app 历史数据回填：
   - ES：reindex 该 app 的索引，改写 `tenantId` 字段。
   - VM：export 该 app 的 series → 重打 `tenant_id` label → re-import。
2. **重指派罕见**：主要发生在初次把 app 从 `admin` 迁到新租户（此时历史数据极少或为零），成本有界。
3. **现有数据回填趁早**：当前仅 2 个 app、约 2 周数据，回填便宜；越晚越贵。

**折中（若接受非追溯）**：可声明「重指派仅对新数据生效，历史数据保留旧 tenant」，但**不推荐**——它会引入隔离泄露，除非明确有合规豁免。

---

## 6. 实施计划（更新后）

### Sprint 0：数据模型预留（1 天）
- [ ] 存储接口预留路由 seam（writer/reader 接受 `(signal, tenant)` 路由键，默认单一后端）—— 为未来「大租户独立链路」留口子，本轮不建多后端
- [ ] `StoredLogRecord` / `StoredSpan` 加 `TenantID` 字段；`baseLabels` 加 `tenant_id` label

### Sprint 1：数据模型 + 关系（3-4 天）
- [ ] `AppInfo` 加 `TenantID` 字段 + 迁移（现有 app → `admin`）
- [ ] `tenantmanager` 包（Tenant CRUD + Redis 持久化）
- [ ] Tenant API Key 管理（生成 + SHA-256 hash + 吊销 + `ValidateAPIKey` + L1 缓存）
- [ ] 写入时 token→(app_id, tenant_id) 解析与注入

### Sprint 2：认证层（2-3 天）
- [ ] `tenantAuthMiddleware`（sk_/ok_/tk_ 前缀路由）
- [ ] `WithTenantID` / `TenantIDFromContext`

### Sprint 3：查询隔离（2-3 天）
- [ ] `TraceQuery` / `MetricQuery` 加 `TenantID`
- [ ] ES `tenantId` 等值过滤 + VM `tenant_id` 等值注入
- [ ] Tempo / Prometheus Handler 注入 TenantID

### Sprint 4：管理 API + 回填（2-3 天）
- [ ] `/api/v2/tenants/*` CRUD + Key 管理 + App 归属
- [ ] 历史数据回填 + 重指派回填工具

---

## 7. 大租户独立链路（演进路径，本轮不建）

「某 tenant 量很大 → 独立写查链路」当前设计**从结构上不支持**（路由 key 只有 `signal`，`hybrid/provider.go:250 backendFor(signal)`）。这是**物理隔离**，超出本设计的**逻辑隔离**范围。

| 级别 | 手段 | 隔离强度 | 何时上 |
|---|---|---|---|
| **L0** | 单后端 + `tenant_id` 逻辑隔离（本设计） | 逻辑 | 当前 |
| **L1 软隔离** | per-app retention + per-tenant 查询限流 + 独立 ILM | 逻辑+限流 | 有租户开始偏大 |
| **L2 节点隔离** | ES shard allocation awareness 把大租户钉到专属 node | 物理节点 | 大租户查询影响他人 |
| **L3 独立链路** | 专属 ES 集群 / VM 实例 + `(signal, tenant)→backend` 路由层 | 物理后端 | 真·超大规模 |

**关键**：`app_id`/`tenant_id` 分片使「搬大租户到专属后端」是干净的**路由操作**（换个后端接收这些 id），不是重写。为此在 **Sprint 0 预留路由 seam**（把 `backendFor(signal)` 泛化为 `backendFor(signal, tenant)`，默认单一后端），现在加便宜，后补要动所有 provider。

---

## 8. 风险与考量（增量）

- **denormalize 一致性**：app 重指派需回填历史数据（见 §5），否则隔离泄露。
- **回填成本**：与数据量成正比，越早越便宜；当前量级（2 app、~2 周）trivial。
- **向后兼容**：admin 全局（无 TenantID）与静态 super key 行为不变；现有 app 迁移到 `admin` 租户零成本。
- **存储路由 seam**：本轮只留接口不建多后端（YAGNI），但 seam 必须先留。
