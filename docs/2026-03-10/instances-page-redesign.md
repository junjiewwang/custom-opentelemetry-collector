# 实例管理页面（Instances）重设计与优化

## 需求背景

Admin WebUI 的实例管理页面原先使用简单的平铺表格布局，缺乏层级导航和丰富的交互体验。
参考 Tasks 页面的设计语言（左右两栏布局 + 树形导航 + 卡片列表），对 Instances 页面进行全面升级。

## 设计目标

1. **左右两栏布局**：左侧 App→Service 两级树形导航，右侧实例卡片列表
2. **可点击统计卡片**：全部 / 在线 / 离线 / Arthas就绪 / Arthas未注册，作为快捷过滤器
3. **树-列表联动**：点击左侧 Service 节点自动筛选右侧实例列表
4. **丰富的详情抽屉**：点击实例卡片弹出侧边抽屉，展示基础信息、Arthas状态、元数据、生命周期等
5. **保持已有功能**：Arthas Task 状态面板、搜索、刷新、下线实例等功能不丢失

## 实施状态：✅ 已完成

## 改动文件

### 1. `custom/extension/adminext/webui/views/instances.html`（完全重写）

**改动内容**：
- 从平铺表格改为两栏布局（左侧 `w-72` 树导航 + 右侧 `flex-1` 卡片列表）
- 新增 5 个可点击统计卡片，支持状态过滤
- 左侧树导航包含"全部实例"根节点 + App→Service 层级
- 右侧实例卡片展示：Agent ID、版本号、在线/离线 badge、主机名/IP、App/Service、Arthas 状态、心跳时间、操作按钮
- 选择面包屑：显示当前选中的 AppName / ServiceName
- 空状态处理
- 详情抽屉：基础信息、Arthas状态、元数据(Attributes)、生命周期 4 个区块
- 抽屉底部操作：复制 Agent ID、下线实例

### 2. `custom/extension/adminext/webui/js/views/instances.js`（完全重写）

**改动内容**：从 ~95 行扩展至 ~290 行

新增状态：
- `instanceTreeData`: 树形数据
- `selectedTreeNodeId`: 当前选中的 Service 节点 ID

新增方法：
- `buildInstanceTree(instances)` — 构建 App→Service 两级树，含在线/离线/Arthas计数
- `_applyInstanceFilters(instances)` — 共享过滤逻辑（状态 + 搜索）
- `filteredInstances()` — 综合过滤（状态 + 搜索 + 树节点选择）
- `applyInstanceFilter()` — 触发树重建
- `getInstanceStats()` — 计算 5 种统计数据
- `toggleInstanceTreeNode(node)` — 展开/折叠 App 节点
- `selectTreeServiceNode(svcNode)` — 选中/取消 Service 节点
- `getSelectedTreeNodeLabel()` — 返回 "AppName / ServiceName" 面包屑标签

保留方法：
- `showInstanceDetails()`, `unRegisterAgent()`, 时间格式化辅助函数

### 3. `custom/extension/adminext/webui/js/app.js`（小幅修改）

**改动内容**：
- 在 `loadInstances()` 方法末尾（第 444 行）添加 `this.instanceTreeData = this.buildInstanceTree(this.instances);`
- 确保每次加载实例数据后自动构建树形数据

## 验证结果

### API 验证
- 8 个实例数据正常返回（1 在线、7 离线）

### UI 验证（Playwright）
- 5 个统计卡片正确渲染：全部:8, 在线:1, 离线:7, Arthas就绪:0, Arthas未注册:8
- 左侧树正确展示 App "CIKmanlvG3sfdnpr" → Service "java-user-service" (8 instances)
- 右侧 8 个实例卡片正确展示，包含状态 badge、主机名/IP、Arthas 状态、心跳时间

## 技术要点

1. **Go embed**：前端文件通过 `//go:embed webui/*` 嵌入 Go 二进制，修改前端文件后需要重新编译
2. **Alpine.js Mixin**：`instancesView()` 通过 `...instancesView()` 展开到 `adminApp()` 主数据对象中
3. **树构建模式**：复用 Tasks 页面的 `buildTaskTree()` 设计模式，用 Map 构建层级后转为数组排序
4. **x-collapse**：树节点展开/折叠使用 Alpine.js `x-collapse` 插件实现平滑动画

## 后端排序支持：✅ 已完成

### 需求背景

实例列表 API 返回顺序不确定（Memory 实现 map 遍历随机，Redis `HGetAll` 无序），前端需要稳定的排序以提升用户体验。

### API 设计

新增 Query 参数：

| 参数名 | 类型 | 必须 | 默认值 | 说明 |
|---|---|---|---|---|
| `sort_by` | string | 否 | `status_heartbeat` | 排序字段 |
| `sort_order` | string | 否 | `desc` | 排序方向：`asc` / `desc` |

`sort_by` 可选值（白名单）：

| 值 | 说明 |
|---|---|
| `status_heartbeat` | 在线状态权重 → 最后心跳（默认，在线优先） |
| `last_heartbeat` | 按心跳时间排序 |
| `registered_at` | 按注册时间排序 |
| `start_time` | 按启动时间排序 |

请求示例：
```
GET /api/v2/instances?status=all&sort_by=status_heartbeat&sort_order=desc
GET /api/v2/instances?status=all&sort_by=last_heartbeat&sort_order=asc
```

### 改动文件

| 文件 | 改动 | 类型 |
|---|---|---|
| `agentregistry/sort.go` | 排序模块：类型定义、白名单验证、排序函数 | 新增 |
| `agentregistry/sort_test.go` | 表驱动单元测试（13 cases） | 新增 |
| `adminext/handlers.go` | `listAllInstances` 添加 `sort_by`/`sort_order` 参数解析和排序调用 | 修改 |

### 架构决策

- **排序在 Handler 层完成**，不侵入 Registry 接口 — 保持 Registry 职责单一（存取数据）
- **排序逻辑抽象到独立的 `sort.go`** — 可测试、可复用
- **白名单验证排序参数** — 安全，防止无效输入
- **`sort.SliceStable` + AgentID tie-breaker** — 保证完全确定性排序
- **向后兼容** — 不传排序参数时使用默认排序，已有 API 调用者无感知

## 遗留问题

- 无
