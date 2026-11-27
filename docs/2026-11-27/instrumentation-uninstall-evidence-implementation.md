# 动态插桩规则卸载确证与一致性修复（实施记录）

> 记录需求、决策、实施进展、验证结果与遗留问题。文档不含凭据或敏感信息。

## 1. 背景与实测依据

前几轮排查发现：规则删除后探针并非「感知规则状态」，而是收到一个 `dynamic_uninstrument` 任务；控制面在 7 天 retention 后物理删除 deleted 规则。实测数据：

| 实验 | 数据 | 结论 |
| --- | --- | --- |
| 正常删除闭环 | `removed/success` + runtime `reverted / is_effective=false` | 正常路径可靠 |
| apply 失败后删除 | uninstall `NOT_FOUND` → target `failed`，runtime 无插桩 | 任务 ACK 会误判 |
| 离线删除 + GC | GC 后 `GetRule=not found`，agent 回来 `pending_tasks=0` | **记录被删后无法补偿** |
| 保留记录对照 | 不跑 GC 时 agent 回来 `uninstall_after=1` | **记录不丢即可自愈** |
| 并发 `refreshRule ∥ DeleteRule` | 3 次 × 60 轮：8 / 5 / 9 次被覆盖回 `active` | 无 CAS 的确定性缺陷 |
| failed uninstall 被 GC | 单测确证规则被物理删除 | `failed` 不能当卸载终态 |
| runtime 快照覆盖率 | 17 条 deleted 规则中 14 条 `targets=0`（82%），刷新 20ms；有 target 的 3 条全部可用、235ms | 强确证可行但需 rule 级回填 |
| tk_ key scope | 14 个 tk_ 中仅 1 个有 `instrumentation:write` | purge 一期不要求 scope |

## 2. 用户决策

1. 强确证**必须包含 runtime snapshot**（是）。
2. CAS 与 targets 一起做成 `CommitRule` 原子提交。
3. 历史规则自动按「从未插桩」批量确证。
4. 自动校验 5 分钟 / 50 条，按**分布式全局口径**设计。
5. purge 由各租户管理自己 app 下服务的规则。
6. 冲突时 API 返回 **409**。
7. 不做探针侧 per-ruleId 串行（另批）。
8. purge 一期**不要求** `instrumentation:write` scope。

## 3. 实施内容

### 3.1 数据模型（`interface.go`、`store.go`、`redis_store.go`）

- `Rule` 增加 `Revision`（CAS 令牌，历史数据为 0）、rule 级确证字段 `UninstallConfirmed / UninstallEvidenceSource / UninstallConfirmedAtMillis`。
- `RuleTargetStatus` 增加弱信号 `UninstallTaskAcked` 与强信号 `RuntimeClean / RuntimeCleanAtMillis / EvidenceSource`；常量 `EvidenceSourceRuntimeSnapshot` / `EvidenceSourceNeverInstrumented`。
- 新增 `ErrRevisionConflict`、`SkipRevisionCheck` 与 `RuleStore.CommitRule(ctx, rule, targets, expectedRevision)`：一次原子写 rule + 可选 targets，成功回写 `rule.Revision`。
  - Memory：同一把锁内比较并写入。
  - Redis：`WATCH(rulesKey, targetKey)` + `HGet` 当前 revision + `TxPipelined` 写两个 key；冲突立即返回不重试。
- `SaveRule` 保留供新建/兼容，同时递增 revision。

### 3.2 服务层（`service.go`、`reconcile.go`）

- 8 处 `SaveRule` 写入点收敛：API 路径走 `commitRule`（冲突上抛→409）；`refreshRule` / `reconcileRule` 走 `commitRuleBestEffort`（冲突时**重读 + 仅合并 summary/operation/audits/timestamps/rule 级确证，不复制 DesiredState** + 重试一次）。
- `refreshTargetStatus` 写弱确证 `UninstallTaskAcked`（仅当卸载任务 success）。
- 新增 `PurgeRule(ctx, ruleID, reason)`：仅 deleted 规则可物理删除，记录结构化日志。
- 新增 `VerifyUninstall(ctx, ruleID)`（见 3.4）。
- `Start/Close` 启停 `EvidenceVerifier`。

### 3.3 GC 与补偿（`gc.go`、`reconcile.go`）

- `gcOnce` 判定改为：`deleted` + retention 到期 + **卸载确证**。确证来源：rule 级 `UninstallConfirmed`，或所有 target `RuntimeClean`。
- 未确证规则**永久保留**（这是自愈前提），超过 `gc_unconfirmed_alert_after_millis` 只告警不删除。
- `failed / offline / expired / removed` 不再因「终态」被删除；`applied` 也不会被误删。
- `PhysicalDeleteRule` 对 `ErrRuleNotFound` 幂等（多副本并发）。
- 开关 `gc_uninstall_evidence_required=false` 回滚到旧逻辑。
- reconcile 去掉 `ScopeType != Service` 限制（instance 规则纳入补偿）；deleted 且未确证的 target **不再 prune**，改为保留并置 offline，agent 回来即可补发 remove。

### 3.4 自动校验（`evidence_verifier.go`）

- 独立 worker，5 分钟周期 + ±10% jitter；复用 `RuntimeSnapshotStore.TryAcquireRefreshLease` 以伪 agentID `__evidence_scan_batch__` 做**全局批次租约**（Redis 模式跨副本单 owner，Memory 模式总授予）。
- `50 条` 是**全局每轮上限**（锁持有者计数），`5 分钟` 是全局周期；`gc_evidence_batch_lease_ttl_millis=120000`。
- `VerifyUninstall` 三分支：
  1. `targets=0 && !EverApplySucceeded` → rule 级确证 `never_instrumented`（82% 历史规则靠此收敛）。
  2. 有 targets → `RefreshRuleRuntimeSnapshot`，仅在 `SnapshotAvailable && !Stale && !Dirty && LastRefreshStatus=Success && !IsApplied && !IsEffective && 无 deleted_residual` 时写 `RuntimeClean`；全部 clean 后写 rule 级确证。
  3. 快照失败/不可用/脏/**查不到**，一律不写确证；发现 `deleted_residual` 记录告警，由 5s reconcile 补发 remove。
- 确证写回走 `commitRuleBestEffort`（CAS）。

### 3.5 租户级 purge（`instrumentation_tenant_handlers.go`、`router.go`）

- 新增 `/api/v2/tenant/instrumentation` 组，**不挂** `requireAdminMiddleware`（该中间件对 tk_ 返回 403），仅挂 `withInstrumentationActor`。
- 端点：`GET /rules`（按租户过滤 deleted 规则，支持 `unconfirmed=true`）、`POST /rules/{id}/verify`、`POST /rules/{id}/purge`。
- 归属校验：`tenantauth.IdentityFromContext` → `IsGlobal()` 放行；tk_ 用 `tokenMgr.(appTenantProvider).GetApp(rule.AppID).TenantID` 与身份比对，解析失败 fail-closed；`X-Tenant-Id` 对 tk_ 被共享中间件忽略。
- purge 前置：规则必须 deleted、`reason` 必填；记录 actor/keyID/tenantID/appID 到结构化日志。**一期不校验 scope**。
- `instrumentationError(err)`：`ErrRevisionConflict → 409`、`ErrRuleNotFound → 404`、其余 400。

### 3.6 前端 / 配置

- `types/instrumentation.ts` 增加确证字段；`api/client.ts` 增加 3 个租户端点方法。
- `InstrumentationPage.tsx`：新增「Unconfirmed (N)」筛选、deleted 详情的确证徽标、Verify 与 Purge 按钮（二次确认）。
- `config/template/config.yaml` 显式配置 5 个 GC/证据参数并注释全局口径。

## 4. 已完成验证（单测）

- `go build ./...`、`go test -count=1 ./...`（见第 5 节全量回归）。
- `TestCommitRuleCASMemory` / `TestCommitRuleCASRedis`（miniredis）：revision 递增、冲突返回、targets 原子性。
- **并发回归**：`TestConcurrentRefreshAndDeleteNoStaleOverwrite` 3 次 × 60 轮，结果均为 `conflicts=6, stale_overwrites=0`（修复前为 8/5/9 次 stale overwrite）。冲突现在转为明确的 409。
- `TestGCKeepsRulesWithoutUninstallEvidence`：failed/unconfirmed 规则 8 天后保留；写入 rule 级确证后可删除。
- `TestVerifyUninstallNeverInstrumentedBackfill`：无 target 且从未插桩 → `never_instrumented` 确证。
- `TestRetainedUnconfirmedRuleSelfHeals`：保留记录时 agent 回归补发 uninstall。
- `TestInstrumentationErrorMapsRevisionConflictTo409`：错误码映射。
- `TestEvidenceBatchLeaseElectsSingleReplica`：两个 store 共享 miniredis，仅一个副本能取得 `__evidence_scan_batch__` 租约。
- 既有 `TestGC*` / `TestReconcile*` / `TestRefreshTargetStatus*` 全部保持通过。

## 5. minikube 端到端验证（2026-11-27）

### 部署

- 渲染后 `extensions.admin.instrumentation_manager`：`type=redis`、`gc_uninstall_evidence_required=true`、扫描 300000ms / 50 条 / 批次租约 120000ms。
- 日志确认：`GC worker started {interval:60, retention:604800}`、`Evidence verifier started {interval:300, batch_lease_ttl:120, max_rules_per_scan:50}`。
- **构建方式说明（如实记录）**：`make docker-build` 因 Docker Hub 未认证拉取限流（429 `toomanyrequests`）失败，`golang:1.25-bookworm` 无法拉取。为不阻塞验证，改用宿主机 Go 1.25.0 执行与 `build.sh` 相同的 `CGO_ENABLED=0 go build -ldflags="-s -w" -o ... ./cmd/customcol`，再以 `FROM custom-otlp-collector:latest-amd64` 只替换二进制构建镜像（前端 `dist` 通过 `go:embed` 进入二进制）。`.env.make`、Dockerfile、compose 均未改动。首次因镜像未更新 + 新 ConfigMap 导致旧二进制报 `instrumentation_manager has invalid keys` 并 CrashLoop，替换镜像后恢复正常。

### 端到端结果

| 场景 | 实测数据 |
| --- | --- |
| 正常删除闭环 + 手动 verify | `uninstall_confirmed=true`、`uninstall_evidence_source=runtime_snapshot`、`revision=9`；target `removed/success`、`runtime_clean=true`、`uninstall_task_acked=true` |
| apply 重叠失败后删除 | uninstall 返回 `NOT_FOUND`、target `failed`；verify 仍写入 `runtime_snapshot` 确证（实测数据支撑「强确证必须含 runtime snapshot」） |
| 无 target 且从未插桩 | verify → `uninstall_evidence_source=never_instrumented` |
| **自动校验 worker 第一轮**（11:56:37，部署后约 5 分钟） | 18 条 deleted 中 11 条自动确证：`runtime_snapshot=4`、`never_instrumented=7`；7 条 `ever_apply_succeeded=true` 且无 target 的规则按设计进入人工复核队列并打 warn 日志 |
| 并发 CAS | 同一规则并发两次 PUT：`200` + `409 {"error":"rule was modified concurrently; retry the operation"}` |
| 409/404 映射 | `TestInstrumentationErrorMapsRevisionConflictTo409` 通过 |
| 前端 | 截图 `docs/2026-11-27/ui-preview/instrumentation-evidence.png`：`Unconfirmed (12)` 筛选、`Evidence: missing` 徽标、Verify / Purge 按钮均渲染 |

### 租户 purge 权限矩阵（无 scope 的两个临时 tk_ key）

| 用例 | 结果 |
| --- | --- |
| `tk_admin` purge 自己 app 的 deleted 规则 | 200，日志含 actor / tenant_id / key_id / reason |
| `tk_other` purge admin app 的规则 | 403 `rule belongs to a different tenant` |
| `tk_other` + `X-Tenant-Id: admin` | 403（impersonation 对 tk_ 无效） |
| purge active 规则 | 409 `only deleted rules can be purged` |
| 缺 reason | 400 `reason is required` |
| `GET /rules?unconfirmed=true`（tk_admin） | 200，13 条，app 均为默认租户 admin（`tenantOfApp` 对空 TenantID 回落 `DefaultTenantID`） |
| 同端点（tk_other） | 200，0 条 |

### 清理

- 本轮创建的 3 条临时规则全部 purge，2 个临时 tk_ key 全部 revoke；规则总数回到基线 23，`e2e-evidence*` / `e2e-cas*` 残留为 0。
- 历史 deleted 规则被自动确证的 11 条属于系统正常行为（其中 7 条 `never_instrumented`、4 条 `runtime_snapshot`），会在各自 retention（7 天）到期后由 GC 清理；未做手工数据改动。
- 端口转发、浏览器、明文临时文件已清理；渲染产物为模板值，未改 `.env.make` / Dockerfile / compose。

### 补充线上实测：三项「原本未实测」的场景（2026-11-27 下午）

用户指出这三项不该只留给单测，已全部在集群补测。为不误删历史规则，用 Python Redis 客户端**只把测试规则的 `updated_at_millis` 前移 8 天**，并把 `deleted_rule_retention_millis` 临时设为 4 天（历史 17 条 deleted 规则实测 age 0~2.8 天，均不满足）；`gc_interval_millis=10000`、扫描周期 60000ms、批次租约 30000ms、副本数 2。

| 场景 | 实测数据 |
| --- | --- |
| **A1 保留期到期 + 已确证 → 删除** | `nnSht6p5XLnKQWpV`（`runtime_snapshot` 确证、时间戳 8 天前）被 GC 物理删除，日志 `13:12:52 GC: physically deleted rule` |
| **A2 保留期到期 + 未确证 → 保留** | `OShpp6wyOoQeKPkO`（`ever_apply_succeeded=true`、无 target、无确证、时间戳 8 天前）在 25 秒、2~3 个 GC 周期后仍存在；时间戳随后恢复原值 |
| **A3 自动确证的副作用（如实）** | 另一条「未确证」规则 `75IKo7WxW2tMda91` 被 verifier 自动确证后才由 GC 删除——不是 GC 绕过确证，是自动校验按设计先收敛了它 |
| **B 多副本批次租约** | `kubectl scale --replicas=2` 后：`sv5kt` 出现 `batch lease acquired (13:06:05)`，`zr7j5` 连续三轮 `skipped cycle, another replica owns the batch lease (13:04:08 / 13:05:14 / 13:06:17)`；同一周期只有一个扫描者，且 GC 删除日志在两副本下只有一条（幂等） |
| **C 回滚开关** | `gc_uninstall_evidence_required=false` 重启后 `Evidence verifier started` 日志不再出现；`E6M9qXVqPl0jqDyO`（未确证、target=`removed`、时间戳 8 天前）被旧 GC 删除，日志 `13:18:13 GC: physically deleted rule`；切回 `true` 后 verifier 恢复启动 |

补充日志：`verifyEvidenceOnce` 增加 `batch lease acquired` / `skipped cycle` 两行 Info 日志（本轮新增，便于线上观测），编译后同样以「宿主机 Go 1.25 编译 + 旧运行时镜像替换二进制」部署。

**过程中的两个如实记录**：
- 第一次切副本后 PF 断开，一条创建命令因 `curl 000` 未产生任何规则，重建 PF 后重做，无脏数据。
- 回滚开关的第一次验证不干净（规则在切开关前已被 verifier 自动确证删除），因此在开关=false 状态下重做了一次，才得到上表 C 的证据链。

### 环境恢复（补测后）

重新渲染模板：`gc_uninstall_evidence_required=true`、扫描 300000ms / 50 条 / 租约 120000ms、`gc_interval_millis` 与 retention 回注释默认值；副本缩回 1；日志确认 `GC worker started {interval:60, retention:604800}` 与 `Evidence verifier started {interval:300, batch_lease_ttl:120, max_rules_per_scan:50}`。规则总数 23（deleted 17），`OShpp` 时间戳恢复原值 `1795588906757`，本轮临时规则全部由 GC 删除、无残留；端口转发与临时文件已清理。

### 补充实测：agent 离线时的 verifier 分支（2026-11-27 下午，探针已离线）

用户指出此时探针已全部离线，正好补上此前唯一没实测的分支。实测确认 registry 中 `14a50a48e204-1-1795679650119` 状态为 `offline`（`14:10:13` 心跳过期日志）。

用纯 API 构造，不改 Redis 数据：创建 `instance` scope 规则并把这个离线 agent 作为 `target_agent_ids`，删除后触发 verify。

| 步骤 | 实测数据 |
| --- | --- |
| 创建规则 | `targets=1`，`state=offline`，**无 `task_id`**（`dispatchOperationToAgent` 对离线 agent 不创建任务） |
| 删除规则 | target 仍 `offline`，`task_type=dynamic_uninstrument` 但无卸载任务，`runtime_clean` 为空 |
| runtime snapshot | `reachable_targets=0`、`offline_targets=1`、`last_refresh_status=skipped`、`last_error_message=agent is offline`、`stale_targets=1`；注意 `snapshot_available=true` 但刷新状态不是 success，正是需要防住的坑 |
| `POST /verify` | 返回 200，但 `uninstall_confirmed=null`、`uninstall_evidence_source=null`，target `runtime_clean=null`——**未把「查不到」当成「无残留」** |
| GC 保留 | 把该规则 `updated_at_millis` 前移 8 天（默认 retention 7 天已满足），等待 75 秒跨过 GC 周期后规则仍存在，`confirmed=null`；GC 日志无删除记录 |
| 清理 | 该临时规则经 `purge` 删除，规则总数回到 23（deleted 17） |

这里也实证了设计目标：删除时 agent 离线不会丢记录，规则保留、快照不误判，等 agent 回来后由 reconcile 补发 remove（补发自愈此前已由单测 `TestRetainedUnconfirmedRuleSelfHeals` 与隔离对照实验覆盖，本轮探针未重新上线，未在线上复现该步）。

### 补充实测：探针重新上线后的补发闭环（2026-11-27 下午）

用户把 demo 重新拉起后，registry 出现**新的 agent ID** `14a50a48e204-1-1795760309120`（旧 ID 为 `…-1795679650119`，说明是新的 JVM 进程）。

**纯只读观察结果**：上线后 25 秒，任务表无新增卸载任务，17 条 deleted 中仍是 7 条未确证，无补发日志。原因是这 7 条全部 `targets=0`（人工复核队列），没有可补偿的 target 对象；且旧 agent ID 已不在 registry。这是数据条件不满足，不是机制失效。

为验证机制本身，做了**可完全还原的最小构造**：给 `OShpp6wyOoQeKPkO`（未确证、`ever_apply_succeeded=true`、原本 targets 不存在）临时写入一个 target，指向当前在线 agent、`state=offline`、`last_dispatch_at_millis=0`。

| 步骤 | 实测数据 |
| --- | --- |
| reconcile 补发 | 14 秒内 target 从 `offline` 变为带 `task_type=dynamic_uninstrument` / `task_id=1dlhoVu4578Pmo8N` 的状态 |
| 探针执行结果 | `status=4 Failed`，`NOT_FOUND: Rule not found or not applied: OShpp6wyOoQeKPkO`（新 JVM 上从未插过这条规则，属预期） |
| runtime snapshot | `reachable_targets=1`、`runtime_found_targets=0`、`effective_targets=0`、`is_effective=false`、`drifted_targets=0` |
| verifier 强确证 | 规则收敛为 `uninstall_confirmed=true`、`uninstall_evidence_source=runtime_snapshot`、`revision=8` |

**这是决策 1 的又一次实证**：补发的卸载任务「失败」，但快照证明探针上无插桩，因此仍能正确确证；只依赖任务 ACK 会把这条规则永久挂起。同时也验证了「保留记录 → agent 回来 → 补发 → 快照确证 → 收敛」的完整线上闭环。

**还原**：删除注入的 `otel:instrumentation:targets:OShpp6wyOoQeKPkO`（原本不存在），清掉规则的三个确证字段并恢复 `updated_at_millis=1795588906757`；规则总数回到 23（deleted 17、未确证 7），与基线一致。补发产生的任务记录留在 Redis，按 `result_ttl=1h` 自动过期。

顺带核对：4 条 active 规则的 target 已归属新 agent ID 且 `state=applied`，控制面与探针状态一致，未出现新旧 agent 串扰。

### 仍未实测（如实）

- 真正的 7 天自然流逝：用时间戳前移 + 保留期缩短等价构造，未等待 7 天。
- 同一 agent ID 失联后恢复（网络分区、JVM 未重启）的补发：本次是新 agent ID，旧 ID 永久消失；该场景由单测覆盖。

## 6. 遗留与边界

- 探针侧 `TaskDispatcher` 仍是 `newCachedThreadPool` + 仅按 taskId 去重（决策 7：本批不做）；apply/remove 乱序的**代码条件**仍在，单次集群试验未复现后果。
- 探针执行 uninstall 持续失败时，本批做到「记录保留 + 持续重试 + 快照告警」，不能自动强制卸载，需人工 purge。
- `ever_apply_succeeded=true` 且 targets 为空的历史规则（线上实测 7 条）无法从控制面证明探针状态，按设计不自动确证，只告警并进入人工复核队列；前端 `Unconfirmed` 筛选可直接定位。
- Memory 模式（默认 `DefaultConfig`，当前部署为 redis）无跨副本租约/CAS 共享，文档标注仅单副本使用。
- 快照 TTL 20s、失败 TTL 5s；确证写入后 GC 只读持久化字段，避免易失缓存造成误判。
- runtime snapshot 刷新是查询类探针任务，会更新快照缓存；批量扫描有全局配额与 per-agent lease 双重限流。

## 7. 收尾

- 本批所有改动未提交；验证用端口转发、临时 key 与临时文件按验证清单清理。
