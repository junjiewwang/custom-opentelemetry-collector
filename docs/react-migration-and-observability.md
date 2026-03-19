# 🚀 React 渐进式迁移 + Trace/Metric 可观测性功能

> 需求文档 & 实施进展记录

## 📋 背景

将前端从 Alpine.js 渐进式迁移到 React + TypeScript，同时新增 Trace/Metric 数据查询可视化功能。

采用**方案 C（渐进式迁移）**：
1. 先搭建 React + Vite + TypeScript 骨架
2. 新功能（Trace/Metric 页面）直接用 React 开发
3. 旧页面低优先级逐步迁移

## 🏗️ 架构设计

### 最终架构（单前端 React）

```
extension/adminext/
├── webui-react/              # React 前端（唯一前端，挂载在 /ui/）
│   ├── src/
│   │   ├── App.tsx
│   │   ├── main.tsx
│   │   ├── api/              # API 层
│   │   ├── components/       # 通用组件（含 Terminal/）
│   │   ├── contexts/         # React Context（Auth / Toast）
│   │   ├── layouts/          # 布局组件
│   │   ├── pages/            # 页面组件（9 个原生 React 页面）
│   │   ├── types/            # TypeScript 类型定义
│   │   └── utils/            # 工具函数
│   ├── package.json
│   ├── vite.config.ts
│   ├── tsconfig.json
│   └── tailwind.config.ts
│
├── webui.go                  # Go embed（仅嵌入 React 前端）
└── router.go                 # 路由（/ui/ → React, /legacy/* → 301 → /ui/）
```

### Go 侧 embed

```go
//go:embed webui-react/dist/*
var reactUIFS embed.FS     // React 构建产物（唯一前端）

// 路由:
// /ui/*     → React SPA
// /         → 重定向到 /ui/
// /legacy/* → 重定向到 /ui/（向后兼容）
```

## 📊 实施阶段

### Phase 0: React 骨架搭建（✅ 已完成）

| 步骤 | 内容 | 状态 |
|------|------|------|
| 0.1 | 初始化 Vite + React + TypeScript 项目 | ✅ 完成 |
| 0.2 | 配置 Tailwind CSS + 主题色系 | ✅ 完成 |
| 0.3 | 搭建骨架（路由 + 布局 + Auth Context + API 层） | ✅ 完成 |
| 0.4 | 实现 Sidebar + 导航 + Legacy iframe 嵌入 | ✅ 完成 |
| 0.5 | Go 侧 webui.go 改造支持 React embed | ✅ 完成 |
| 0.6 | 构建验证 + 编译检查 | ✅ 完成 |

**构建结果**：
- React 前端：`npm run build` ✅（产物 ~78KB gzip）
- Go 后端：`go build` ✅（双前端 embed 成功）
- 路由结构：`/ui/` → React SPA，`/legacy/` → Alpine.js，`/` → 301 → `/ui/`

### Phase 1: Trace 查询（✅ 已完成）

**后端（Go）：**

| 步骤 | 内容 | 状态 |
|------|------|------|
| 1.1 | `config.go` 添加 `ObservabilityConfig`（Jaeger/Prometheus 端点） | ✅ 完成 |
| 1.2 | 创建 `observability_handler.go`（Trace Query Proxy + Metric Query Proxy） | ✅ 完成 |
| 1.3 | `router.go` 注册 `/api/v2/observability/*` 路由 | ✅ 完成 |
| 1.4 | `extension.go` 初始化 `obsClient` 查询客户端 | ✅ 完成 |

**前端（React）：**

| 步骤 | 内容 | 状态 |
|------|------|------|
| 1.5 | 创建 `types/trace.ts` Jaeger 响应类型定义 | ✅ 完成 |
| 1.6 | `api/client.ts` 添加 Trace/Metric 查询 API 方法 | ✅ 完成 |
| 1.7 | 创建 `utils/trace.ts` 数据转换工具函数 | ✅ 完成 |
| 1.8 | 实现 `TracesPage.tsx` 完整搜索面板+结果列表 | ✅ 完成 |
| 1.9 | 实现 `TraceDetail.tsx` Span 时间轴可视化组件 | ✅ 完成 |

**新增 API 路由：**

| 端点 | 代理目标 | 功能 |
|------|---------|------|
| `GET /api/v2/observability/traces` | Jaeger `/api/traces` | 搜索 Traces |
| `GET /api/v2/observability/traces/{traceID}` | Jaeger `/api/traces/{id}` | 获取单个 Trace 详情 |
| `GET /api/v2/observability/traces/services` | Jaeger `/api/services` | Service 列表 |
| `GET /api/v2/observability/traces/services/{svc}/operations` | Jaeger `/api/services/{svc}/operations` | Operation 列表 |
| `GET /api/v2/observability/metrics/query` | Prometheus `/api/v1/query` | Instant query |
| `GET /api/v2/observability/metrics/query_range` | Prometheus `/api/v1/query_range` | Range query |
| `GET /api/v2/observability/metrics/labels` | Prometheus `/api/v1/labels` | Label 名称列表 |
| `GET /api/v2/observability/metrics/labels/{name}/values` | Prometheus `/api/v1/label/{name}/values` | Label 值列表 |
| `GET /api/v2/observability/metrics/series` | Prometheus `/api/v1/series` | Series 元数据 |
| `GET /api/v2/observability/metrics/metadata` | Prometheus `/api/v1/metadata` | Metric 元数据 |

**配置方式（config.yaml）：**

```yaml
admin:
  observability:
    jaeger:
      endpoint: "http://jaeger-query:16686"
    prometheus:
      endpoint: "http://prometheus:9090"
```

**构建结果**：
- React 前端：`npm run build` ✅（产物 ~82KB gzip，含 Trace 页面）
- Go 后端：`go build` ✅

### Phase 2: Metric 查询（✅ 已完成）

**前端（React）：**

| 步骤 | 内容 | 状态 |
|------|------|------|
| 2.1 | 安装 ECharts 依赖 (`echarts` + `echarts-for-react`) | ✅ 完成 |
| 2.2 | 创建 `types/metric.ts` Prometheus HTTP API 响应类型定义 | ✅ 完成 |
| 2.3 | 更新 `api/client.ts` Metric 方法返回类型（替换 unknown 为类型安全） | ✅ 完成 |
| 2.4 | 创建 `utils/metric.ts` 数据转换 + 预设面板 + 时间范围计算 | ✅ 完成 |
| 2.5 | 创建 `components/TimeSeriesChart.tsx` ECharts 时间序列组件（Tree-shaking） | ✅ 完成 |
| 2.6 | 实现 `MetricsPage.tsx`（PromQL 查询面板 + RED Dashboard 面板） | ✅ 完成 |
| 2.7 | Vite 代码分割优化（React / ECharts 独立打包） | ✅ 完成 |

**MetricsPage 功能：**

- **PromQL Query Tab**：自由输入 PromQL 表达式 + 示例查询快捷填充 + ECharts 折线图展示
- **RED Dashboard Tab**：选择 Service → 自动并行加载 6 个预设面板
  - Request Rate (QPS)
  - Error Rate (%)
  - Latency P50 / P95 / P99
  - Requests by Status Code
- **共享时间范围选择器**：15m / 30m / 1h / 3h / 6h / 12h / 24h / 2d / 7d
- **自动 step 计算**：根据时间范围自动选择合适的查询步长

**新增依赖：**

| 包名 | 版本 | 说明 |
|------|------|------|
| `echarts` | ^5.x | Apache ECharts 核心（Tree-shaking 按需导入） |
| `echarts-for-react` | ^3.x | React ECharts 包装组件 |

**构建产物（代码分割后）：**

| 文件 | 大小 | gzip |
|------|------|------|
| `vendor-react-*.js` | 49 KB | 17 KB |
| `index-*.js` | 234 KB | 72 KB |
| `vendor-echarts-*.js` | 568 KB | 191 KB |
| `index-*.css` | 18 KB | 4 KB |
| **合计** | **869 KB** | **284 KB** |

### Phase 3: 联动 & 增强（✅ 已完成）

**Trace ↔ Metric 双向联动：**

| 步骤 | 内容 | 状态 |
|------|------|------|
| 3.1 | `TraceDetail.tsx` 添加 "View Metrics" / "More Traces" 联动按钮 | ✅ 完成 |
| 3.2 | `TracesPage.tsx` 支持 URL 查询参数（`?service=xxx&lookback=1h`），从 Metric 页面跳转时自动填充搜索条件并触发搜索 | ✅ 完成 |
| 3.3 | `MetricsPage.tsx` RED Dashboard 添加 "View Traces" 联动按钮 + 支持 URL 参数（`?service=xxx&tab=red`） | ✅ 完成 |

**Service Map 服务拓扑图：**

| 步骤 | 内容 | 状态 |
|------|------|------|
| 3.4 | 后端 `observability_handler.go` 新增 `handleGetDependencies` 代理 Jaeger Dependencies API | ✅ 完成 |
| 3.5 | 后端 `router.go` 注册 `GET /api/v2/observability/dependencies` 路由 | ✅ 完成 |
| 3.6 | 前端 `types/trace.ts` 新增 `JaegerDependencyLink` 类型 | ✅ 完成 |
| 3.7 | 前端 `api/client.ts` 新增 `getDependencies()` 方法 | ✅ 完成 |
| 3.8 | 创建 `pages/ServiceMapPage.tsx` — ECharts Graph 力导向拓扑图 | ✅ 完成 |
| 3.9 | `App.tsx` 添加 `/service-map` 路由 | ✅ 完成 |
| 3.10 | `Sidebar.tsx` 添加 Service Map 导航菜单项 | ✅ 完成 |

**联动跳转路径：**

```mermaid
graph LR
    subgraph "Trace → Metric"
        TD["TraceDetail<br/>View Metrics 按钮"] -->|"/metrics?service=svc&tab=red"| MP["MetricsPage<br/>RED Dashboard"]
    end
    
    subgraph "Metric → Trace"  
        MP2["MetricsPage<br/>View Traces 按钮"] -->|"/traces?service=svc&lookback=1h"| TP["TracesPage<br/>自动搜索"]
    end
    
    subgraph "Service Map → Trace"
        SM["ServiceMapPage<br/>点击节点"] -->|"/traces?service=svc&lookback=1h"| TP2["TracesPage<br/>自动搜索"]
    end
```

**ServiceMapPage 功能：**

- ECharts Graph 力导向布局拓扑图
- 节点大小按调用量对数缩放
- 边粗细按调用次数缩放 + 箭头方向
- 支持拖拽、缩放、平移
- 点击节点跳转到 Traces 页面
- Tooltip 显示调用统计
- 时间范围选择器（1h / 6h / 12h / 24h / 2d / 7d）

**新增 API 路由：**

| 端点 | 代理目标 | 功能 |
|------|---------|------|
| `GET /api/v2/observability/dependencies` | Jaeger `/api/dependencies` | 服务依赖关系 |

**构建产物（Phase 3 后）：**

| 文件 | 大小 | gzip |
|------|------|------|
| `vendor-react-*.js` | 50 KB | 18 KB |
| `index-*.js` | 242 KB | 73 KB |
| `vendor-echarts-*.js` | 609 KB | 205 KB |
| `index-*.css` | 18 KB | 4 KB |
| **合计** | **919 KB** | **300 KB** |

## ⚙️ 技术栈

| 组件 | 选型 |
|------|------|
| 构建工具 | Vite 6.x |
| 框架 | React 19 |
| 语言 | TypeScript 5.x |
| CSS | Tailwind CSS 4.x |
| 路由 | React Router 7 |
| 图表 | Apache ECharts (via echarts-for-react) |
| HTTP | 原生 fetch (封装 API 层) |
| 状态管理 | React Context + useState (轻量级) |

## 🧪 联调测试记录（2026-03-17）

**环境：** 本地开发（Vite dev server + Go 后端 + 远程 Prometheus/Jaeger）

| 功能模块 | 测试结果 | 备注 |
|---------|---------|------|
| 前端 Vite 启动 | ✅ 正常 | `http://localhost:5174/ui/` |
| Go 后端启动 | ✅ 正常 | `:8088`，Observability proxy 初始化成功 |
| 登录鉴权 | ✅ 正常 | API Key 登录 + Remember 功能正常 |
| 导航菜单 | ✅ 正常 | Dashboard / Applications / Instances / Services / Tasks / Configs / **Traces** / **Metrics** / **Service Map** 全部显示 |
| **Metrics - PromQL Query** | ✅ 正常 | 查询 `up` 返回 15 个 series，ECharts 图表渲染正常 |
| **Metrics - RED Dashboard** | ✅ 正常 | Service 列表正确获取（customcol / java-user-service / tapm-api），6 个 RED 面板渲染正常 |
| **Metrics → Traces 联动** | ✅ 正常 | RED Dashboard "View Traces" 按钮存在，点击可跳转到 Traces 页面 |
| **Traces - 搜索面板** | ✅ 正常 | Service/Operation/Lookback/Limit/Tags/Duration 全部渲染 |
| **Traces - Service 列表** | ✅ 正常 | 返回 3 个服务：jaeger-all-in-one / java-user-service / tapm-api |
| **Traces - Operation 列表** | ✅ 正常 | 选择 service 后自动加载 operations（25+ 个 operation） |
| **Traces - 搜索查询** | ✅ 正常 | 搜索 java-user-service 返回 5 条 traces，列表展示正常 |
| **Traces - Trace 详情** | ✅ 正常 | 时间轴瀑布图、Span 信息（ID/时间/时长/服务）展示正常 |
| **Traces - Span 详情** | ✅ 正常 | Tags(16) / Process Tags(17) / Logs(31) 全部展示，Error 标记正常 |
| **Traces - View Metrics** | ✅ 正常 | Trace 详情中 "View Metrics" 联动按钮正常 |
| **Service Map - 页面** | ✅ 正常 | UI 加载正常，Time Range 选择器正常 |
| **Service Map - 依赖数据** | ⚠️ 数据为空 | API 代理正常（返回 200），但 Jaeger 中无依赖数据（可能需要 spark-dependencies job） |
| **Prometheus 连接** | ✅ 可达 | `prometheus.istio-system.svc.cluster.local:9090` 正常响应 |
| **Jaeger 连接** | ✅ 可达 | `jaeger.devcloud`（非 `jeager.http.devcloud`）正常响应 |

**结论：**
- Prometheus 相关功能（Metrics 页面）**全部正常** ✅
- Jaeger 相关功能（Traces 页面）**全部正常** ✅ （Jaeger Query 地址为 `http://jaeger.devcloud`）
- Service Map **API 代理正常**，但 Jaeger 中无依赖数据 ⚠️
- 前端 UI、路由、联动、降级提示均正常 ✅

**配置修正记录：**
- Jaeger Query 的正确域名为 `jaeger.devcloud`（不是之前误用的 `jeager.http.devcloud`）
- `jeager.http.devcloud` 是 OTLP Collector 写入端点（端口 55681），不提供 Query API
- 已更新 `config/build/config.yaml` 中的 endpoint

---

## 🔄 旧版 WebUI (Alpine.js) → React 完整迁移计划

> 目标：完全废弃旧版 Alpine.js 前端（webui/），将所有页面功能迁移到 React（webui-react/），最终移除 /legacy/ 路由和 iframe 嵌入机制。

### 旧版页面功能盘点

| 页面 | 复杂度 | 代码量 | 核心功能 | 特殊依赖 |
|------|--------|--------|----------|----------|
| **Dashboard** | ⭐ 低 | ~73行 HTML | 4个统计卡片 + Quick Actions | 无 |
| **Applications** | ⭐⭐ 中 | ~72行 HTML + app.js | 表格 CRUD + Token 管理（创建/删除/Token生成/自定义设置） | 模态框×2 |
| **Services** | ⭐ 低 | ~29行 HTML | 服务卡片列表 + 跳转实例页 | 无 |
| **Instances** | ⭐⭐⭐⭐ 高 | ~419行 HTML + 295行 instances.js | 左侧树 + 右侧卡片列表 + 抽屉详情 + Arthas 终端 | xterm.js WebSocket 终端 |
| **Tasks** | ⭐⭐⭐⭐⭐ 最高 | ~909行 HTML + 880行 tasks.js | 三级树 + 任务列表 + 抽屉详情 + 创建表单(SearchableSelect) + 动态表单 | 自定义 SearchableSelect |
| **Configs** | ⭐⭐⭐ 中高 | ~185行 HTML + app.js | 左侧服务树 + JSON 编辑器 + 模板推荐 + 缺失字段补全 | JSON 编辑/校验 |

### React 已有基础设施（可复用）

| 模块 | 文件 | 说明 |
|------|------|------|
| API 客户端 | `api/client.ts` | 已有全部 API 方法（Apps/Instances/Services/Tasks/Config/Arthas/Auth） |
| 类型定义 | `types/api.ts` | 已有 App/Instance/Service/Task/Config/ArthasAgent 类型 |
| 认证 | `contexts/AuthContext.tsx` | 登录/登出/API Key 持久化 |
| Toast | `contexts/ToastContext.tsx` | 全局通知 |
| 路由 | `App.tsx` | 路由框架已搭好 |
| 侧边栏 | `layouts/Sidebar.tsx` | 导航菜单已有所有菜单项 |

### 迁移阶段总览

```mermaid
gantt
    title 旧版 WebUI → React 迁移甘特图
    dateFormat YYYY-MM-DD
    
    section Phase 4 - 简单页面
    Dashboard 页面           :done, p4a, 2026-03-17, 1d
    Services 页面            :done, p4b, 2026-03-17, 1d
    Applications 页面        :done, p4c, 2026-03-17, 1d
    
    section Phase 5 - 通用组件
    SearchableSelect 组件    :done, p5a, 2026-03-19, 1d
    ConfirmDialog 组件       :done, p5b, 2026-03-19, 0.5d
    DetailDrawer 组件        :done, p5c, 2026-03-19, 0.5d
    TreeNav 组件             :done, p5d, 2026-03-19, 0.5d
    
    section Phase 6 - 配置编辑器
    ConfigsPage 迁移         :done, p6a, 2026-03-19, 1d
    
    section Phase 7 - 实例管理
    InstancesPage 迁移       :done, p7a, 2026-03-19, 1d
    
    section Phase 8 - 任务管理
    TasksPage 迁移           :done, p8a, 2026-03-19, 1d
    
    section Phase 9 - 终端 + 清理
    xterm.js React 封装      :done, p9a, 2026-03-19, 1d
    Arthas 终端集成          :done, p9b, 2026-03-19, 0.5d
    移除 Legacy 代码          :done, p9c, 2026-03-19, 0.5d
```

### 迁移后架构

```mermaid
graph TB
    subgraph "迁移完成后的架构"
        Browser[浏览器] --> Router[Go 后端路由]
        Router -->|/ui/*| React[React 前端<br/>唯一前端入口]
        Router -->|/api/v2/*| API[REST API]
        
        subgraph "React 页面（全部原生）"
            R1[DashboardPage ✅]
            R2[AppsPage ✅]
            R3[ServicesPage ✅]
            R4[InstancesPage ✅]
            R5[TasksPage ✅]
            R6[ConfigsPage ✅]
            R7[TracesPage ✅]
            R8[MetricsPage ✅]
            R9[ServiceMapPage ✅]
        end
        
        subgraph "可复用组件"
            C1[SearchableSelect ✅]
            C2[ConfirmDialog ✅]
            C3[DetailDrawer ✅]
            C4[TreeNav ✅]
            C5[TerminalPanel ✅]
            C6[useTerminal Hook ✅]
            C7[JsonEditor 🔄 可选]
        end
    end
    
    style R1 fill:#22c55e,color:#fff
    style R2 fill:#22c55e,color:#fff
    style R3 fill:#22c55e,color:#fff
    style R4 fill:#22c55e,color:#fff
    style R5 fill:#22c55e,color:#fff
    style R6 fill:#22c55e,color:#fff
    style R7 fill:#22c55e,color:#fff
    style R8 fill:#22c55e,color:#fff
    style R9 fill:#22c55e,color:#fff
    style C5 fill:#22c55e,color:#fff
    style C6 fill:#22c55e,color:#fff
```

---

### Phase 4: 简单页面迁移（✅ 已完成 — 2026-03-17）

将 Dashboard、Services、Applications 从 `<LegacyPage>` iframe 嵌入改为 React 原生实现。

| 步骤 | 内容 | 状态 | 产物 |
|------|------|------|------|
| 4.1 | **DashboardPage** — 4个统计卡片 + Quick Actions + 30s 自动刷新 | ✅ 完成 | `DashboardPage.tsx` (6.20KB) |
| 4.2 | **ServicesPage** — 服务卡片网格 + 点击跳转 Instances | ✅ 完成 | `ServicesPage.tsx` (3.95KB) |
| 4.3 | **AppsPage** — 表格 CRUD + Token 管理（Create Modal + Token Modal） | ✅ 完成 | `AppsPage.tsx` (13.84KB) |
| 4.4 | 更新 `App.tsx` 路由 — 3个页面从 `<LegacyPage>` 改为原生组件 | ✅ 完成 | — |
| 4.5 | 编译验证 `go build ./...` | ✅ 通过 | — |
| 4.6 | 生产部署模式测试（React 打包 + Go 后端） | ✅ 通过 | — |

**当前 App.tsx 路由状态：**

```tsx
{/* 已迁移页面 - React 原生实现 (9/9 全部完成 🎉) */}
<Route path="dashboard" element={<DashboardPage />} />
<Route path="apps" element={<AppsPage />} />
<Route path="services" element={<ServicesPage />} />
<Route path="configs" element={<ConfigsPage />} />
<Route path="instances" element={<InstancesPage />} />
<Route path="tasks" element={<TasksPage />} />

{/* 新页面 - React 原生实现 */}
<Route path="traces" element={<TracesPage />} />
<Route path="metrics" element={<MetricsPage />} />
<Route path="service-map" element={<ServiceMapPage />} />
```

---

### Phase 5: 通用组件开发（✅ 已完成 — 2026-03-19）

开发后续页面所需的可复用组件。

| 步骤 | 组件 | 说明 | 状态 | 产物 |
|------|------|------|------|------|
| 5.1 | **SearchableSelect** | 支持分组、键盘导航(↑↓Enter/Esc)、搜索高亮、懒加载、自定义输入、点击外部关闭 | ✅ 完成 | `SearchableSelect.tsx` (16KB) |
| 5.2 | **ConfirmDialog** | Context + Promise 模式替代 `window.confirm()`，支持 danger/default 变体 | ✅ 完成 | `ConfirmDialog.tsx` (4.8KB) |
| 5.3 | **DetailDrawer** | 右侧滑入抽屉（Escape 关闭 + 遮罩层 + 可配宽度 + 可选页脚） + DrawerSection/DrawerInfoRow 辅助组件 | ✅ 完成 | `DetailDrawer.tsx` (6.2KB) |
| 5.4 | **TreeNav** | 多级嵌套树、搜索过滤（保留祖先链）、展开折叠、Badge 计数、搜索高亮 | ✅ 完成 | `TreeNav.tsx` (9.6KB) |

**组件 API 概览：**

```mermaid
graph TB
    subgraph "Phase 5 通用组件"
        SS["SearchableSelect<br/>搜索下拉选择器"]
        CD["ConfirmDialog<br/>确认弹窗 (Context)"]
        DD["DetailDrawer<br/>右侧详情抽屉"]
        TN["TreeNav<br/>左侧树导航"]
    end
    
    subgraph "使用场景"
        Tasks["TasksPage<br/>Phase 8"]
        Instances["InstancesPage<br/>Phase 7"]
        Configs["ConfigsPage<br/>Phase 6"]
        Apps["AppsPage<br/>Phase 4 ✅"]
    end
    
    Tasks -->|"任务类型/目标选择"| SS
    Tasks -->|"任务详情"| DD
    Tasks -->|"三级导航树"| TN
    Tasks -->|"删除确认"| CD
    Instances -->|"实例详情"| DD
    Instances -->|"App→Service 树"| TN
    Instances -->|"下线确认"| CD
    Configs -->|"服务树"| TN
    Apps -->|"删除确认"| CD
```

**集成方式：**
- `ConfirmProvider` 已集成到 `App.tsx` Provider 链中（ToastProvider 内层）
- 使用方式：`const confirm = useConfirm(); const ok = await confirm({ message: '...' });`

**构建产物（Phase 5 后）：**

| 文件 | 大小 | gzip |
|------|------|------|
| `vendor-react-*.js` | 50 KB | 18 KB |
| `index-*.js` | 259 KB | 76 KB |
| `vendor-echarts-*.js` | 609 KB | 205 KB |
| `index-*.css` | 24 KB | 5 KB |
| **合计** | **942 KB** | **304 KB** |

### Phase 6: 配置编辑器迁移（✅ 已完成 — 2026-03-19）

将旧版 Configs 配置编辑器完整迁移到 React，复用 Phase 5 的 TreeNav 和 ConfirmDialog 组件。

| 步骤 | 内容 | 状态 | 产物 |
|------|------|------|------|
| 6.1 | **ConfigsPage** — 左侧服务树(TreeNav) + 右侧 JSON 编辑器 | ✅ 完成 | `ConfigsPage.tsx` (24.7KB) |
| 6.2 | 模板推荐 + 缺失字段检测补全逻辑 | ✅ 完成 | 内含于 ConfigsPage |
| 6.3 | JSON 实时语法校验 + 状态栏（版本/字符数/合法性） | ✅ 完成 | 内含于 ConfigsPage |

**ConfigsPage 功能：**

- **左侧服务树**：App → Service → Instance 三级结构（TreeNav），搜索过滤，刷新按钮
- **右侧 JSON 编辑器**：textarea + 实时 JSON 语法校验
- **模板推荐**：空配置时显示 "Apply Template" 按钮
- **缺失字段补全**：与参考模板对比，提示缺少的顶层字段并一键补全
- **操作按钮**：Save / Reset / Delete Config（均使用 ConfirmDialog 确认）
- **脏状态检测**：修改后显示 "Unsaved Changes" 脉冲提示
- **状态栏**：Target 服务名 / Version / 字符数 / JSON 合法性指示器
- **切换保护**：切换服务时如有未保存更改，弹出确认对话框

**构建产物（Phase 6 后）：**

| 文件 | 大小 | gzip |
|------|------|------|
| `vendor-react-*.js` | 50 KB | 18 KB |
| `index-*.js` | 275 KB | 81 KB |
| `vendor-echarts-*.js` | 609 KB | 205 KB |
| `index-*.css` | 27 KB | 5 KB |
| **合计** | **961 KB** | **309 KB** |

### Phase 7: 实例管理迁移（✅ 已完成 — 2026-03-19）

将旧版 Instances 页面完整迁移到 React，复用 TreeNav / DetailDrawer / ConfirmDialog 组件。

| 步骤 | 内容 | 状态 | 产物 |
|------|------|------|------|
| 7.1 | **InstancesPage** — 左侧 App/Service 树(TreeNav) + 右侧实例卡片列表 | ✅ 完成 | `InstancesPage.tsx` (25.6KB) |
| 7.2 | 5 个统计卡片 + 搜索过滤 + 状态筛选 + 树-列表联动 | ✅ 完成 | 内含于 InstancesPage |
| 7.3 | 实例详情抽屉(DetailDrawer) — 基本信息/Arthas 状态/元数据/生命周期 | ✅ 完成 | 内含于 InstancesPage |
| 7.4 | 类型定义更新 — Instance/EnrichedInstance/AgentStatus 匹配后端 JSON | ✅ 完成 | `types/api.ts` |
| 7.5 | API 修复 — getInstances 解包 listResponse 格式 | ✅ 完成 | `api/client.ts` |

**InstancesPage 功能：**

- **左侧导航树**：App → Service 两级结构（TreeNav），"All Instances" 根节点
- **5 个统计卡片**：Total / Online / Offline / Arthas Ready / Arthas N/A（可点击快捷过滤）
- **搜索过滤**：Agent ID / Hostname / Service / IP / App ID 全文搜索
- **树-列表联动**：点击 Service 节点自动筛选右侧列表
- **实例卡片**：状态图标 / Agent ID / 版本 / Online/Offline Badge / 主机名 IP / Arthas 状态 / 心跳时间 / 运行时长
- **详情抽屉**：基本信息 / Arthas 状态 / 元数据(Labels) / 生命周期（启动时间/注册时间/心跳/状态带脉冲动画）
- **操作按钮**：Copy Agent ID / Remove Instance（仅离线实例可下线，使用 ConfirmDialog 确认）
- **Arthas 合并**：`getInstances` + `getArthasAgents` 并行加载后合并 tunnelReady 状态

**构建产物（Phase 7 后）：**

| 文件 | 大小 | gzip |
|------|------|------|
| `vendor-react-*.js` | 50 KB | 18 KB |
| `index-*.js` | 294 KB | 85 KB |
| `vendor-echarts-*.js` | 609 KB | 205 KB |
| `index-*.css` | 30 KB | 6 KB |
| **合计** | **983 KB** | **314 KB** |

### Phase 8: 任务管理迁移（✅ 已完成 — 2026-03-19）

将旧版 Tasks 页面完整迁移到 React，复用 TreeNav / DetailDrawer / ConfirmDialog / SearchableSelect 组件。

| 步骤 | 内容 | 状态 | 产物 |
|------|------|------|------|
| 8.1 | **TasksPage 主页** — 三级导航树(TreeNav) + 任务列表 + 6个状态统计卡片 + 搜索过滤 | ✅ 完成 | `TasksPage.tsx` (66.5KB / 1177行) |
| 8.2 | 任务详情抽屉(DetailDrawer) — 目标信息/参数/时间统计/执行结果/性能分析结果/Artifact | ✅ 完成 | 内含于 TasksPage |
| 8.3 | 创建任务模态框 — SearchableSelect 选类型/目标 + 动态 Instrument/Uninstrument 表单 + 通用 JSON 参数 | ✅ 完成 | 内含于 TasksPage |
| 8.4 | 类型定义更新 — TaskInfoV2/TaskResultRaw/NormalizedTaskResult/Task 匹配后端 JSON | ✅ 完成 | `types/api.ts` |
| 8.5 | API 修复 — getTasks 解包 listResponse + getTask 返回 TaskInfoV2 | ✅ 完成 | `api/client.ts` |
| 8.6 | App.tsx 更新 — 移除 LegacyPage import，所有页面均为 React 原生 | ✅ 完成 | `App.tsx` |

**TasksPage 功能：**

- **三级导航树**：App → Service → Instance（TreeNav），"All Tasks" 根节点，树节点按最新任务时间排序，失败节点自动展开
- **6 个统计卡片**：Total / Running / Pending / Success / Failed / Timeout（可点击快捷过滤）
- **搜索过滤**：Task ID / App / Service / Agent ID / Task Type 全文搜索
- **树-列表联动**：点击 Instance 节点自动筛选右侧列表
- **任务卡片**：状态图标/任务类型/状态Badge/Task ID/App\u00b7Service/Agent ID/创建时间
- **详情抽屉**：
  - 执行目标（Service + Instance ID）
  - 任务参数（JSON 渲染 + 复制）
  - 时间统计（Created/Started/Completed/Duration）
  - 性能分析结果（async-profiler：Flame Graph 链接 / analysis_status / analysis_summary）
  - Artifact（文件引用 + 大小）
  - 结果摘要（智能排序的 key-value 卡片）
  - 完整输出（Error Message / result_json / result_data 三层展示）
  - Cancel Task / Refresh 操作按钮
- **创建任务模态框**：
  - Task Type 选择（SearchableSelect 分组 + 支持自定义输入）
  - Target Agent 选择（SearchableSelect 懒加载在线 Agent）
  - Timeout / Priority 配置
  - **Dynamic Instrument 动态表单**：Class/Method/Type/SpanName/RuleID/ParameterTypes + Capture Options (args/return/maxLen) + Force
  - **Dynamic Uninstrument 动态表单**：By Rule ID 或 By Method 双模式切换
  - 通用 JSON 参数输入（其他任务类型）

**标志性里程碑 🎉：**

- **所有旧版页面已完全迁移到 React，LegacyPage iframe 嵌入机制已从 App.tsx 移除**
- App.tsx 不再引用 `LegacyPage`，9 个页面全部为 React 原生实现

**构建产物（Phase 8 后）：**

| 文件 | 大小 | gzip |
|------|------|------|
| `vendor-react-*.js` | 50 KB | 18 KB |
| `index-*.js` | 344 KB | 94 KB |
| `vendor-echarts-*.js` | 609 KB | 205 KB |
| `index-*.css` | 35 KB | 6 KB |
| **合计** | **1038 KB** | **323 KB** |

### Phase 9: xterm.js 终端 + 清理收尾（✅ 已完成 — 2026-03-19）

封装 xterm.js React 组件，集成 Arthas WebSocket 终端，并完成旧版代码清理。

| 步骤 | 内容 | 状态 | 产物 |
|------|------|------|------|
| 9.1 | **useTerminal Hook** — 封装 xterm.js 的 React Hook（创建/销毁/resize/搜索） | ✅ 完成 | `Terminal/useTerminal.ts` (7.2KB) |
| 9.2 | **TerminalPanel 组件** — 全屏终端面板 + WebSocket 连接 + VSCode 风格搜索框 | ✅ 完成 | `Terminal/TerminalPanel.tsx` (15.4KB) |
| 9.3 | **InstancesPage 集成** — 实例详情抽屉添加 "Open Terminal" / "Attach & Connect" 按钮 | ✅ 完成 | `InstancesPage.tsx` 更新 |
| 9.4 | **移除 LegacyPage.tsx** — 删除 iframe 嵌入机制文件 | ✅ 完成 | 已删除 |
| 9.5 | **移除 webui/ 目录** — 删除全部 Alpine.js 旧版前端文件 | ✅ 完成 | 已删除 |
| 9.6 | **清理 Go 后端** — `webui.go` 移除 legacyUIFS、`router.go` 移除 `/legacy/*` 路由（改为重定向到 /ui/） | ✅ 完成 | `webui.go` + `router.go` |
| 9.7 | **API Client 更新** — 添加 `attachArthas` / `detachArthas` 便捷方法 | ✅ 完成 | `api/client.ts` |
| 9.8 | **Vite 配置更新** — xterm.js 代码分割 + 移除 legacy 代理 | ✅ 完成 | `vite.config.ts` |
| 9.9 | **全量构建验证** | ✅ 通过 | React build + Go build |

**useTerminal Hook 功能：**

- 封装 xterm.js Terminal 实例全生命周期（创建 → mount → dispose）
- 集成 FitAddon、SearchAddon、WebLinksAddon
- ResizeObserver 自适应容器尺寸
- 提供 `write` / `clear` / `focus` / `resize` / `findNext` / `findPrevious` / `getProposedDimensions` / `getXterm` API
- cols-1/rows-1 修正逻辑（与旧版 terminal.js 一致）

**TerminalPanel 组件功能：**

- **全屏覆盖式终端面板**：fixed 定位，圆角 + 阴影
- **头部信息栏**：连接状态指示灯（绿/黄/灰） + Service@IP 显示 + 错误提示
- **操作按钮**：清屏 / 搜索 / Reconnect / 关闭
- **VSCode 风格搜索框**：Ctrl/Cmd+F 触发，Enter/Shift+Enter 导航，支持区分大小写/正则
- **WebSocket 连接管理**：自动 Attach → 获取 Token → 建立 WS → 双向数据传输
- **命令历史**：↑↓ 键导航
- **超时保护**：15秒连接超时自动断开
- **懒加载**：通过 `React.lazy()` 加载，xterm.js 只在打开终端时才下载

**清理成果：**

| 删除项 | 说明 |
|--------|------|
| `webui/` 目录 | 全部 Alpine.js 前端文件（HTML/JS/CSS/第三方库） |
| `LegacyPage.tsx` | iframe 嵌入旧页面的过渡组件 |
| `webui.go` 中 `legacyUIFS` | 旧版 embed.FS 声明 + `newLegacyUIHandler()` |
| `router.go` 中 `/legacy/*` | 旧版前端路由（改为 301 重定向到 /ui/） |
| `vite.config.ts` 中 legacy 代理 | 开发服务器 /legacy 反向代理 |

**新增依赖：**

| 包名 | 版本 | 说明 |
|------|------|------|
| `@xterm/xterm` | ^5.x | xterm.js 核心库 |
| `@xterm/addon-fit` | ^0.x | 自适应容器尺寸 |
| `@xterm/addon-search` | ^0.x | 搜索功能 |
| `@xterm/addon-web-links` | ^0.x | URL 可点击链接 |

**构建产物（Phase 9 后 — 最终版）：**

| 文件 | 大小 | gzip | 说明 |
|------|------|------|------|
| `vendor-react-*.js` | 50 KB | 18 KB | React / ReactDOM / React Router |
| `index-*.js` | 347 KB | 96 KB | 应用主包（全部页面 + 组件） |
| `vendor-echarts-*.js` | 609 KB | 205 KB | ECharts（Tree-shaking） |
| `vendor-xterm-*.js` | 370 KB | 96 KB | xterm.js（懒加载） |
| `TerminalPanel-*.js` | 11 KB | 4 KB | 终端组件（懒加载） |
| `index-*.css` | 37 KB | 7 KB | 主样式 |
| `TerminalPanel-*.css` | 6 KB | 2 KB | xterm 样式（懒加载） |
| **合计** | **1430 KB** | **428 KB** | 其中初始加载仅 1043 KB / 326 KB gzip |

> 注：xterm.js 相关 chunk 仅在用户打开 Arthas 终端时才加载（懒加载），不影响初始页面加载性能。

---

### 🎉 迁移完成总结

从 Phase 0 到 Phase 9，全部完成！

| 指标 | 迁移前 | 迁移后 |
|------|---------|--------|
| 前端框架 | Alpine.js + CDN | React 19 + TypeScript + Vite |
| 前端入口 | 双前端（/ui/ + /legacy/） | 单前端（/ui/） |
| CSS 方案 | CDN Tailwind | 本地 Tailwind 4.x |
| 图表库 | 无 | ECharts（Tree-shaking） |
| 终端 | CDN xterm.js | 本地 @xterm/xterm + React Hook |
| 类型安全 | 无 | TypeScript strict mode |
| 代码分割 | 无 | 4 个 vendor chunk + 2 个 lazy chunk |
| 页面数 | 6（Alpine） | 9（React）+ 3 个新可观测性页面 |
| 可复用组件 | 0 | 7（SearchableSelect/ConfirmDialog/DetailDrawer/TreeNav/TerminalPanel/useTerminal/TimeSeriesChart） |

### 迁移后目标文件结构

```
webui-react/src/
├── api/
│   └── client.ts                   # API 客户端（已有）
├── components/
│   ├── TimeSeriesChart.tsx          # ECharts 图表（已有）
│   ├── TraceDetail.tsx              # Trace 详情（已有）
│   ├── SearchableSelect.tsx         # ✅ Phase 5
│   ├── ConfirmDialog.tsx            # ✅ Phase 5
│   ├── DetailDrawer.tsx             # ✅ Phase 5
│   ├── TreeNav.tsx                  # ✅ Phase 5
│   ├── JsonEditor.tsx               # 🔄 可选优化（Phase 6 暂用 textarea）
│   ├── Terminal/
│       ├── TerminalPanel.tsx        # ✅ Phase 9
│       └── useTerminal.ts           # ✅ Phase 9
├── contexts/
│   ├── AuthContext.tsx              # 认证（已有）
│   └── ToastContext.tsx             # 通知（已有）
├── hooks/
│   ├── useAutoRefresh.ts            # 🆕 自动刷新
│   ├── useWebSocket.ts              # 🆕 WebSocket 管理
│   └── useConfirm.ts                # 🆕 确认弹窗 Hook
├── layouts/
│   ├── MainLayout.tsx               # 已有
│   └── Sidebar.tsx                  # 已有
├── pages/
│   ├── DashboardPage.tsx            # ✅ Phase 4
│   ├── AppsPage.tsx                 # ✅ Phase 4
│   ├── ServicesPage.tsx             # ✅ Phase 4
│   ├── ConfigsPage.tsx              # ✅ Phase 6
│   ├── InstancesPage.tsx            # ✅ Phase 7
│   ├── TasksPage.tsx                # ✅ Phase 8
│   ├── TracesPage.tsx               # ✅ Phase 1
│   ├── MetricsPage.tsx              # ✅ Phase 2
│   ├── ServiceMapPage.tsx           # ✅ Phase 3
│   └── LoginPage.tsx                # ✅ Phase 0
├── types/
│   ├── api.ts                       # 已有
│   ├── trace.ts                     # 已有
│   └── metric.ts                    # 已有
└── utils/
    ├── format.ts                    # 🆕 从旧版 utils.js 移植
    ├── trace.ts                     # 已有
    └── metric.ts                    # 已有
```

### 迁移风险与注意事项

| 风险 | 影响 | 缓解措施 |
|------|------|----------|
| xterm.js React 集成复杂 | 终端功能可能有 bug | 参考 terminal.js 的 fitAndNotify 逻辑，保留搜索功能 — **已完成** |
| 任务创建表单逻辑复杂 | 动态表单（instrument/uninstrument/profiling 各有不同参数） | 逐一对照 tasks.js 中的 `buildInstrumentParams()` 等方法 — **已完成** |
| CDN 依赖消除 | 旧版依赖 CDN（Tailwind/Alpine/xterm/FontAwesome） | React 版全部本地化 — **已完成** |
| 双前端过渡期 | 迁移期间两套前端共存 | 已全部迁移，旧前端已删除 — **已完成** |

### 可选优化清单

| 优化项 | 当前问题 | 建议 |
|--------|----------|------|
| JSON 编辑器 | textarea 无语法高亮 | 可选 Monaco Editor 或 CodeMirror |
| 确认弹窗 | `window.confirm()` 原生样式 | React ConfirmDialog 组件 |
| 表格排序/分页 | 无分页 | 添加前端分页 + 排序 |
| 数据缓存 | 每次切页重新请求 | React Query 或 SWR 缓存 |
| 错误边界 | 无错误处理 | React ErrorBoundary 组件 |
| 响应式 | 部分移动端适配差 | Tailwind 响应式优化 |

---

## 🐛 Bug 修复记录

### BugFix-001: ConfigsPage 服务树为空（2026-03-19）

**问题描述：** Configs 页面的服务树完全无法显示，报错 `H.map is not a function`，导致无法查看/编辑任何服务配置。

**根因分析：**

```mermaid
graph LR
    A["buildConfigTree()"] -->|"inst.app_name === app.name"| B["匹配失败 ❌"]
    B --> C["appInstances = [] 空数组"]
    C --> D["serviceMap 为空"]
    D --> E["所有 App 节点下<br/>serviceNodes = []"]
    E --> F["页面显示<br/>'No services found'"]
    
    A2["buildConfigTree()"] -->|"inst.app_id === app.id"| B2["匹配成功 ✅"]
    B2 --> C2["appInstances = 3 个实例"]
    C2 --> D2["按 service_name 分组"]
    D2 --> E2["服务树正常显示"]
    
    style B fill:#ef4444,color:#fff
    style B2 fill:#22c55e,color:#fff
```

**API 数据结构：** Instance 对象中只有 `app_id` 字段（关联 App 的 ID），**没有** `app_name` 字段。

**修复内容：** `ConfigsPage.tsx` 第 112 行

| 修复前（错误） | 修复后（正确） |
|---------------|---------------|
| `inst.app_name === app.name` | `inst.app_id === app.id` |

**验证结果：**
- ✅ TypeScript 编译通过
- ✅ Vite 生产构建通过
- ✅ Go 后端构建通过

### BugFix-002: Dashboard 统计数据全部显示 0（2026-03-19）

**问题描述：** Dashboard 页面的 Total Apps、Online Instances、Total Instances 等统计卡片全部显示 0，与实际数据不符。

**根因分析：**

```mermaid
graph TB
    A["后端 /api/v2/dashboard/overview"] --> B["返回嵌套结构"]
    B --> C["{ apps: { total: 1 },<br/>instances: { online: 1, total: 3 },<br/>tasks: { pending: 0 } }"]
    
    D["前端 DashboardOverview 类型"] --> E["期望扁平结构"]
    E --> F["{ total_apps, online_instances,<br/>total_instances, ... }"]
    
    C --> G["字段名不匹配 ❌"]
    F --> G
    G --> H["dashboard?.total_apps = undefined"]
    H --> I["显示 ?? 0 = 0"]
    
    style G fill:#ef4444,color:#fff
    style I fill:#ef4444,color:#fff
```

**修复内容：** `client.ts` — `getDashboard()` 方法

在 API Client 中将后端嵌套结构映射为前端扁平结构：

```typescript
// 修复前：直接返回
return this.request<DashboardOverview>('GET', '/dashboard/overview');

// 修复后：转换数据结构
return this.request<Record<string, Record<string, number>>>('GET', '/dashboard/overview')
  .then(res => ({
    total_apps: res.apps?.total ?? 0,
    online_instances: res.instances?.online ?? 0,
    total_instances: res.instances?.total ?? 0,
    // ...
  }));
```

### BugFix-003: TreeNav button 嵌套 button HTML 合法性问题（2026-03-19）

**问题描述：** 控制台报错 `In HTML, <button> cannot be a descendant of <button>`，因为 TreeNodeItem 渲染了外层 `<button>` 包裹内层展开/折叠 `<button>` 的结构。

**修复内容：** `TreeNav.tsx` — `TreeNodeItem` 函数

将内部的展开/折叠 `<button>` 改为 `<span role="button" tabIndex={0}>`，保持交互行为不变且符合 HTML 规范。同时增加了键盘事件支持（Enter / Space）。

### BugFix-004: Configs 页面点击 App 节点触发 404 请求（2026-03-19）

**问题描述：** 点击服务树中的 App 节点（如 "test102"）时，触发了 `/api/v2/apps/{id}/config/services/` 请求（尾部斜杠），后端返回 404。原因是 App 节点的 `serviceName` 为空字符串。

**根因分析：**

```mermaid
graph LR
    A["点击 App 节点"] --> B["handleSelectNode(node)"]
    B --> C["nodeData.type = 'app'<br/>nodeData.serviceName = ''"]
    C --> D["loadConfig(nodeData)"]
    D --> E["getAppServiceConfig(appId, '')"]
    E --> F["GET /apps/{id}/config/services/"]
    F --> G["404 Not Found ❌"]
    
    style F fill:#ef4444,color:#fff
    style G fill:#ef4444,color:#fff
```

**修复内容（两处）：**

1. `ConfigsPage.tsx` — `handleSelectNode`：App 节点只设置选中状态，不调用 `loadConfig`
2. `TreeNav.tsx` — `handleSelect`：当 `allowSelectParent=true` 时，点击有子节点的节点同时触发展开/折叠和选中回调

**修复后效果：**
- ✅ 点击 App 节点 → 展开子服务列表，右侧保持 "Select a service" 提示
- ✅ 点击 Service 节点 → 正确加载服务配置
- ✅ 控制台 0 错误、0 警告

---

### UI-Enhance-001: 全站 UI/UX 优化（2026-03-19）

**背景：** 对全部页面进行了专业 UI/UX 评审，发现多项视觉和交互问题，按优先级分批修复。

**优化内容：**

```mermaid
graph TB
    subgraph "P0 — Font Awesome 图标修复"
        P0A["index.html"] -->|"添加 CDN link"| P0B["Font Awesome 6.5.1"]
        P0B --> P0C["50+ 个图标全部正确渲染<br/>（侧边栏/卡片/按钮/登录页）"]
    end
    
    subgraph "P1 — 侧边栏导航增强"
        P1A["Sidebar.tsx"] --> P1B["导航分组<br/>Management / Observability"]
        P1A --> P1C["选中态左边框指示器<br/>3px primary-400 圆角条"]
        P1A --> P1D["Footer 增强<br/>版本号 + Logout 标签"]
    end
    
    subgraph "P1 — Dashboard 布局增强"
        P1E["DashboardPage.tsx"] --> P1F["统计卡片可点击跳转"]
        P1E --> P1G["Quick Actions 2×3 网格"]
        P1E --> P1H["System Info 信息卡片"]
        P1E --> P1I["Health Overview<br/>健康度进度条 + 任务队列 + 可观测性入口"]
    end
    
    subgraph "P2 — Metrics 时间选择器"
        P2A["metric.ts"] -->|"标签精简"| P2B["'Last 15 min' → '15m'"]
        P2C["MetricsPage.tsx"] -->|"flex-nowrap"| P2D["9 个按钮一行显示<br/>选中态改为蓝色实心"]
    end
    
    style P0C fill:#22c55e,color:#fff
    style P1B fill:#3b82f6,color:#fff
    style P1C fill:#3b82f6,color:#fff
    style P1I fill:#3b82f6,color:#fff
    style P2D fill:#a855f7,color:#fff
```

**修改文件：**

| 文件 | 修改内容 |
|------|---------|
| `index.html` | 引入 Font Awesome 6.5.1 CDN（integrity + crossorigin） |
| `Sidebar.tsx` | 重写：导航分组 + 选中态左边框 + 图标颜色区分 + Footer 版本号 |
| `DashboardPage.tsx` | 增强：卡片可点击 + Quick Actions 网格 + System Info + Health Overview |
| `MetricsPage.tsx` | 时间按钮样式优化（flex-nowrap + 实心选中态） |
| `metric.ts` | TIME_RANGE_PRESETS 标签精简（去掉 "Last " 前缀） |

**验证结果：**
- ✅ TypeScript 编译通过
- ✅ Vite 生产构建通过（354KB index.js）
- ✅ 浏览器联调全部页面图标正确显示
- ✅ 侧边栏分组 + 选中态 + Footer 显示正常
- ✅ Dashboard 三段式布局填满页面
- ✅ Metrics 时间按钮一行显示无换行

---

## 📝 遗留问题

- [ ] Service Map 依赖数据为空，可能需要部署 Jaeger spark-dependencies job 来生成依赖关系
- [x] ~~旧页面迁移优先级待排序~~ → 已制定 Phase 4-9 完整计划 → **全部完成**
- [x] ~~xterm.js 终端组件的 React 封装方案~~ → Phase 9 已完成
- [ ] 生产构建的资源哈希策略待确认
- [ ] Service Map 节点右键菜单（跳转 Metrics / Traces 快捷入口）
- [ ] 自定义 Dashboard 面板保存功能
- [ ] `index.html` 中 favicon 仍引用 `/vite.svg`，在生产部署时会 404（不影响功能）
