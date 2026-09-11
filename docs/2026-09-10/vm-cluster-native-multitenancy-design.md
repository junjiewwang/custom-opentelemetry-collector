# VictoriaMetrics 原生多租户迁移设计(硬隔离)

> 状态:设计草案 · 日期:2026-09-10 · 前置文档:[[multi-tenancy-architecture-design-v2]]

## 1. 背景与问题

当前指标信号的租户隔离是 **label 软隔离**:

- **写**:`metric_writer.go:171` 把 `tenant_id` 作为 label 写入(`pt.TenantID != ""` 时),来源是
  `stored_metric.go:71` 从 resource attr `tenant_id` 读取,该 attr 由 tokenauthprocessor 注入。
- **读**:`buildSelector`(`metricsql.go:22`)拼 `tenant_id="X"` filter;查询结构体显式带
  `TenantID`;枚举方法读 `tenantctx` 上下文。
- **VM 实例**:单节点 `vmsingle.vm-minimal:8428`,启动参数**无** `-multitenancy.enabled`,
  客户端走单租户 `/api/v1/import/prometheus`(`client.go:128`)。

**软隔离的本质风险**:隔离由「查询层是否记得加 `tenant_id` filter」决定。任何一条新增读路径
漏加即泄露——2026-09-09 刚修复的枚举缺口(6 个枚举方法漏 4 个)就是这个风险的实例。

## 2. 现状:集群里已有原生多租户原型

Minikube 集群里存在两套 VM:

| | `vmsingle`(vm-minimal,13d) | `vm-cluster`(vm-cluster,15h) |
|---|---|---|
| 架构 | 单节点 | vmauth + vminsert + vmselect + vmstorage |
| 多租户 | 无(未开 `-multitenancy.enabled`) | **原生**(vmauth 按 bearer token 路由 accountID) |
| 隔离 | `tenant_id` label 软隔离 | `/select/<acct>/`、`/insert/<acct>/` 硬隔离 |
| collector | ✅ 当前使用 | ❌ 未接入 |

`vm-cluster` 的 `vmauth-config` 已配好两个 demo 租户(实测跑通、`wrong-token`→401):

```yaml
users:
  - bearer_token: "tenant-a-token" → /select/1/prometheus + /insert/1/prometheus
  - bearer_token: "tenant-b-token" → /select/2/prometheus + /insert/2/prometheus
```

即:**硬隔离路径已被原型验证**,本文设计如何把 collector 从 vmsingle 迁到 vm-cluster。

## 3. 目标与非目标

**目标**:指标信号从 vmsingle(label 软隔离)迁到 vm-cluster(accountID 硬隔离);
trace/log/admin 仍走 ES(无原生多租户,继续 `tenantId.keyword` 软隔离)。

**非目标**:trace/log 的硬隔离(ES 做不到);多套 VM 集群的运维自动化。

## 4. 现状代码事实(迁移触及点)

| 关注点 | 现状 | 位置 |
|---|---|---|
| 写 URL | `writeBase + /api/v1/import/prometheus` | `client.go:128` |
| 读 URL | `readBase + /api/v1/{query,query_range,label/.../values,labels,series}` | `client.go:169-265` |
| write/read base | 静态字段,来自单一 `endpoint` 配置 | `client.go:87-100` |
| tenant label 写入 | `baseLabels` 写 `tenant_id` | `metric_writer.go:171` |
| tenant 过滤 | `buildSelector` 拼 `tenant_id="X"` | `metricsql.go:22` |
| 配置 | `victoriametrics.endpoint = vmsingle...8428` | `config/template/config.yaml` |
| tenant 模型 | `Tenant{ID, Name, Status, ...}` **无 account_id** | `tenantmanager/model.go:24` |

## 5. 设计

### 5.1 tenantID → accountID 映射(核心新增)

VM 原生多租户需要数值 accountID(0..2^32),而租户 ID 是字符串。在 tenant 模型加字段并持久化到 Redis:

```go
type Tenant struct {
    ID        string `json:"id"`
    Name      string `json:"name"`
    Status    string `json:"status"`
    AccountID uint32 `json:"account_id,omitempty"` // 新增:VM 原生多租户账号
    // ...
}
```

- **分配**:创建租户时用 Redis `INCR otel:tenant_account_seq` 单调递增(无碰撞、可回放),从 1 开始;
  0 保留给默认 admin 租户 / 全局数据(见 §5.4)。
- **查询**:tenantmanager 提供 `ResolveAccountID(ctx, tenantID) (uint32, error)`,结果可缓存
  (account 分配后基本不变)。
- **兼容**:`account_id` 为 0 或缺失 = 回退到现有 label 软隔离(平滑迁移期用)。

### 5.2 写路径

1. 配置新增两个 endpoint(直连 cluster,见 §5.5):
   ```yaml
   victoriametrics:
     insert_endpoint: "http://vminsert.vm-cluster:8480"   # 写
     select_endpoint: "http://vmselect.vm-cluster:8481"   # 读
   ```
2. `httpVMClient` 的写请求 URL 由 `writeBase + /api/v1/import/prometheus` 改为
   `insert_endpoint + /insert/<accountID>/prometheus/api/v1/import`。
3. 写入前按 `accountID` **分组**——`WriteMetricPoints` 的批量可能跨租户,须按 account 拆批,
   分别 POST 到各自的 `/insert/<acct>/`。
4. **不再写 `tenant_id` label**(account 已物理隔离)。`baseLabels` 去掉 `tenant_id` 分支。
   - 注:ES 侧 trace/log 仍需 `tenantId` 字段,两链路语义保持「同租户」一致即可,label 名不强制。

### 5.3 读路径

1. 读请求 URL 改为 `select_endpoint + /select/<accountID>/prometheus/api/v1/{query,...}`。
2. `buildSelector` **不再拼 `tenant_id="X"`**(account 隔离已保证),其余 label 过滤不变。
3. 查询结构体 `MetricQuery.TenantID` **保留**(前端/auth 语义不变),VM reader 内部
   `accountID = tenantMgr.ResolveAccountID(ctx, query.TenantID)` 后拼 URL。
4. 枚举方法(`ListMetricNames` 等)同理:`tenantctx.TenantIDFromContext(ctx)` → resolve account →
   URL 带 `/select/<acct>/`。不再需要把 tenant 折进 `buildSelector`。

### 5.4 默认租户与全局数据

- **admin 租户** = accountID 0(VM 默认 tenant)。admin/operator 查询不带 `/select/<acct>/`
  前缀,命中 account 0。
- **全局数据**(写入时无 tenant,如 collector-self 内部指标)也落 account 0,admin 可见,
  租户不可见——与现有语义一致。
- 原生模型下 admin 若走 account 0 则**看不到**其它 account 的数据。原软隔离下 admin 无 filter
  可见全部,迁移后默认视图收窄为 account 0——这是语义变化,但由「admin 三读面」补齐(见下),
  不构成能力退步,反而是「最小可见 + 按需查看」的更安全方向。

**admin 三读面(定稿结论)**

admin 的「看」拆成三个正交平面,各有独立通道,**均不需要查询层跨 account fan-out**:

| 平面 | 诉求 | 通道 |
|---|---|---|
| 数据平面(复现) | 看某租户实际看到什么(排查/复现) | 扮演:显式传 tenantID → `/select/<acct>/` |
| 数据平面(计量) | 某租户的业务用量(调用量/错误率) | 扮演 + 查,或直接读存储账本 |
| 资源平面 | 每租户吃多少存储/序列、全局资源分布 | `vmstorage /metrics` 的 `vm_tenant_*` per-account 账本 |

- **扮演(admin impersonation)**:admin 显式指定 scope(tenantID),走与租户相同的
  `ResolveAccountID → /select/<acct>/` 路径。需把「认证身份 actor」与「生效作用域 scope」拆开
  (见 §7);`tk_` 键硬绑定自身租户,请求携带的 impersonation 参数必须被忽略/拒绝;仅 `ok_`/`sk_`
  可扮演,并记审计日志。
- **存储账本**:硬隔离自带,`vmstorage /metrics` 暴露 `vm_tenant_used_bytes` / `vm_tenant_rows` /
  `vm_tenant_series` / `vm_tenant_timeseries_created_total`(label `accountID`/`projectID`)。
  admin 资源面板拉取后把 `accountID` 反解回 tenant 名即可,数据面零查询代码改动。

### 5.5 vmauth vs 直连

| | 直连 vminsert/vmselect(推荐) | 经 vmauth |
|---|---|---|
| account 路由 | collector 自己 resolve + 拼 URL | vmauth 按 bearer token 路由 |
| 每租户凭证 | 无(accountID 即隔离) | 需在 vmauth 配 token + collector 管 token |
| 运维 | 少一层组件依赖 | 集中管控、可做限流/审计 |
| 与现有原型 | 不依赖 demo token | 复用 `tenant-a/b-token` 思路 |

推荐 **直连**:collector 已有 tenantmanager,resolve accountID 是自然延伸;避免每新增租户改
vmauth 配置。vmauth 可作**可选前置网关**留给合规/限流场景。

### 5.6 涉及改动清单(预估)

1. `tenantmanager/model.go` + `repository_redis.go`:加 `AccountID` 字段 + `ResolveAccountID`。
2. `tenantmanager/service.go`:创建租户时分配 accountID。
3. `victoriametrics/client.go`:write/read base 拆分 + 按 account 拼 URL。
4. `victoriametrics/metric_writer.go`:去掉 `tenant_id` label,按 account 分组写。
5. `victoriametrics/metric_reader.go` + `metricsql.go`:去掉 `tenant_id` filter,resolve account。
6. `observabilitystorageext/extension.go` + config 转换:新增 insert/select endpoint。
7. `config/template/config.yaml`:新增 `victoriametrics.insert_endpoint/select_endpoint`。

## 6. 迁移策略

1. **双写过渡**:新数据同时写 vmsingle(带 label,兼容旧读)与 vm-cluster(按 account),观察一致性。
2. **读切换**:读路径切到 vm-cluster,灰度(按 tenant 开关)。
3. **下线 vmsingle**:停双写、停 vmsingle;老数据若需长期保留可一次性 backfill 到对应 account。
4. **回滚**:把 `hybrid.metric` 指回 vmsingle 即回到 label 软隔离(现有配置已支持)。

## 7. 权衡与开放问题

- **硬隔离只覆盖指标 1/3**:trace/log 仍 ES 软隔离,`tenantId.keyword` 纪律照旧要守。迁移不减少
  ES 侧的防漏义务。
- **admin 可见性语义变化(已定稿,见 §5.4)**:原生 account 下 admin 默认只见 account 0。原
  「跨 account fan-out」方案经论证**不需要**——它假设 admin 需在查询层合并租户数据,而实际
  诉求由「admin 三读面」(扮演 / 存储账本 / account 0)全部覆盖,fan-out 是用错了工具。
- **admin 扮演的 actor/scope 分离**:`TenantID` 字段现身兼「认证身份」与「生效作用域」两职,
  支持扮演后必须拆开——actor 由 key 类型决定授权,scope 决定拼 `/select/<acct>/`。`tk_` 键
  scope 恒等于自身租户,忽略任何 impersonation 参数;`ok_`/`sk_` 才可扮演,并写审计日志。
- **ES 软隔离无存储账本(新开放项)**:指标迁移后 `vm_tenant_*` 白送 per-tenant 存储计量;
  trace/log 的 ES 侧没有原生账本,per-tenant 存储占用需按 `tenantId` 聚合或 per-app index
  统计(再靠 app→tenant 映射),成本与准确性都差一截。trace/log 的存储计量需单独立项。
- **account 配额/回收**:VM account 无内置配额,内存/磁盘隔离需靠 storage 层配置。
- **回填成本**:vmsingle 历史数据(带 `tenant_id` label)迁到 account 需按 tenant 拆写。

## 8. 建议

当前规模(单 admin 租户 + 少量 app、无跨租户对抗威胁)下,label 软隔离 + 已补的枚举隔离
**已正确够用**。vm-cluster 原生多租户是**合规强隔离**场景的硬方案:

- 若目标只是「防自己写漏 filter」→ 优先做**软隔离防漏测试**(对每个 reader 方法断言 tenant 过滤存在),成本远低于迁移。
- 若目标是「多租户 SaaS、租户间强隔离合规」→ 按本文 §5 迁移到 vm-cluster。

建议先落防漏测试,把软隔离加固到「漏一条就红」;迁移 vm-cluster 作为独立的后续 feature 立项。
