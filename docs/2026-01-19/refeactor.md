## 进度

- [x] **PR-00 迁移验收门槛与开关（仅模板/文档，占位不生效）**（2026-01-16）
  - `config.yaml`：新增（注释形式）迁移开关占位，避免当前阶段未知字段导致配置加载失败
  - `refeactor.md`：建立进度记录区，后续每个 PR 落地后在此更新

- [x] **PR-01 新增 `model` 与双向 converter（仅新增代码，不改调用方）**（2026-01-16）
  - 新增 `custom/controlplane/model`：内部数据模型（`AgentConfig`/`Task`/`TaskResult`/`Chunk`）
  - 新增 `custom/controlplane/conv/probeconv`：probe pb（`custom/proto/controlplane/v1`）↔ `model`
  - 新增 `custom/controlplane/conv/legacyconv`：legacy JSON types（`custom/proto/controlplane_legacy/v1`）↔ `model`

- [x] **PR-02 `controlplaneext` 增 `ControlPlaneV2`（model 签名，PR-02 阶段为 facade）**（2026-01-16）
  - 新增 `ControlPlaneV2` 接口：新增 `UpdateConfigV2`/`SubmitTaskV2`/`ReportTaskResultV2`/`UploadChunkV2` 等 model 签名方法
  - `Extension` 实现 `ControlPlaneV2`：内部通过 `legacyconv` 适配到现有 legacy-based manager，保证行为不变
- [x] **PR-05 Chunk 上传 API 改 model（ChunkManager/Extension 内部逻辑迁移到 model）**（2026-01-19）
  - `ChunkManager` 核心处理改为 `model.ChunkUpload`/`model.ChunkUploadResponse`，优先用 `upload_id` 作为会话 key（无则回退 `task_id`）
  - `Extension` 的 `UploadChunk`（legacy）与 `UploadChunkV2`（model）复用同一套 model 实现，legacy 仅做入参/出参适配
- [x] **PR-03 `agentgatewayreceiver` 改为 probe↔model（TaskResult/Chunk 上传链路切 v2）**（2026-01-19）
  - receiver 持有 `controlplaneext.ControlPlaneV2`：上报任务结果/分片上传走 model 签名（底层仍由 `controlplaneext` facade 适配，行为不变）
  - `ReportTaskResult`/状态上报内嵌 `TaskResult`：probe proto → `model.TaskResult`（`probeconv`）→ `ReportTaskResultV2`
  - `UploadChunkedResult`：probe proto → `model.ChunkUpload`（`probeconv`）→ `UploadChunkV2`，再返回 probe `ChunkedUploadResponse`
- [x] **PR-04 `longpoll` 内部类型从 legacy 切到 model（Config/Task 返回 model，receiver 不再依赖 legacy）**（2026-01-19）
  - `longpoll.PollResponse` 的 `Config/Tasks` 改为 `model.AgentConfig`/`model.Task`；长轮询内部用私有 legacy JSON DTO 做存储编解码（存储格式暂不变，留待 PR-06/07）
  - `agentgatewayreceiver` 输出链路统一为：longpoll(model) → `probeconv.AgentConfigToProto` / `probeconv.TasksToProto` → probe proto
  - `custom/receiver/agentgatewayreceiver` 目录已消除对 `controlplane_legacy` 的 import；`custom` 模块 `go test ./...` 通过
- [x] **PR-06 Nacos ConfigManager v2 dataId + 双读/可选双写**（2026-01-19）
  - `ConfigManagerV2/OnDemandConfigManagerV2` 落地；Nacos 存储支持 `dataId + nacos_dataid_suffix`（默认 `.v2`）
  - `read_prefer_v2` 控制读优先级；`dual_write` 控制在 v2 开启时是否同时写回 legacy dataId（便于回滚）
- [x] **PR-07 Redis TaskStore v2 keyprefix + 双读/可选双写**（2026-01-19）
  - `extensions.controlplane.task_manager.migration.*`：新增迁移开关（`enable_v2/read_prefer_v2/dual_write/key_prefix_v2`）
  - `custom/extension/controlplaneext/taskmanager/store/redis.go`：Redis TaskStore 支持 v1/v2 namespace 双读；开启 `dual_write` 时 best-effort 双写/回填；v2 存储为 model JSON
  - `custom/receiver/agentgatewayreceiver/longpoll/task_handler.go`：longpoll 任务旁路支持 v1/v2 前缀双读 + 双 channel 订阅，避免“任务写入 v2 但 poll 仍读 v1”
  - `custom/config/template/config.yaml`：补充 task_manager migration 配置示例（默认注释）
  - 本地 `gofmt` + `go test ./...` 通过
- [x] **PR-08 Admin API v2（JSON）与旧端点兼容**（2026-01-19）
  - `custom/extension/adminext/router.go`：新增 `/api/v2` 路由（auth 规则与 v1 一致）
  - `custom/extension/adminext/handlers.go`：新增 v2 handlers，JSON 入参/出参使用 `model`；内部优先调用 `OnDemandConfigManagerV2`，否则 fallback 到 v1 并用 `legacyconv` 适配
  - 任务接口：`/api/v2/tasks` 支持 list/create/get/cancel/batch（内部复用现有 `taskMgr`）
  - 本地 `gofmt` + `go test ./...`（`custom/` 模块）通过
- [x] **PR-09 删除 legacy 目录（硬切，完成）**（2026-01-19）
  - **决策**：不保留 v1 admin；不继续读取 v1 存量数据（Nacos v1 dataId / Redis v1 keys 可直接放弃）
  - **实施拆分**：先将 adminext/WebUI 完整切到 `/api/v2`，再把 `controlplaneext/taskmanager/configmanager` 的核心接口统一为 `model`，最后删除 `custom/proto/controlplane_legacy` 与 `legacyconv`
  - **上线语义（研发阶段可控）**：允许升级过程中丢弃未完成任务与历史配置；要求升级前写入 v2 dataId 与使用 v2 Redis 前缀
  - **子阶段进度**（2026-01-19）：
    - [x] adminext 路由切 v2-only：删除 `/api/v1` 路由，所有端点迁移到 `/api/v2`
    - [x] WebUI 请求路径切 `/api/v2`：`api.js` base path + `app.js` Arthas ws url + task create payload 字段对齐 model
    - [x] 删除 adminext 中 v1 handlers 和 `controlplane_legacy` import：`handlers.go` 不再直接引用 `controlplanev1.*`
    - [x] TaskManager model 化：接口/TaskInfo/store 切到 `model.*`，删除 v1/v2 双读分支
      - `store/interface.go`：`TaskInfo`/`ApplyTaskUpdateResult` 字段改为 model 类型；新增 `IsTerminalStatus()` 公共函数
      - `store/memory.go`：`SaveResult`/`GetResult` 签名改为 `*model.TaskResult`
      - `store/redis.go`：**完全重写**，删除 `MigrationConfig`/v1-v2 双读逻辑，简化为 model-only 存储
      - `taskmanager/interface.go`：删除 `MigrationConfig` 类型及 `Config.Migration` 字段
      - `taskmanager/factory.go`：`NewRedisTaskStore` 调用简化（无需 migration 参数）
      - `controlplaneext/extension.go`：ControlPlane 接口实现使用 `legacyconv` 转换（边界层适配）
      - `adminext/handlers.go`：v2 handlers 直接使用 model 类型，无需转换
      - `longpoll/task_handler.go`：**完全重写**，删除 v1/v2 双读逻辑，简化为 model-only
      - `legacyconv/task.go`：新增 `TasksToLegacy()`/`TasksFromLegacy()` 批量转换函数
    - [x] ConfigManager model 化：以 `model.AgentConfig` 为唯一类型，删除双读/双写分支
      - `configmanager/interface.go`：合并 `ConfigManager`/`ConfigManagerV2` 和 `OnDemandConfigManager`/`OnDemandConfigManagerV2`，删除 `MigrationConfig`
      - `configmanager/memory.go`：使用 `model.AgentConfig`
      - `configmanager/nacos.go`：删除双读双写逻辑，简化为 model-only
      - `configmanager/on_demand.go`：删除双读双写逻辑，简化为 model-only
      - `configmanager/factory.go`：删除 multi_agent_nacos 支持，重定向到 on_demand
      - 删除 `configmanager/multi_agent_nacos.go` 和 `configmanager/multi_agent_nacos_test.go`
      - `extension.go`：适配新的 ConfigManager 接口
      - `adminext/handlers.go`：删除旧的类型断言逻辑
    - [x] ControlPlane 接口收敛：移除 legacy `ControlPlane`，将 `ControlPlaneV2` 扶正
      - `extension.go`：`ControlPlane` 接口改为 model 类型，`ControlPlaneV2` 改为类型别名
      - 删除 `config_manager.go`（旧的内存配置管理器）
      - `status_reporter.go`：删除 legacy 类型依赖
      - `task_executor.go`：使用 `model.Task`/`model.TaskResult`
      - `task_handlers.go`：使用 `model.Task`/`model.TaskResult`
      - `controlplane_service.go`：方法调用从 `*V2` 改为直接调用
    - [x] 删除 legacy 目录：删除 `custom/proto/controlplane_legacy/` 与 `custom/controlplane/conv/legacyconv/`
      - `agentregistry/interface.go`：新增内部 `HealthState`/`HealthStatus` 类型，替代 `controlplanev1.HealthStatus`
      - `agentregistry/*.go`：删除 `controlplanev1` import，`UpdateHealth` 使用内部 `*HealthStatus`
      - 测试文件更新：所有测试文件使用 `model.*` 或包内类型，删除 `controlplane_legacy` 依赖
      - 删除 `custom/proto/controlplane_legacy/` 目录
      - 删除 `custom/controlplane/conv/legacyconv/` 目录
      - 验证：`go build ./...` 和 `go test ./...` 通过

---

**Verifying TaskResultStatus**

I'm considering the TaskResultStatus enum and its potential nested fields, like status, errorCode/message, resultData, resultJson, and started/completed. I think it’s important to verify these details by reading the proto file. I'll perform tool calls to access the relevant files. This way, I can ensure I'm getting the correct information before moving forward. It feels good to have a plan to verify things properly!
我会先把 probe/legacy 两套 `proto`（尤其 `AgentConfig`、`TaskResult`）的字段定义读出来，确认真实差异；然后按“可直接拆 Jira/PR”的粒度，逐工作包列出：要改的具体文件、关键函数签名改动点、字段映射清单与兼容开关/回滚点。
### 字段差异先对齐（后面 checklist 会引用）
#### **`AgentConfig`：legacy（内部 JSON model） vs probe（对探针 pb）**
- **legacy（当前内部在用）**：`custom/proto/controlplane_legacy/v1/types.go` 里的 `AgentConfig`
  - `config_version`（string）
  - `sampler { type, ratio, rules_json }`（注意：规则是 **JSON string**）
  - `batch { max_export_batch_size, max_queue_size, schedule_delay_millis, export_timeout_millis }`
  - `dynamic_resource_attributes`（map）
  - `extension_config_json`（string）
- **probe（对探针）**：`custom/proto/controlplane/v1/config.proto` 里的 `AgentConfig`
  - `version { version, etag }`（注意：**etag 成为一等字段**）
  - `sampler { type, ratio, rules[] }`（注意：规则是 **结构化 repeated message**）
  - `batch` 字段基本同名同义
  - `dynamic_resource_attributes`、`extension_config_json`

**关键映射差异/坑点**
- **etag**：legacy 内部模型没有；当前实现是“etag 作为 longpoll 旁路字段”，在 `agentgatewayreceiver/controlplane_convert.go` 里由 `legacyConfigToProto(cfg, etag)` 注入。
- **sampler rules**：legacy 是 `rules_json`，probe 是 `rules[]`。当前实现明确“不解析”（`Rules: nil`），所以探针侧如果依赖 rules，会拿不到。
- **版本字段**：probe 的 `ConfigVersion` 是 message（version+etag），legacy 只有 string，需要 model 统一语义。

#### **`TaskResult`：probe 两种形态 + legacy（内部 JSON model）**
- **probe #1（状态上报里带的 `TaskResult`）**：`custom/proto/controlplane/v1/task.proto`
  - `status` 枚举：`SUCCESS/FAILED/TIMEOUT/CANCELLED/RESULT_TOO_LARGE`
  - 额外字段：`result_data_type`、`retry_count`、`compression`、`original_size`、`compressed_size`、`result_json`（string）
- **probe #2（单独上报 `TaskResultRequest`）**：`custom/proto/controlplane/v1/poll.proto`
  - `status` 枚举：稳定集合 `PENDING/RUNNING/SUCCESS/FAILED/TIMEOUT/CANCELLED`
  - `error_code`、`error_message`、`result_data`、`result_json`、`started/completed/execution`
- **legacy（当前内部在用）**：`custom/proto/controlplane_legacy/v1/types.go` 里的 `TaskResult`
  - `status`：`PENDING/RUNNING/...`（允许 JSON string 反序列化并归一化）
  - `error_code`/`error_message`
  - `result`（`json.RawMessage`）+ `result_data`（bytes）
  - `started_at_millis`/`completed_at_millis`/`execution_time_millis`

**关键映射差异/坑点**
- probe 有“**两套 status enum**”（一个稳定集合含 running/pending，一个偏“结果枚举”含 result-too-large/compression），legacy 只有 `TaskStatus`；因此 model 必须能表达 **running/pending + 结果过大/压缩信息**（否则会丢语义）。
- `result_json`：probe 是 string；legacy 内部用 `json.RawMessage`，需要统一为 model 的 `json.RawMessage`（或 `[]byte`）更合理。
- 时间戳：当前内部主流用 `*_millis`；不要引入 `unix_nano` 混用（legacy 的 `.proto` 里有 `*_unix_nano`，但你们内部实际用的是 `types.go` 的 `*_millis`）。

---

### 可直接拆 Jira/PR 的 checklist（按工作包；每条≈一个 PR）
下面每条都写了：**改哪些文件**、**改哪些签名**、**要补哪些映射**、**兼容/回滚开关**。命名仅建议，你们可以按团队习惯改。

---

## 0. PR-00：迁移验收门槛与开关（纯基础设施）
- **目标**：让“能删 legacy”可度量、迁移可开关。
- **改动文件**
  - `custom/config/template/config.yaml`：补充 v2 配置开关（建议）
    - `controlplane.migration.enable_v2`（总开关）
    - `controlplane.migration.dual_write`（存储双写开关）
    - `controlplane.migration.read_prefer_v2`（读优先级）
    - `controlplane.migration.nacos_dataid_suffix` / `redis_keyprefix_v2`（避免硬编码）
  - 可选：新增一个 CI/脚本（如在 Makefile/Go test 中）检测 `import ...controlplane_legacy/v1` 计数（你们不想加脚本也行，至少形成“验收标准”）。
- **签名变化**：无
- **回滚**：所有开关默认关闭，零风险。

---

## 1. PR-01：新增 `model` 与双向 converter（不改现有调用）
- **目标**：建立内部统一数据模型，为后续“消除 legacy/probe 在业务层扩散”做地基。
- **新增文件（实际落地路径）**
  - `custom/controlplane/model/*.go`
  - `custom/controlplane/conv/probeconv/*.go`
  - `custom/controlplane/conv/legacyconv/*.go`
- **核心类型（建议）**
  - `model.ConfigVersion{Version string, Etag string}`
  - `model.AgentConfig{Version ConfigVersion, Sampler *SamplerConfig, Batch *BatchConfig, DynamicResourceAttributes map[string]string, ExtensionConfigJSON string, SamplerRulesJSON string (迁移期可选)}`
  - `model.Task{ID, TypeName, ParametersJSON json.RawMessage, PriorityNum, TimeoutMillis, CreatedAtMillis, ExpiresAtMillis, MaxAcceptableDelayMillis}`
  - `model.TaskResult{TaskID, AgentID, Status model.TaskStatus, ErrorCode, ErrorMessage, ResultJSON json.RawMessage, ResultData []byte, StartedAtMillis, CompletedAtMillis, ExecutionTimeMillis, ResultTooLarge bool, Compression*, OriginalSize, CompressedSize, ResultDataType, RetryCount}`
- **要实现的 converter（最小闭环）**
  - probe：
    - `probeconv.AgentConfigFromProto / ToProto`
    - `probeconv.TaskFromProto / ToProto`
    - `probeconv.TaskResultFromTaskProto`（来自 `task.proto`）
    - `probeconv.TaskResultFromPollProto`（来自 `poll.proto` 的 `TaskResultRequest`）
  - legacy（对现有内部 JSON types）：
    - `legacyconv.AgentConfigFromLegacy / ToLegacy`
    - `legacyconv.TaskFromLegacy / ToLegacy`
    - `legacyconv.TaskResultFromLegacy / ToLegacy`
- **签名变化**：无（先只新增）
- **映射差异处理策略（明确写进 converter 注释）**
  - sampler rules：先保真（`rules_json` ↔ model 的 `SamplerRulesJSON`），probe 的 `rules[]` 暂不解析时明确置空，并记录 TODO。
  - etag：纳入 model 的 `ConfigVersion.Etag`，避免继续“旁路字段”。

---

## 2. PR-02：`controlplaneext` 增 `ControlPlaneV2`（model 签名），并在 `Extension` 内部做桥接
- **目标**：把 legacy 从 **核心接口签名** 上拔掉（但保留 legacy interface 兼容调用方）。
- **改动文件**
  - `custom/extension/controlplaneext/extension.go`
  - 以及所有实现 `ControlPlane` 的方法所在文件（同目录下多个）
- **新增接口（建议）**
  - `type ControlPlaneV2 interface { ... }`（参数/返回值都用 `model.*`）
- **`Extension` 做到**
  - 继续实现旧 `ControlPlane`（legacy 签名）：内部 `legacyconv -> model -> v2 实现`
  - 新增实现 `ControlPlaneV2`：直接走 model
- **签名变化**
  - 新增 `ControlPlaneV2`，旧的 `ControlPlane` 不动（降低一次性改动面）
- **验收**
  - 编译通过、行为不变、`agentgatewayreceiver` 暂时仍走旧接口也可以。

---

## 3. PR-03：`agentgatewayreceiver` 彻底改为 “probe ↔ model”，不再引用 legacy
- **目标**：让对探针的入口层不再 import `controlplane_legacy`。
- **改动文件**
  - `custom/receiver/agentgatewayreceiver/controlplane_convert.go`（删除 legacy 相关函数，替换为 `probeconv` + `model`）
  - `custom/receiver/agentgatewayreceiver/controlplane_service.go`
  - `custom/receiver/agentgatewayreceiver/controlplane_handler.go`（通常不用动）
- **签名变化（关键）**
  - `controlPlaneService` 内部调用从：
    - `controlplaneext.ControlPlane`（legacy）→ `controlplaneext.ControlPlaneV2`（model）
- **具体替换点**
  - `legacyConfigToProto(...)` → `probeconv.AgentConfigToProto(modelCfg)`
  - `legacyTasksToProto(...)` → `probeconv.TasksToProto(modelTasks)`
  - `taskResultRequestToLegacy(...)` / `taskResultToLegacy(...)` → `probeconv.TaskResultFromPollProto/FromTaskProto` 得到 model，再调用 `ReportTaskResultV2`
  - `UploadChunkedResult`：probe chunk → `model.ChunkUpload` → `UploadChunkV2`
- **字段映射重点**
  - `TaskResultRequest.status`（稳定集合）直接映射到 model 的 running/pending/terminal。
  - `task.proto.TaskResult.status`（包含 RESULT_TOO_LARGE）映射到 model 的 `ResultTooLarge/Compression/Size` 等；必要时把 `RESULT_TOO_LARGE` 归一化为 `FAILED` + `error_code=RESULT_TOO_LARGE`（但更推荐 model 原生支持）。

---

## 4. PR-04：`longpoll` 内部类型从 legacy 切到 model（消除 `controlplane_legacy` import）
- **目标**：`longpoll` 不再持有 `*legacyv1.AgentConfig` / `[]*legacyv1.Task`。
- **改动文件**
  - `custom/receiver/agentgatewayreceiver/longpoll/types.go`：`PollResponse.Config/Tasks` 改为 model
  - `custom/receiver/agentgatewayreceiver/longpoll/config_handler.go`（你当前打开的文件）：
    - 读取配置从 legacy→model（或直接从 `ConfigManagerV2` 返回 model）
    - `ConfigEtag/ConfigVersion` 的来源统一（不再“etag 旁路”）
  - `custom/receiver/agentgatewayreceiver/longpoll/task_handler.go`
  - `custom/receiver/agentgatewayreceiver/longpoll/helper.go`
- **签名变化**
  - `Manager.Poll(...) / PollSingle(...)` 的返回结构不必变（可以内部替换 `PollResponse` 字段类型），但会影响 `controlplane_service.go`（已在 PR-03 处理）。
- **兼容**
  - 先让 `longpoll` 仍可从旧的 `controlplaneext.ControlPlane` 获取数据（通过 `legacyconv` 转 model），再逐步切到 `ControlPlaneV2`（推荐直接切，减少中间态）。

---

## 5. PR-05：Chunk 上传 API 改 model（低风险，优先做）
- **目标**：让 `chunk_manager` 不再依赖 legacy 的 `UploadChunkRequest/Response`。
- **改动文件**
  - `custom/extension/controlplaneext/chunk_manager.go`
  - `custom/extension/controlplaneext/extension.go`（接口新增 `UploadChunkV2`）
  - `custom/receiver/agentgatewayreceiver/controlplane_service.go`（已在 PR-03 覆盖）
- **签名变化（建议）**
  - `ChunkManager.HandleChunk(ctx, req *model.ChunkUpload) (*model.ChunkUploadResponse, error)`
  - `ControlPlaneV2.UploadChunk(ctx, req *model.ChunkUpload) (*model.ChunkUploadResponse, error)`
- **映射重点**
  - probe 的 `ChunkedTaskResult` 字段几乎 1:1（`task_id/upload_id/chunk_index/total_chunks/chunk_data/chunk_checksum`）。

---

## 6. PR-06：Nacos ConfigManager 做 v2 dataId + 双读/可选双写（存储兼容核心）
- **目标**：让配置存储逐步从 legacy JSON（v1）迁到 model JSON（v2），并支持可回滚（双读/可选双写）。
- **改动文件（落地版）**
  - `custom/extension/controlplaneext/configmanager/interface.go`：新增 `ConfigManagerV2` / `OnDemandConfigManagerV2` + `MigrationConfig`
  - `custom/extension/controlplaneext/configmanager/nacos.go`：单 dataId Nacos manager 支持 v2 dataId + 双读/双写
  - `custom/extension/controlplaneext/configmanager/on_demand.go`：on-demand（token+agentID）支持 v2 dataId + 双读/双写
  - `custom/extension/controlplaneext/configmanager/factory.go`：把 migration 透传到 on-demand manager
  - `custom/extension/controlplaneext/component_factory.go`：创建 ConfigManager 统一走 `configmanager.NewConfigManager`（真正支持 `on_demand/multi_agent_nacos`）
  - `custom/extension/controlplaneext/extension.go`：`UpdateConfigV2/GetCurrentConfigV2` 优先调用 `ConfigManagerV2`（存在则直连 v2 存储）
  - `custom/config/template/config.yaml`：在 `config_manager.migration` 下补齐配置示例（默认注释）
- **配置路径（重要）**
  - `extensions.controlplane.config_manager.migration.*`
- **落库格式与兼容策略**
  - **dataId 规则**：`dataIdV2 = dataId + nacos_dataid_suffix`（默认 `.v2`，若已带后缀则不重复追加）
  - **读**：由 `read_prefer_v2` 控制优先级（先读 v2 或先读 v1），失败/不存在会 fallback 另一侧
  - **写**：
    - v2 API（`UpdateConfigV2/SetConfigForAgentV2`）：`enable_v2=true` 时**必写 v2**；`dual_write=true` 时额外写 v1
    - v1 API（legacy）：默认仍写 v1；`dual_write=true` 时额外写 v2（便于回滚/灰度）

---

## 7. PR-07：Redis TaskStore 引入 v2 keyprefix + 双读（任务存储兼容核心）
- **目标**：task 相关 Redis JSON 不再嵌 legacy pb struct，迁移到 model。
- **改动文件**
  - `custom/extension/controlplaneext/taskmanager/store/interface.go`：新增 `TaskStoreV2`（或把 `TaskInfo`/`TaskResult` 改用 model 并保留 v1 struct 另起名）
  - `custom/extension/controlplaneext/taskmanager/store/redis.go`
  - `custom/extension/controlplaneext/taskmanager/store/memory.go`
  - `custom/extension/controlplaneext/taskmanager/service.go`（调用侧切换到 v2 store）
  - `custom/extension/controlplaneext/taskmanager/helper.go`（`TaskHelper` 迁到 model）
- **签名变化（建议）**
  - `store.TaskInfo` 变为 `model.TaskInfo`（或新建 `store.TaskInfoV2`）
  - `ApplyTaskResult/ApplyCancel/...` 的入参 `*controlplanev1.TaskResult` → `*model.TaskResult`
- **Redis key 兼容（强烈建议）**
  - 新增 v2 前缀：例如 `otel:tasks:v2`
  - **读**：先读 v2 key；找不到再读 v1 key（legacy JSON）并转换为 model（可选 read-repair 写回 v2）
  - **写**：
    - 任务系统有队列/事件/状态机，一次性双写复杂；建议先做 **只写 v2 + 双读**，并在回滚预案里说明“回滚到旧版本时，v2 新任务可能不可见，需要接受丢失/清空任务队列”的运营策略；如果必须强回滚无损，再实现双写。
- **字段映射重点（`TaskResult`）**
  - 保证状态机语义不变（你们现在已经在 store 层实现了“终态 no-op”等逻辑），迁移时不要把 `RESULT_TOO_LARGE` 误当成普通失败而丢掉大小/压缩字段。

---

## 8. PR-08：Admin API v2（JSON）与旧端点兼容
- **目标**：对外提供 model JSON 的 `/api/v2`，同时保持 `/api/v1` 行为与入参完全兼容。
- **落地内容**
  - **路由**：`custom/extension/adminext/router.go` 新增 `/api/v2`，并沿用 v1 的 auth middleware 规则。
  - **配置接口（model JSON）**：`/api/v2/apps/{appID}/config` 下的 default/instance 配置读写。
    - 优先调用 `configmanager.OnDemandConfigManagerV2`（存在则直连 v2 存储）。
    - 否则 fallback 到 v1 `OnDemandConfigManager` 并通过 `legacyconv.AgentConfigFromLegacy/ToLegacy` 做适配。
  - **任务接口（model JSON）**：`/api/v2/tasks` 支持 list/create/get/cancel/batch。
    - 内部复用现有 legacy `taskMgr`，通过 `legacyconv.TaskFromLegacy/TaskToLegacy` 与 `legacyconv.TaskStatusFromLegacy` 做数据/状态适配。
- **验收**
  - 本地 `gofmt` + `go test ./...`（`custom/` 模块）通过。
- **签名变化**
  - handler 层新增 struct/DTO；不建议直接暴露 pb 作为 JSON（除非你们前端强绑定 pb 字段名）。
- **字段映射重点**
  - sampler rules：v2 API 可以直接支持结构化 rules（对齐 probe），同时保留 `rules_json` 兼容字段一段时间（避免历史 UI/脚本炸裂）。

---

## 9. PR-09：删除 `controlplane_legacy`（硬切：内部统一走 `model`）
- **目标**：删除 `custom/proto/controlplane_legacy/` 与所有 legacy 引用；内部控制面只保留 `model` + `probe proto`（边界用 `probeconv`）。
- **前置约束（硬切）**
  - 不保留 `/api/v1` admin（仅保留 `/api/v2`）。
  - 不继续读取 v1 存量数据：
    - Nacos：不再读旧 dataId（未迁移配置视为不存在）。
    - Redis：不再读 v1 keyspace（旧队列/任务详情/结果视为丢弃）。
- **实施步骤（建议按子 PR 推进，确保每步可编译）**
  1. **adminext v2-only**：移除 `/api/v1` 路由与 handler；WebUI 统一改走 `/api/v2`（含 ws-token 与 arthas ws）。
  2. **TaskManager model 化**：`taskmanager` 接口/`TaskInfo`/store/executor 全面切到 `model.*`；删除 Redis v1/v2 双读/双写与 namespace 分支，仅保留 v2 prefix + model JSON。
  3. **ConfigManager model 化**：`configmanager` 以 `model.AgentConfig` 为唯一类型；删除 v1/v2 双读/双写与 suffix 逻辑；由配置直接指定 v2 dataId。
  4. **ControlPlane 接口收敛**：移除 legacy `ControlPlane`，将 `ControlPlaneV2` 扶正为 `ControlPlane`（model）。
  5. **删除 legacy 目录**：删除 `custom/proto/controlplane_legacy/` 与 `custom/controlplane/conv/legacyconv/`，全仓 legacy import 归零。
- **验收 checklist**
  - grep：`controlplane_legacy/v1` import = 0。
  - `custom` 模块：`gofmt` + `go test ./...` 通过。
  - 关键链路：probe `UnifiedPoll/GetConfig/GetTasks/ReportStatus/ReportTaskResult/UploadChunkedResult` 全通；admin `/api/v2`（dashboard/apps/instances/services/tasks/arthas）全通。

---

### 你可以直接复制进 Jira 的“字段映射清单”（重点：`AgentConfig`、`TaskResult`）
#### **`AgentConfig` 映射（legacy ↔ model ↔ probe）**
- **version**
  - legacy `ConfigVersion` ↔ model `Version.Version`
  - model `Version.Etag` ↔ probe `ConfigVersion.etag`
  - 现有 longpoll 旁路 `ConfigEtag`：迁移后应由 model 持有并在存储层统一生成/读取
- **sampler**
  - type：legacy `Sampler.Type(int)` ↔ probe `SamplerConfig.type(enum)`（数值可直接转）
  - ratio：同名同义
  - rules：
    - legacy `RulesJSON string` → model `SamplerRulesJSON string`（保真）
    - probe `rules[]` → model `SamplerRules []SamplerRule`（建议新增字段）
    - **迁移期策略**：允许两者同时存在；写入时优先结构化，读取旧数据时填充 `SamplerRulesJSON`，对 probe 返回时若 `SamplerRules` 为空可选择“不返回 rules”或做一次 JSON 解析（取决于你们想不想在 server 做解析）
- **batch**：字段 1:1（注意 int32/int64）
- **dynamic_resource_attributes / extension_config_json**：1:1

#### **`TaskResult` 映射（两套 probe ↔ model ↔ legacy）**
- **status**
  - probe `poll.proto:TaskResultStatus`（含 RUNNING/PENDING）↔ model `TaskStatus`
  - probe `task.proto:TaskResult.Status`（含 RESULT_TOO_LARGE）↔ model：
    - `ResultTooLarge=true` + `Status=FAILED`（或新增 `TaskStatusResultTooLarge`，看你们是否想扩展稳定集合）
- **error_code/error_message**
  - poll.proto `error_code` → model `ErrorCode`
  - task.proto 没有 `error_code`：可在 `RESULT_TOO_LARGE` 时填 `ErrorCode="RESULT_TOO_LARGE"`
- **result_json/result_data**
  - probe `result_json`(string) → model `ResultJSON json.RawMessage`（解析失败则当作纯字符串包一层或直接丢到 `ErrorMessage`，建议明确策略）
  - probe `result_data`(bytes) → model `ResultData`
  - legacy `Result json.RawMessage` 与 `ResultData []byte` ↔ model 1:1
- **时间戳**
  - 统一 millis（你们现有内部就是 millis）；任何 `unix_nano` 输入都在 adapter 层转换成 millis，禁止混入 store
- **压缩/大小/类型**
  - task.proto 的 `compression/original_size/compressed_size/result_data_type/retry_count` → model 保留字段，避免丢信息；legacy 目前没这些字段，存储时 v2 JSON 会比 v1 丰富

---

### 最后一句建议（拆 PR 的顺序）
如果你们希望每个 PR 都“可单独上线且易回滚”，推荐顺序是：**PR-00 → PR-01 → PR-02 → PR-05 → PR-03 → PR-04 → PR-06 → PR-07 → PR-08 → PR-09**（先把 model+双接口打好，再逐步迁移入口与存储）。