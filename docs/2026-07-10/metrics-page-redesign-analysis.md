# Metrics 页面重构分析 — 参考 Grafana/Tempo 设计体系

> **文档状态**：Sprint 1/2/3 已完成 ✅  
> **创建时间**：2026-07-10  
> **最后更新**：2026-07-10（Sprint 3 实施完成）  
> **范围**：`extension/adminext/webui-react/src` 中 Metrics 相关页面与组件  

---

## §1 现状评估

### 1.1 当前技术栈

| 维度 | 现状 |
|------|------|
| 框架 | React + TypeScript |
| 样式 | Tailwind CSS v3.4（纯工具类，无 CSS Modules） |
| 图表库 | ECharts v6（通过 echarts-for-react 按需加载） |
| 图标 | Font Awesome（`<i className="fas fa-xxx">`） |
| 路由 | React Router v6 |
| 状态管理 | 组件级 useState（无全局状态） |

### 1.2 当前页面结构

```
MetricsPage.tsx (507 行单文件)
├── Tab 切换: [Metric Query] / [RED Dashboard]
├── 时间范围选择器 (按钮组)
├── Query Tab
│   ├── 查询输入卡片 (metric name + service filter + 执行按钮)
│   ├── 示例 metric 按钮组
│   └── 图表结果卡片 (TimeSeriesChart)
└── RED Tab
    ├── Service 下拉选择
    └── 2 列 Grid (6 个 RED 面板)
```

### 1.3 现有问题识别

| # | 问题 | 设计原则违反 | 严重度 |
|---|------|-------------|--------|
| 1 | **单文件 507 行**，查询逻辑、数据转换、UI 渲染混合 | SRP / 高内聚低耦合 | 🔴 高 |
| 2 | **无 DataZoom 交互**——图表不支持鼠标框选缩放/拖拽平移 | Grafana: 直接操作原则 | 🔴 高 |
| 3 | **时间范围只有预设按钮**，无自定义起止时间选择 | 灵活性 / 完备性 | 🟡 中 |
| 4 | **无自动刷新机制**——打开后数据静止，需手动刷新 | Grafana: 实时监控 | 🟡 中 |
| 5 | **图表无阈值线/告警标注**——纯数据展示，无上下文 | Grafana: 信息层次 | 🟡 中 |
| 6 | **Tooltip 模式固定为 axis**，无法切换 Single/All/Hidden | Grafana: 渐进复杂度 | 🟢 低 |
| 7 | **Legend 仅底部列表**，不支持右侧表格模式或 value 计算 | Grafana: Legend 设计 | 🟢 低 |
| 8 | **图表颜色硬编码 15 色**，无深色主题适配 | 可扩展性 / 一致性 | 🟡 中 |
| 9 | **查询输入用原生 datalist**，补全体验差（无模糊搜索、无高亮） | UX 精细度 | 🟡 中 |
| 10 | **RED Dashboard 无 Rate/Error/Duration 联动**——不能从异常指标直接跳转 Trace | Grafana Tempo: 跨信号关联 | 🟡 中 |
| 11 | **无查询历史/收藏**——每次都需重新输入 metric 名称 | 用户效率 | 🟢 低 |
| 12 | **响应式不足**——移动端 2 列 grid 不适配 | 自适应 | 🟢 低 |

---

## §2 Grafana / Tempo 设计体系精华提炼

### 2.1 Grafana Time Series Panel 核心设计原则

| 原则 | 含义 | 当前项目差距 |
|------|------|-------------|
| **默认优先** | 零配置开箱即用，合理默认值 | ✅ 已满足（自动 step 计算） |
| **渐进式复杂度** | 基础 → 中级 → 高级分层暴露 | ❌ 无高级选项入口 |
| **直接操作** | Zoom/Pan/Brush 鼠标直接交互 | ❌ 只有静态展示 |
| **信息层次** | Tooltip → Legend → 阈值 → 注解 | ❌ 只有基础 Tooltip + Legend |
| **自适应显示** | 点/轴/密度自动根据数据调整 | ⚠️ 部分满足（smooth: true） |
| **组合灵活性** | 同面板混合 Line/Area/Bar/Points | ❌ 固定为 line 或 area |

### 2.2 Grafana Time Series 关键视觉特征

```
┌──────────────────────────────────────────────────────────────┐
│ Panel Title                            [Legend Table: Right]  │
├──────────────────────────────────────────────────────────────┤
│                                                              │
│  ┌── Y-Axis ──┐                                             │
│  │   100ms    │   ╭─╮      ╭──╮                            │
│  │    80ms    │  ╭╯ ╰╮   ╭╯  ╰───╮      Gradient Fill     │
│  │    60ms    │ ╭╯   ╰╮ ╭╯       ╰╮                        │
│  │    40ms    │╭╯     ╰╮╯         ╰─── Threshold Line ───  │
│  │    20ms    │╯  ░░░░░░░░░░░░░░░░░░░░  (soft fill below) │
│  │     0ms    │▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓│
│  └────────────┘                                             │
│  10:00   10:15   10:30   10:45   11:00   (X-Axis: Time)    │
│                                                              │
│  ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁ DataZoom Slider ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁ │
├──────────────────────────────────────────────────────────────┤
│  ● p50 (23.4ms)  ● p95 (67.8ms)  ● p99 (142ms)   [Right] │
└──────────────────────────────────────────────────────────────┘
```

**关键视觉元素**：
- **渐变填充**（Gradient Opacity）：从序列色到透明的渐变
- **阈值线**：水平虚线 + 轻微背景色标识告警区域
- **DataZoom 滑块**：底部迷你缩略图 + 可拖拽范围选择器
- **Legend 表格**：右侧/底部，显示 min/max/avg/last 等计算值
- **Tooltip Cross-hair**：垂直十字线 + 所有系列数值排列
- **颜色方案**：低饱和度主色 + 渐变，避免视觉噪音

### 2.3 Grafana Tempo — Traces Drilldown 设计精华

| 特征 | 设计意图 | 可借鉴点 |
|------|---------|---------|
| **RED 驱动可视化** | 从 Rate/Error/Duration 入手发现问题 | ✅ 已有 RED Tab，可深化 |
| **无需编写查询语言** | 降低使用门槛，点击即探索 | 改进查询输入体验 |
| **自动特征识别** | 系统自动高亮异常点/趋势变化 | 可增加异常标注 |
| **跨信号链接** | Metric → Trace → Log 一键跳转 | 强化 RED → Trace 联动 |
| **Service Graph** | 拓扑可视化展示服务依赖 | 可结合已有 ServiceMap |
| **Exemplars** | Metric 数据点关联到具体 Trace ID | 高级特性，可考虑 |

### 2.4 Grafana 的 Dark Theme 配色体系

```
背景层次:
  L0 (Canvas):     #111217  →  最底层画布
  L1 (Surface):    #181b1f  →  面板/卡片背景
  L2 (Elevated):   #22252b  →  Tooltip/Dropdown 弹出层

文字层次:
  Primary:         #e0e0e0  →  标题/数值
  Secondary:       #8e8e8e  →  描述/标签
  Disabled:        #5a5a5a  →  不可用态

图表色板 (Grafana Classic):
  Blue:    #73BF69 → #FADE2A → #FF9830 → #F2495C → #5794F2
  序列1:   #7EB26D   序列2: #EAB839   序列3: #6ED0E0
  序列4:   #EF843C   序列5: #E24D42   序列6: #1F78C1

网格/轴线:
  Grid:            rgba(255,255,255,0.04)  →  极轻微虚线
  Axis:            rgba(255,255,255,0.10)  →  轴线
```

---

## §3 重构方案设计

### 3.1 架构重构（高内聚低耦合）

将当前 507 行单文件拆分为职责明确的模块：

```
src/pages/MetricsPage.tsx          (容器组件，~80行)
src/features/metrics/
├── components/
│   ├── MetricQueryPanel.tsx        (查询面板组件)
│   ├── MetricExplorer.tsx          (Metric 名称浏览器/搜索)
│   ├── RedDashboard.tsx            (RED Dashboard 面板组)
│   ├── TimeRangeSelector.tsx       (时间范围选择器，可复用)
│   ├── AutoRefreshToggle.tsx       (自动刷新控制)
│   └── PanelCard.tsx               (单面板容器，统一标题/loading/error)
├── hooks/
│   ├── useMetricQuery.ts           (查询状态管理 hook)
│   ├── useRedPanels.ts             (RED 面板数据管理 hook)
│   ├── useAutoRefresh.ts           (自动刷新逻辑)
│   └── useTimeRange.ts             (时间范围管理)
├── utils/
│   └── metric.ts                   (已有，保持不变)
└── types/
    └── metric.ts                   (已有，保持不变)

src/components/charts/
├── TimeSeriesChart.tsx             (增强版，支持 DataZoom/Threshold)
├── ChartThemeProvider.tsx          (图表主题上下文)
└── chartTheme.ts                   (Grafana-style 主题配置)
```

**模块职责划分**：

| 模块 | 职责 | 对外接口 |
|------|------|---------|
| `useMetricQuery` | 管理查询 state + API 调用 + 错误处理 | `{ execute, data, loading, error }` |
| `useTimeRange` | 时间范围选择/自定义/持久化 | `{ range, setRange, getParams }` |
| `useAutoRefresh` | 定时刷新间隔管理 | `{ interval, setInterval, enabled }` |
| `TimeSeriesChart` | 纯展示组件，接收 series/options 渲染 | `<TimeSeriesChart series={} options={} />` |
| `PanelCard` | 统一面板 UI 容器（标题、描述、loading 骨架屏、error 状态） | `<PanelCard title="" loading={}>...</PanelCard>` |

### 3.2 图表增强（参考 Grafana Time Series）

#### 现有 → 改进

| 维度 | 现状 | 目标（Grafana 风格） |
|------|------|---------------------|
| **DataZoom** | 无 | 底部 slider + 框选缩放 + 双击还原 |
| **Tooltip** | 固定 axis 模式 | 支持 Single/All 切换，显示 cross-hair |
| **Legend** | 底部列表（≤10 系列才显示） | 右侧表格模式 + min/max/avg/last 计算值 |
| **渐变** | 仅 area 模式有基础渐变 | 全局 Opacity Gradient 模式，更细腻 |
| **阈值线** | 无 | 可配置水平阈值线（如 P99 SLO = 200ms） |
| **空数据处理** | "No data" 静态占位 | Connect null + 断开显示策略 |
| **Y 轴** | 单 Y 轴 auto | 双 Y 轴支持（如 RPS + Latency 叠加） |
| **交互** | 无 | 点击数据点 → 跳转到该时间点的 Traces |

#### ECharts 配置增强点

```typescript
// 增加 DataZoom 交互（框选缩放 + 底部滑块）
dataZoom: [
  { type: 'inside', xAxisIndex: 0 },  // 鼠标滚轮/框选
  { type: 'slider', xAxisIndex: 0, height: 20, bottom: 0,
    borderColor: 'transparent',
    backgroundColor: 'rgba(0,0,0,0.03)',
    fillerColor: 'rgba(59,130,246,0.1)',
    handleStyle: { color: '#3b82f6' },
  },
],

// 增加阈值线（markLine）
markLine: {
  silent: true,
  lineStyle: { type: 'dashed', color: '#ef4444', width: 1 },
  data: [{ yAxis: thresholdValue, name: 'SLO' }],
},

// Tooltip 增强为 All 模式 + 排序
tooltip: {
  trigger: 'axis',
  axisPointer: { type: 'cross', crossStyle: { color: '#999' } },
  order: 'valueDesc',  // 按值降序排列
},

// Legend 增强为表格模式
legend: {
  type: 'scroll',
  orient: 'vertical',
  right: 0,
  top: 20,
  bottom: 20,
  // ... 配合 formatter 显示 min/max/last
},
```

### 3.3 交互增强（参考 Grafana Tempo）

#### 3.3.1 时间范围选择器升级

```
┌─────────────────────────────────────────────────────┐
│ [15m] [30m] [1h] [3h] [6h] [12h] [24h] [7d] │Custom│
│                                                     │
│ ┌── Custom Range Popover (展开时) ──────────────┐  │
│ │  From: [2026-07-10 09:00 ▼]                    │  │
│ │  To:   [2026-07-10 10:00 ▼]  [Apply]          │  │
│ └────────────────────────────────────────────────┘  │
│                                                     │
│ Auto-refresh: [Off ▼] [5s | 10s | 30s | 1m | 5m]  │
└─────────────────────────────────────────────────────┘
```

#### 3.3.2 Metric 名称搜索器升级

用 **Combobox 组件**（模糊搜索 + 高亮匹配 + 键盘导航）替代原生 datalist：

```
┌─────────────────────────────────────────┐
│ 🔍 http_server_requ█                    │
├─────────────────────────────────────────┤
│  http_server_request_duration_seconds    │  ← 匹配高亮
│  http_server_request_duration_seconds_count │
│  http_server_request_error_count         │
│  ─────────── Recent ──────────           │
│  go_goroutines                           │  ← 最近查询
│  process_cpu_seconds_total               │
└─────────────────────────────────────────┘
```

#### 3.3.3 RED → Trace 联动增强

在 RED Dashboard 的每个面板上增加：
- **异常时间段高亮**：Error Rate > 阈值的时间区间用红色背景标注
- **点击跳转**：点击图表上的数据点 → 跳转到该时间段的 Traces 列表
- **Exemplar 标注**（进阶）：在 metric 图表上标注具体的 Trace 采样点

### 3.4 视觉设计升级

#### 3.4.1 图表主题（参考 Grafana）

```typescript
// chartTheme.ts — Grafana-inspired light theme
export const GRAFANA_LIGHT_THEME = {
  // 背景
  panelBg: '#ffffff',
  canvasBg: '#f8fafc',  // Tailwind slate-50

  // 网格
  gridColor: 'rgba(0, 0, 0, 0.04)',
  gridStyle: 'dashed' as const,

  // 轴线
  axisLineColor: 'rgba(0, 0, 0, 0.08)',
  axisLabelColor: '#6b7280',  // Tailwind gray-500
  axisFontSize: 11,

  // Tooltip
  tooltipBg: 'rgba(255, 255, 255, 0.98)',
  tooltipBorder: '#e5e7eb',
  tooltipShadow: '0 4px 12px rgba(0,0,0,0.08)',

  // 色板（Grafana Classic 低饱和度适配 light theme）
  colors: [
    '#5470c6',  // 蓝
    '#91cc75',  // 绿
    '#fac858',  // 黄
    '#ee6666',  // 红
    '#73c0de',  // 浅蓝
    '#fc8452',  // 橙
    '#9a60b4',  // 紫
    '#ea7ccc',  // 粉
  ],

  // 线条
  lineWidth: 1.5,  // Grafana 默认值
  smooth: false,   // Grafana 默认 Linear 而非 Smooth

  // 渐变填充
  gradientOpacity: [0.25, 0.02],  // 从上到下渐变
};
```

#### 3.4.2 面板卡片设计（参考 Grafana Panel）

```
┌─ PanelCard ──────────────────────────────────────────────┐
│ ┌─ Header ─────────────────────────────────────────────┐ │
│ │ 🟢 Request Rate              ⟳ 5s │ ⚙ │ ⋯ │ ↗ │   │ │
│ │     每秒请求数 (QPS)                                  │ │
│ └──────────────────────────────────────────────────────┘ │
│ ┌─ Chart Area ─────────────────────────────────────────┐ │
│ │                                                      │ │
│ │         (TimeSeriesChart)                            │ │
│ │                                                      │ │
│ └──────────────────────────────────────────────────────┘ │
│ ┌─ Footer/Legend ──────────────────────────────────────┐ │
│ │ ● series-1  min: 12  max: 89  avg: 45  last: 67    │ │
│ └──────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────┘
```

Header 图标含义：
- `⟳ 5s`：自动刷新间隔提示
- `⚙`：面板设置（切换 Line/Area/Bar、设置阈值等）
- `⋯`：更多操作（导出 CSV、复制查询、全屏）
- `↗`：跳转到关联 Trace（Tempo 风格 cross-signal link）

---

## §4 改动优先级排序（ROI 矩阵）

| 优先级 | 改动项 | 工作量 | 用户价值 | 依赖 |
|--------|--------|--------|---------|------|
| **P0** | 模块拆分（SRP 治理） | 中 | 高（可维护性） | 无 |
| **P0** | DataZoom 框选缩放/平移 | 低 | 高（核心交互缺失） | 无 |
| **P1** | 时间范围自定义 + 自动刷新 | 中 | 高（监控场景核心） | 无 |
| **P1** | Metric 名称 Combobox | 中 | 高（查询效率） | 无 |
| **P1** | Legend 表格模式 + 统计值 | 低 | 中（数据可读性） | 无 |
| **P2** | Grafana 风格图表主题 | 低 | 中（视觉专业度） | 无 |
| **P2** | RED → Trace 点击联动 | 中 | 高（Tempo 核心体验） | TraceAPI |
| **P2** | 阈值线/异常标注 | 低 | 中（可观测性） | 配置接口 |
| **P3** | 查询历史/收藏 | 中 | 中（用户效率） | LocalStorage |
| **P3** | 深色主题适配 | 中 | 低（当前无切换入口） | Theme Provider |

---

## §5 Sprint 划分建议

| Sprint | 内容 | 验收 | 状态 |
|--------|------|------|------|
| 1 | 模块拆分 + DataZoom + 图表主题升级 | 编译通过，交互可用，视觉一致 | ✅ 已完成（2026-07-10） |
| 2 | 时间范围自定义 + 自动刷新 + Combobox 搜索 | 功能完整，键盘可达 | ✅ 已完成（2026-07-10） |
| 3 | Legend 表格 + 阈值线 + RED→Trace 联动 | 联动跳转正确，视觉完整 | ✅ 已完成（2026-07-10） |
| 4 | 查询历史/收藏 + 响应式适配 + 深色主题（可选） | 体验完整 | ⏳ 待实施 |

---

## §6 对比总结：当前设计 vs Grafana/Tempo 标杆

| 维度 | 当前 | Grafana/Tempo | 差距评级 |
|------|------|--------------|---------|
| **图表交互** | 静态展示 | Zoom/Pan/Brush/Click-to-explore | 🔴 大差距 |
| **数据密度适应** | 固定样式 | Auto points/axis/density | 🟡 中差距 |
| **信息层次** | 单一 tooltip | Tooltip + Legend Table + Threshold + Annotation | 🔴 大差距 |
| **跨信号关联** | 有 "View Traces" 按钮 | 点击数据点 → Trace，Exemplar 标注 | 🟡 中差距 |
| **查询构建** | 手动输入 + datalist | Combobox + 历史 + 语法高亮 | 🟡 中差距 |
| **时间控制** | 预设按钮 | 预设 + Custom + Auto-refresh | 🟡 中差距 |
| **代码架构** | 507 行单文件 | 模块化组件/hook 分离 | 🔴 大差距 |
| **主题/色板** | 硬编码 15 色 | 主题系统 + 可配色板 | 🟢 小差距 |
| **RED Dashboard** | 6 面板 grid | RED + Exemplar + Service Graph | 🟡 中差距 |
| **加载体验** | 简单 spinner | 骨架屏 + 渐进加载 + 错误重试 | 🟢 小差距 |

---

## §7 核心结论

### 是否需要重构？

**是，需要重构**。主要原因：

1. **架构问题（P0）**：507 行单文件违反 SRP，逻辑/视图/数据混杂，难以维护和测试
2. **交互缺失（P0）**：缺少 DataZoom 是可观测性工具的核心交互缺失——用户无法通过图表直接探索时间维度的数据
3. **与行业标杆差距明显**：在图表交互、信息层次、跨信号关联三个维度存在 🔴 大差距

### 推荐路径

**渐进式重构**（不推荐一次性大重写）：
- Sprint 1 先解决架构问题和最高价值的交互缺失（DataZoom）
- Sprint 2-3 逐步补齐 Grafana/Tempo 的核心交互模式
- Sprint 4 锦上添花（深色主题、高级特性）

### 不建议做的事情

- ❌ 不建议切换图表库（ECharts → Recharts/D3）——ECharts 本身能力足够，问题在于没有充分使用
- ❌ 不建议引入 Grafana 源码/SDK——我们是独立产品，参考设计理念即可
- ❌ 不建议在 Sprint 1 就做深色主题——需要先建立 Theme Provider 基础设施

---

## §8 遗留问题

1. **是否需要支持多 Y 轴？** Grafana 支持，但当前 RED 面板每个面板只展示单一 metric，暂不需要；未来如果做"RPS + Latency 叠加视图"则需要
2. **Exemplar（数据点 → Trace ID 标注）是否纳入？** 需要后端在 metric 写入时记录采样 Trace ID，当前 `StoredMetricDataPoint` 结构中无此字段，需后端协同改造
3. **自动刷新的后端 WebSocket/SSE 推送 vs 前端轮询？** 建议先用前端 `setInterval` 轮询（简单可靠），后续如有实时性需求再考虑 SSE

---

## §9 Sprint 1 实施记录（2026-07-10）

### 改动清单

| # | 文件 | 改动类型 | 说明 |
|---|---|---|---|
| 1 | `components/charts/chartTheme.ts` | **新增** | Grafana 风格图表主题（色板/DataZoom/cross-hair tooltip/grid 样式） |
| 2 | `components/TimeSeriesChart.tsx` | **改造** | 委托 `buildTimeSeriesOption` 主题函数；新增 `showDataZoom`/`legendPlacement` props |
| 3 | `features/metrics/hooks/useTimeRange.ts` | **新增** | 时间范围管理 + 参数计算 hook |
| 4 | `features/metrics/hooks/useMetricQuery.ts` | **新增** | 查询面板状态管理 hook |
| 5 | `features/metrics/hooks/useRedPanels.ts` | **新增** | RED Dashboard 面板数据管理 + 并行 API 调用 hook |
| 6 | `features/metrics/hooks/useMetricAvailability.ts` | **新增** | Backend 可用性检测 + metric names 加载 hook |
| 7 | `features/metrics/components/TimeRangeSelector.tsx` | **新增** | 可复用的时间范围选择器组件 |
| 8 | `features/metrics/components/MetricQueryPanel.tsx` | **新增** | Metric 查询面板组件 |
| 9 | `features/metrics/components/RedDashboard.tsx` | **新增** | RED Dashboard 组件 |
| 10 | `pages/MetricsPage.tsx` | **精简** | 507 行 → ~120 行容器组件，委托 hooks + 子组件 |

### 架构变化

```
Before (507 行单文件)          After (模块化)
──────────────────────         ──────────────────
MetricsPage.tsx                MetricsPage.tsx        (~120行 容器)
  ├─ 状态管理 (useState x 8)    ├─ useTimeRange
  ├─ API 调用逻辑                ├─ useMetricQuery
  ├─ 数据转换逻辑                ├─ useRedPanels
  ├─ UI 渲染 (300+ 行 JSX)      ├─ useMetricAvailability
  └─ 图表配置                    ├─ MetricQueryPanel
                                ├─ RedDashboard
                                ├─ TimeRangeSelector
                                ├─ TimeSeriesChart (+DataZoom)
                                └─ chartTheme.ts
```

### 新增功能

| 功能 | 实现方式 |
|------|---------|
| **DataZoom 框选缩放** | 鼠标拖拽框选 + 鼠标滚轮缩放（inside） |
| **DataZoom 滑块** | 底部迷你缩略图滑块（slider），可拖拽范围 |
| **Cross-hair tooltip** | axisPointer: cross，垂直+水平辅助线 |
| **Tooltip 按值降序** | `order: 'valueDesc'` |
| **Grafana 色板** | 8 色低饱和度调色板（`#5470c6` 等） |
| **图例右侧放置** | `legendPlacement:'right'` 支持 |
| **组件可复用** | TimeRangeSelector/TimeSeriesChart 可跨页面使用 |

### 验证结果

```
# TypeScript 编译
$ npx tsc --noEmit
(0 errors)

# Vite 生产构建
$ npx vite build
✓ built in 3.55s
  MetricsPage.js   10.56 kB (gzip: 3.39 kB)
  TimeSeriesChart.js 12.22 kB (gzip: 5.04 kB)
```

---

## §10 Sprint 2 实施记录（2026-07-10）

### 改动清单

| # | 文件 | 改动类型 | 说明 |
|---|---|---|---|
| 1 | `hooks/useAutoRefresh.ts` | **新增** | 自动刷新 hook（Off/5s/10s/30s/1m/5m 档位） |
| 2 | `components/TimeRangeSelector.tsx` | **升级** | 新增 Custom Range popover + Auto-refresh 下拉 |
| 3 | `components/MetricNameCombobox.tsx` | **新增** | 模糊搜索 KeyNav Combobox 替代原生 datalist |
| 4 | `components/MetricQueryPanel.tsx` | **升级** | `<input list>` → `<MetricNameCombobox>` |
| 5 | `pages/MetricsPage.tsx` | **修改** | 接入 useAutoRefresh，传递 refresh props |

### 新增功能详情

#### 时间范围选择器升级

```
Before (单一按钮组)                    After
───────────────────                    ─────
[15m][30m][1h]...[7d]                  [15m][30m][1h]...[7d][📅 Custom] [5s ▼]
                                         └─ Custom Range Popover     └─ Auto-refresh
                                            ┌──────────────┐
                                            │ From: [picker]│
                                            │ To:   [picker]│
                                            │ [Apply][Cancel]│
                                            └──────────────┘
```

#### MetricNameCombobox 对比

| 维度 | 原生 datalist | MetricNameCombobox |
|------|-------------|-------------------|
| 搜索方式 | 前缀匹配（浏览器实现不一致） | 模糊子串匹配（可控） |
| 键盘导航 | ↑↓ + Enter（有限） | ↑↓ 高亮+滚动 + Enter 选择 + Esc 关闭 |
| 匹配高亮 | 无 | `<mark>` 蓝色高亮 |
| 点击外部关闭 | 浏览器自动（不可靠） | mousedown 监听，可靠 |
| 空结果提示 | 无下拉 | "No matching metrics" 提示 |
| 最大显示 | 不限（可能很慢） | 最多 50 项（可滚动） |

#### 自动刷新策略

- **触发条件**：仅在当前 tab 有活跃内容时才执行刷新
  - Query tab：`metricInput` 非空时自动重新查询
  - RED tab：`redService` 已选择时自动重新加载面板
- **时间范围变更时**：timer 自动重置（依赖 `getParams` 变化触发 callback 重建）
- **UI 反馈**：刷新活跃时显示 `<i class="fa-spin" /> {interval}` 文字

### 验证结果

```
# TypeScript 编译
$ npx tsc --noEmit
(0 errors)

# Vite 生产构建
$ npx vite build
✓ built in 3.31s
  MetricsPage.js  16.02 kB (gzip: 4.93 kB)
```

---

## §11 Sprint 3 实施记录（2026-07-10）

### 改动清单

| # | 文件 | 改动类型 | 说明 |
|---|---|---|---|
| 1 | `components/charts/chartTheme.ts` | **升级** | 新增 `computeSeriesStats()` + `showLegendStats` 参数 + `threshold` markLine |
| 2 | `components/TimeSeriesChart.tsx` | **升级** | 新增 `showLegendStats`/`threshold`/`onChartClick` props；注册 `MarkLineComponent`；图表下方 stats footer |
| 3 | `features/metrics/components/RedDashboard.tsx` | **升级** | 接入 `onTraceClick` 回调；RED 面板 `showLegendStats={true}` |
| 4 | `pages/MetricsPage.tsx` | **修改** | 新增 `handleTraceClick`：点击图表数据点 → ±5min 窗口 → Traces 页面 |

### 新增功能详情

#### Legend 统计表

图表下方显示每个系列的 stats footer（min/max/avg），色块 + 名称 + 统计值：

```
● http_server_request  min:12 min:89 avg:45
● http_server_error      min:0  max:5  avg:1.2
```

右键 legend（`legendPlacement='right'`）时也显示 format 后的 stats。

#### 阈值线

通过 `threshold` prop 传递 `{ value, label, color? }`：

```tsx
<TimeSeriesChart
  threshold={{ value: 200, label: 'SLO 200ms', color: '#ef4444' }}
/>
```

渲染为红色虚线 markLine + 尾部标签。

#### RED → Trace 点击联动

```
用户点击 RED 面板图表数据点
  → onChartClick({ seriesName, time, value })
    → RedDashboard: handleChartClick → onTraceClick({ service, time, panelId })
      → MetricsPage: handleTraceClick → navigate(`/traces?service=xxx&start=T-5m&end=T+5m`)
        → Traces 页面自动打开该 service ±5min 时间窗口
```

### 验证结果

```
# TypeScript 编译
$ npx tsc --noEmit
(0 errors)

# Vite 生产构建
$ npx vite build
✓ built in 3.33s
  TimeSeriesChart.js  13.72 kB (gzip: 5.66 kB)
  MetricsPage.js       16.36 kB (gzip: 5.08 kB)
```
