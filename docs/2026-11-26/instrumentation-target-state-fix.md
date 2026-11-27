# Instrumentation 目标状态回退修复（Low Coverage 根因）

> 记录需求、根因、实施进展、验证结果与遗留问题。文档不含凭据或敏感信息。

## 1. 现象与需求

`http://custom-collector.devcloud/ui/instrumentation?app_id=xUXCbjcSnSy5LZUJ&service_name=springboot-jdk8u202-otel-demo`
页面上 4 条 active 规则的 verdict 全部显示 `Low Coverage`。用户要求「根治」，不是调大 TTL 或改展示文案。

## 2. 根因（确证）

前端 verdict 由 `InstrumentationPage.tsx:286-315` 计算：
`coverage = applied / (applied + running + pending + failed)`，低于 50% 且无 failed 时显示 `Low Coverage`。

排查时 4 条规则的实际 `summary` 均为 `applied=0 / pending=1 / failed=0`，所以 coverage=0。目标详情进一步显示
`task_status=success` 但 `state=pending`，且 `ever_apply_succeeded=true`（曾真实生效）。因此状态是**后来被回退**的。

因果链：

1. `config/template/config.yaml:310` 配置 `result_ttl: 1h`；
2. 任务结果到期后 `GetTaskResult` 返回 `found=false`，但任务记录仍在，`GetTaskStatus` 返回 success；
3. `InstrumentationService.refreshTargetStatus`（`service.go:332-375`）在结果缺失时走 info 分支，无条件执行
   `target.State = mapInfoStatusToTargetState(info.Status)`；
4. 旧 `mapInfoStatusToTargetState` 没有 `TaskStatusSuccess` 分支，落到 `default → TargetStatePending`，
   覆盖了已记录的 `applied`（`service.go:602-611` 旧实现）；
5. `applied_targets` 归零 → 页面全线 `Low Coverage`。

时间证据：任务创建于 `15:54:30`，目标状态在 `16:54:33` 被刷新，正好相隔 1 小时（TTL 到期后的第一次刷新）。

## 3. 改动（只改状态机，MCP / 前端 / 配置均未动）

| 文件 | 说明 |
| --- | --- |
| `extension/controlplaneext/instrumentationmanager/service.go` | `mapInfoStatusToTargetState(status, desired)` 补齐终态映射：success → `applied`（active）/ `removed`（paused|deleted）、failed/timeout/cancelled/resultTooLarge → `failed`；保留 pending/unspecified → `dispatched` 的既有契约 |
| 同上 | `refreshTargetStatus` 的 info 分支加终态保护：当前状态是 `applied/removed/failed` 且 info 映射结果非终态时，只更新 `task_status`，不覆盖 `state` |
| 同上 | 新增 `isResultDerivedTerminalState`，并在注释中说明它与 `gc.go` 的 `isTerminalTargetState`（GC 语义，不含 applied）的区别 |
| `extension/controlplaneext/instrumentationmanager/target_status_refresh_test.go`（新增） | 结果 TTL 过期后的 9 种状态组合、result 仍存在时的权威性、offline/expired 跳过、info 终态映射与既有非终态语义 |

**未改动**：`result_ttl`（调大只能延迟问题）、前端 verdict（「pending 显示 In Progress」属展示层可选改进，本次未做）、E2E 历史规则数据。

**边界确认**：新任务下发时 `reconcileExistingTarget`（`reconcile.go:393-396`）会先把 `State` 置为 `dispatched`、`TaskStatus` 置为 `pending`，因此终态保护不会阻碍新任务流转；离线/过期目标在函数开头已跳过。

## 4. 验证结果

- `go build ./...`、`go test -count=1 ./...` 全通过；新增单测所在包通过；改动文件 lint 0。
  - 期间曾因完全委托 `mapResultStatusToTargetState` 导致既有测试
    `TestResumeRuleDoesNotShowOfflineTargetsAsApplied` 失败（pending 应映射 `dispatched` 而非 `pending`），
    已改为「只补齐终态映射、保留非终态语义」，复测通过。
- minikube：渲染 → 构建 → 部署 → 重启，Pod `1/1 Running`。
- 部署后 4 条 active 规则的 `summary` 全部为 `applied_targets=1 / pending=0 / failed=0 / running=0`。
- 目标详情：`state=applied`、`task_status=success`；任务创建于 `15:54:30`，验证时已过 3.15 小时，
  远大于 `result_ttl=1h`，即**结果确实已过期**的情况下状态仍为 applied。再次主动触发刷新后状态不变
  （`changed=false`，没有再次写库）。
- 浏览器实测页面：`Healthy` / `1/1 active applied` / `100% coverage`，目标行 `applied` + `task success`。
  截图 `docs/2026-11-26/ui-preview/instrumentation-healthy.png`。

## 5. 遗留问题

- **任务记录也过期时不会自愈**：若 `GetTaskStatus` 返回 `nil` 或错误，`refreshTargetStatus` 直接返回，
  已持久化为 `pending` 的历史目标不会被修正（`ever_apply_succeeded` 也不参与状态计算）。
  本批修复保证的是「结果过期但任务记录仍在」这一常见场景不再回退；若将来连任务记录也被清理，
  需要额外的数据修复或引入单调的终态标记。
- 页面列表里仍可见若干历史 `pending` / `deleted` 的 E2E 验证规则（如 `e2e-p3-explicit-override`），
  它们不是本次 4 条 active 规则，未做数据清理。
- 展示层「pending 占比高时显示 In Progress 而非 Low Coverage」未实施；状态机修复后 active 规则已不再
  落入该分支，但若将来出现真实的部分未生效场景，该文案仍可能引起误解。

## 6. 收尾

验证用端口转发与浏览器已释放，临时文件已清理。本批改动未提交。
