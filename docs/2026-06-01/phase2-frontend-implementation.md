# Phase 2 实施进展记录

> 日期: 2026-06-01
> 需求文档: `docs/2026-05-29/unified-observability-storage-design.md`
> 状态: ✅ Phase 2 全部完成（待集成测试环境验证）

## 已完成任务

### Task 2.8: 前端 WebUI — Log 查询页面

**实施内容:**

1. **类型定义** (`types/log.ts`)
   - `LogRecord`: 单条日志记录（id, timestamp, severity, body, service_name, attributes, resource, trace_id, span_id）
   - `LogSearchResult`: 搜索结果 (logs + total)
   - `LogContext`: 前后文日志行
   - `LogField`: 可用过滤字段
   - `LogStats`: 统计结果（severity 分布、时间直方图）
   - `LogSearchParams` / `LogStatsParams`: 查询参数
   - 导出 `SEVERITY_COLORS` 和 `SEVERITY_WEIGHTS` 常量

2. **API 客户端** (`api/client.ts`)
   - `searchLogs(params)`: 搜索日志
   - `getLogContext(logID, lines)`: 获取日志上下文
   - `getLogFields(start?, end?)`: 获取可用字段
   - `getLogStats(params)`: 获取日志统计

3. **页面组件** (`pages/LogsPage.tsx`)
   - 搜索面板: 全文搜索、Service 下拉、Severity 多选标签、Trace ID、时间范围
   - 结果列表: Severity 徽标、时间戳、Service、Body 单行展示
   - 展开详情: 元数据、日志全文、Attributes 标签、Resource 标签、Trace ID 跳转链接
   - 统计栏: Severity 分布计数
   - 分页: 上/下一页
   - 不可用提示: 当 storageExtension 未配置时展示配置说明

### Task 2.9: 前端 WebUI — Storage Status 面板

**实施内容:**

1. **类型定义** (`types/storage.ts`)
   - `StorageStatus`: 存储状态（provider, healthy, version, indices, details）
   - `IndexInfo`: 索引信息
   - `StorageHealth`: 健康检查结果
   - `RetentionPolicies` / `RetentionPolicy`: 保留策略
   - `DiskUsage`: 磁盘使用量
   - `PurgeResult`: 清除操作结果
   - 辅助函数: `formatBytes()`, `formatRetention()`

2. **API 客户端** (`api/client.ts`)
   - `getStorageStatus()`: 获取存储状态
   - `getStorageHealth()`: 健康检查
   - `getStorageRetention()`: 获取保留策略
   - `setStorageRetention(signal, duration)`: 设置保留策略
   - `purgeStorage(signal, before)`: 清除数据
   - `getStorageDiskUsage()`: 磁盘使用量

3. **页面组件** (`pages/StorageAdminPage.tsx`)
   - 健康状态卡片: 绿/红指示灯 + 延迟
   - 存储后端卡片: Provider + 版本号
   - 磁盘使用卡片: 用量进度条 + 可用容量
   - 按信号类型分布: Trace / Metric / Log 各自占用
   - 保留策略管理: 查看/修改保留时间 + 清除操作
   - 索引列表表格: 名称、信号类型、文档数、大小

### Task 2.5 (前端部分): 路由与导航集成

**实施内容:**

1. **路由** (`App.tsx`)
   - 添加 `/logs` → `LogsPage` 懒加载路由
   - 添加 `/storage` → `StorageAdminPage` 懒加载路由

2. **侧边栏** (`layouts/Sidebar.tsx`)
   - Observability 分组新增: Logs (fa-file-alt) 和 Storage (fa-database)

## 后端已完成任务 (上一次实施)

- **Task 2.4**: adminext 通过 `host.GetExtensions()` 发现 observabilitystorageext，获取 Reader 实例
- **Task 2.5** (后端): Log query API `/api/v2/observability/logs/*` 注册
- **Task 2.6**: ES StorageAdmin 适配器 (GetStatus, GetRetention, GetDiskUsage 已实现；SetRetention/Purge 为 stub)
- **Task 2.7**: Storage Admin API `/api/v2/observability/admin/*` 注册
- V2 Trace/Metric handlers 使用结构化响应
- 双模式路由 (V2 structured + legacy proxy)

## 后端补充实现 (2026-06-01 第二次实施)

### Task 2.6 补充: SetRetention / Purge / PurgeByApp 实现

**SetRetention** (`admin.go`):
- 校验 retention > 0
- 调用 `createILMPolicy` 更新 ILM 策略的 delete phase `min_age`

**Purge** (`admin.go`):
- 构建 `range` 查询 (timestamp < before)
- 调用 `client.DeleteByQuery` 批量删除过期数据
- 返回实际删除的文档数

**PurgeByApp** (`admin.go`):
- 构建 `bool.must` 查询: timestamp range + app_id term
- 调用 `client.DeleteByQuery` 按应用选择性删除
- 返回实际删除的文档数

**Adapter 层** (`reader_adapter.go`):
- `indexPrefixForSignal(signal)`: 信号类型 → ES 索引前缀映射
- `timestampFieldForSignal(signal)`: 信号类型 → 时间戳字段映射
  - trace → `start_time`, metric → `@timestamp`, log → `timestamp`
- `SetRetention`: 解析 duration + 获取 indexPrefix → 委托 ES Admin
- `Purge`: 获取 indexPrefix + timestampField → 委托 ES Admin
- `PurgeByApp`: 获取 indexPrefix + timestampField → 委托 ES Admin

### Task 2.10: Integration Testing

新增 5 个集成测试函数（`integration_test.go`）:

| 测试函数 | 验证内容 |
|---------|---------|
| `TestIntegration_Admin_SetRetention` | 更新 ILM 策略的 delete phase; 负数时长被拒绝 |
| `TestIntegration_Admin_Purge` | 写入 trace → purge → 验证 count=0 |
| `TestIntegration_Admin_PurgeByApp` | 写入 2 个 app_id 的日志 → purge 其一 → 另一个仍存在 |
| `TestIntegration_Admin_GetStatus` | 获取集群健康状态 |
| `TestIntegration_Admin_GetDiskUsage` | 获取索引统计数据 |

测试门控: `ES_INTEGRATION_TEST=true` 环境变量

## 遗留问题 / 待后续 Phase 处理

| # | 内容 | 目标阶段 | 优先级 |
|---|------|---------|--------|
| ~~1~~ | ~~`StorageAdmin.SetRetention()` 和 `Purge()`/`PurgeByApp()` 后端实现~~ | ~~Phase 2~~ | ~~✅ 已完成~~ |
| 2 | 前端 Trace/Metric 页面 V2 结构化模式适配器（Jaeger/Prometheus 格式转换）| Phase 4 | P2 |
| 3 | 日志时间直方图可视化（需 ECharts）| Phase 3+ | P3 |
| ~~4~~ | ~~Integration Testing（Task 2.10）~~ | ~~Phase 2~~ | ~~✅ 已完成~~ |
| 5 | 前端 Log 页面获取 services 列表目前复用 trace services 接口，若无 trace 数据可能为空 | Phase 3+ | P3 |

## 文件变更清单

### 新增文件
- `extension/adminext/webui-react/src/types/log.ts`
- `extension/adminext/webui-react/src/types/storage.ts`
- `extension/adminext/webui-react/src/pages/LogsPage.tsx`
- `extension/adminext/webui-react/src/pages/StorageAdminPage.tsx`

### 修改文件
- `extension/adminext/webui-react/src/api/client.ts` — 添加 Log/Storage API 方法
- `extension/adminext/webui-react/src/App.tsx` — 添加 Logs/Storage 路由
- `extension/adminext/webui-react/src/layouts/Sidebar.tsx` — 添加 Logs/Storage 菜单项
- `extension/observabilitystorageext/provider/elasticsearch/admin.go` — 实现 SetRetention/Purge/PurgeByApp
- `extension/observabilitystorageext/reader_adapter.go` — 替换 stub 为真实实现，添加 helper 方法
- `extension/observabilitystorageext/provider/elasticsearch/integration_test.go` — 新增 5 个 Admin 集成测试
