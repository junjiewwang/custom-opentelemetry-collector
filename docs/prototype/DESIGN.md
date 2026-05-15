# APM Observability Platform — 原型设计文档

> **版本**：v1.4  
> **日期**：2026-05-15  
> **产出物**：`docs/prototype/apm-prototype.html` + `docs/prototype/styles.css`  
> **技术栈**：纯 HTML + CSS + Vanilla JS（独立交互原型，无框架依赖）  
> **v1.1 更新**：新增全局 Scope Bar（多租户四级资源选择器）+ Resource Explorer 页面  
> **v1.2 更新**：新增角色切换器（Platform Admin ↔ Tenant User），同一原型演示双视角差异  
> **v1.3 更新**：业界对标增强 — Apdex 评分 + Error Inbox + Deployment Tracking + Latency Heatmap + Endpoint 分析  
> **v1.4 更新**：管理端重新定位 — Platform Dashboard + Tenant Management + Resource Usage + Global Errors/Alerts + Scope Bar Service 默认行为优化

---

## 1. 设计概述

### 1.1 产品定位

面向 SRE、后端开发、团队管理层的**全栈应用性能观测平台**，覆盖可观测性三大支柱（Metrics / Traces / Logs）+ 基础设施管理 + 动态插桩。

### 1.2 设计原则

| 原则 | 说明 |
|------|------|
| **Data-Dense** | 高信息密度，一屏展示关键决策数据，减少点击层级 |
| **Contextual Navigation** | 任何异常都能 3 步内下钻到根因 |
| **Role-Adaptive** | 管理层看总览趋势，SRE 看告警/实例，开发看 Trace/Instrumentation |
| **Zero-Latency Feedback** | 微交互即时响应，骨架屏/加载动画消除等待焦虑 |
| **Dark by Default** | 深色主题减轻长时间监控的视觉疲劳 |

### 1.3 美学方向

- **风格**：Datadog / Grafana 融合风格，科技感 + 功能主义
- **基调**：深色 `#0d1117` 背景 + 蓝色主色调 `#58a6ff`
- **字体**：IBM Plex Sans（正文） + JetBrains Mono（数据/代码）
- **特色**：状态色发光效果（glow shadows）、数据密集但层次分明

---

## 2. 信息架构

```
APM Platform
├── 🌐 Global Scope Bar (全页面共享)
│   └── Tenant → App → Service(默认选第一个) → Instance(保留 All) 四级级联选择器
│
├── 🛡️ Platform Admin 视角
│   ├── Platform (管理职能)
│   │   ├── Platform Dashboard  ← 跨租户全局概览 + 快速操作入口
│   │   ├── Tenants             ← 租户目录 + 接入状态 + Agent 版本
│   │   ├── Resource Usage      ← 按租户的配额/用量/趋势
│   │   ├── Global Errors       ← 跨租户错误聚合排查
│   │   ├── Global Alerts       ← 跨租户告警统一管理
│   │   └── Resource Explorer   ← 树形资源导航 + 详情面板
│   └── Triage (排查辅助)
│       ├── Service Dashboard   ← 帮租户看服务黄金信号
│       ├── Service Map         ← 帮租户看服务拓扑
│       ├── Traces              ← 帮租户看链路
│       └── Metrics             ← 帮租户看指标
│
└── 👤 Tenant User 视角
    ├── Overview
    │   ├── Service Dashboard
    │   └── Service Map
    ├── Explore
    │   ├── Traces
    │   ├── Errors (Error Inbox)
    │   └── Metrics
    ├── Infrastructure
    │   ├── Instances
    │   ├── Instrumentation
    │   └── Deployments
    ├── Alerts
    │   └── Alert Rules
    └── Account
        ├── API Keys
        ├── Usage & Billing
        └── Team Members
```

---

## 3. 页面设计规范

### 3.1 Service Dashboard（黄金信号总览）

**目标用户**：管理层（快速了解全局健康）+ SRE（发现异常服务）

**核心区块**：
1. **Golden Signals 卡片组**（6 张）：Request Rate / Success Rate / P99 Latency / Error Rate / Active Services / **Apdex Score**（v1.3 新增）
   - Apdex 卡片含 SVG 环形进度图 + 0~1 分值 + 趋势
   - 计算公式：`Apdex = (满意 + 容忍/2) / 总数`
2. **趋势图区**（2 列）：Throughput 时序图 / Latency 分布图
3. **Service Health 表**：服务名 + **Apdex** + 健康评分 + 吞吐 + 延迟 + 错误率 + **Version** + 实例数
   - **Apdex 列**（v1.3）：四级颜色编码 — excellent(≥0.94 绿色) / good(≥0.85 蓝色) / fair(≥0.7 黄色) / poor(<0.7 红色)
   - **Version 列**（v1.3）：版本号 badge，最近发布的带 ↑ 箭头 + 警告色标记

**交互设计**：
- 卡片 hover 显示发光边框（glow effect），暗示可点击下钻
- 表格行 hover 高亮，点击跳转服务详情
- 趋势指标（↑/↓/→）用颜色编码：绿色=好转，红色=恶化，灰色=稳定

### 3.2 Service Map（服务拓扑）

**目标用户**：SRE（理解调用关系） + 开发（排查依赖问题）

**核心区块**：
1. **力导向拓扑图**：节点=服务/中间件，边=调用关系
2. **状态映射**：节点颜色=健康状态，边颜色=流量状态，边粗细=调用量
3. **图例**：颜色含义说明

**交互设计**：
- 节点 hover 放大 + 发光，显示 tooltip（吞吐/延迟/错误率）
- 点击节点显示右侧详情面板
- 缩放/平移/全屏操作栏
- 异常边（红色）自带脉冲动画，引导注意力

### 3.3 Traces（链路追踪）

**目标用户**：开发（定位性能瓶颈） + SRE（排查错误链路）

**核心区块**：
1. **Filter Bar**：标签式过滤（service / status / duration），支持自由文本
2. **Scatter Plot**：延迟散点图，X=时间 Y=延迟，颜色=状态（蓝正常/黄慢/红错误）
3. **Trace 列表**：ID + 根服务 + 操作 + 耗时 + Span 数 + 状态
4. **Trace Timeline**：瀑布流展示 Span 树，水平条表示时间占比

**交互设计**：
- 散点图框选区域放大
- 列表行点击展开 Timeline
- Timeline 中 Span 条 hover 显示详情
- 异常 Span 用红色高亮 + ⚠ 标记

### 3.4 Metrics（指标面板）

**目标用户**：SRE（监控趋势） + 开发（性能分析）

**核心区块**：
1. **Tab 切换**：RED Dashboard / Custom Query / Saved Panels
2. **KPI 卡片**（3 张）：Avg Response Time / Throughput / Error Count
3. **4 宫格图表**：Rate / Errors / Duration / Saturation

**交互设计**：
- 图表支持时间范围拖选
- Custom Query Tab 提供 PromQL 编辑器
- 面板右上角操作按钮：全屏/分享/编辑

### 3.5 Instances（实例健康）

**目标用户**：SRE（容量管理） + 运维（故障定位）

**核心区块**：
1. **搜索栏**：实时过滤 + 状态快捷按钮（Online/Offline 计数）
2. **实例表格**：实例名 + 服务 + 状态 + CPU/Memory（进度条） + GC Pause + Uptime + Agent 版本

**交互设计**：
- CPU/Memory 用 mini 进度条 + 颜色渐变（绿→黄→红）
- Offline 实例行灰化处理
- 点击行展开详情（Arthas 终端/任务列表）

### 3.6 Instrumentation（动态插桩）

**目标用户**：开发（动态注入探针） + SRE（监控规则状态）

**核心区块**：
1. **状态概览**：Active / Paused / Failed 计数 + 创建按钮
2. **左右分栏**：左=规则列表，右=选中规则详情
3. **详情面板**：规则配置 + Target Status 列表（每个实例的应用状态）

**交互设计**：
- 规则列表选中行高亮
- Target 状态用圆点 + badge 双重编码
- 操作按钮组：暂停/恢复/删除
- **New Rule 按钮**（v1.3 增强）：点击弹出 3 步 Modal 表单

**New Rule Modal（创建规则弹窗）**：

> v1.3 新增

| 步骤 | 内容 | 字段 |
|------|------|------|
| Step 1: Basic Info | 基础信息 | Rule Name / Description / Rule Type（Trace/Metric/Log 分段控件）/ Target Service（下拉） |
| Step 2: Target | 目标定义 | Class Pattern（等宽输入）/ Method Pattern（等宽输入）/ Include Overloads / Interface Match + 匹配预览 |
| Step 3: Capture | 采集配置 | Capture Options（4 个复选框）/ Sampling Rate / Desired State（Active/Paused 开关）+ Rule Summary 预览卡 |

**Modal 交互规范**：
- 底部 `Back / Cancel / Next` 三按钮，最后一步变为 `Create Rule`
- 顶部步骤条显示当前进度（1/2/3），已完成步骤变绿 ✓
- Escape 键或点击遮罩关闭
- 创建成功后显示 Toast 提示 + 新规则出现在列表顶部
- Class/Method 输入实时预览"将匹配 N 个实例"

### 3.7 Alerts（告警管理）

**目标用户**：SRE（响应告警） + 管理层（了解系统风险）

**核心区块**：
1. **Tab 切换**：Active Alerts (带计数) / Alert Rules / History
2. **告警卡片**：左侧彩色边框（严重程度）+ 告警标题 + 描述 + 持续时间 + 操作按钮
3. **严重等级映射**：Critical=红色 / Warning=黄色 / Info=蓝色

**交互设计**：
- Critical 告警的圆点有脉冲动画
- Acknowledge 按钮一键确认
- 点击展开详情（指标图 + 影响范围 + 建议操作）

### 3.8 Resource Explorer（资源浏览器）

> **v1.1 新增**

**目标用户**：SRE（资源全景） + 管理层（资产盘点） + 开发（快速定位实例）

**设计动机**：
- 解决 App/Service/Instance 三个页面割裂、无下钻、跨页导航断链的痛点
- 提供一屏内浏览和管理全部层级的统一入口

**核心区块**：
1. **左侧树形导航**（320px 固定宽度）：
   - 搜索栏：实时过滤 App/Service/Instance
   - 统计行：`N Apps · N Services · N Instances`
   - 三级树：App (蓝色 cube) → Service (绿色 cogs) → Instance (状态圆点)
   - 每级缩进 16px，叶节点显示 IP 地址
2. **右侧详情面板**：
   - 面包屑导航（可点击回溯上级）
   - 四宫格关键指标（CPU/Memory/GC Pause/Uptime + mini 进度条）
   - Tab 切换：Overview / Metrics / Tasks / Arthas / Instrumentation
   - 快捷操作按钮组

**交互设计**：
- 树节点 chevron 点击展开/折叠子节点
- 节点行点击选中（蓝色左边框高亮），右侧面板随之更新
- 搜索过滤时自动展开匹配节点的父级
- 状态圆点实时反映健康/降级/离线状态

### 3.9 Global Scope Bar（全局资源选择器）

> **v1.1 新增**

**目标用户**：所有角色

**设计动机**：
- 将 `Tenant → App → Service → Instance` 四级资源体系作为全局上下文控制器
- 所有页面（Dashboard / Traces / Metrics / Instances / Instrumentation / Alerts）自动响应 scope 变化
- 参考 Datadog 的 `env → service → version` scope 模式，为多租户场景提供天然隔离

**核心区块**：
1. **双层 Header 结构**：
   - **第一层（Scope Bar，40px）**：`SCOPE` 标签 + 四级级联 Picker + Clear 按钮
   - **第二层（Main Header，48px）**：面包屑 + Scope Context Tag + 时间范围选择器
2. **四级级联 Picker**：
   - 每个 Picker 包含图标 + 当前值 + 下拉箭头
   - Picker 之间用 `>` 分隔符连接
   - **Service 默认行为**（v1.4）：没有 "All Services" 选项，默认选中第一个具体服务
   - **Instance 层**保留 "All Instances" 选项（同一服务下看所有实例的聚合是合理的）
   - App 层仍保留 "All Apps" 选项

**交互设计**：
- 点击 Picker 展开下拉面板，面板内含搜索框 + 选项列表
- 选中项高亮（蓝色背景 + ✓ 图标），右侧显示 meta 信息（如 `3 svc · 12 inst`）
- 外部点击自动关闭面板
- 选择后自动更新面包屑中的 Scope Context Tag
- Clear 按钮一键重置所有级别为 "All"
- 子级 Picker 联动：选择 App 后，Service 下拉仅显示该 App 的服务

**多租户扩展**：
- Tenant 层在 App 之上，作为最高级别的资源隔离维度
- 切换 Tenant 后，所有下级 Picker 自动重置并刷新数据
- 权限控制：用户只能看到被授权的 Tenant

### 3.10 Role Switcher（角色切换器）

> **v1.2 新增**

**目标用户**：产品演示 / 设计评审场景

**设计动机**：
- 同一原型中演示"运营端"与"租户端"两种视角差异，无需维护两套页面
- 让 Stakeholder 直观理解权限分级对 UI 的影响
- 为未来实际产品的 RBAC（基于角色的访问控制）提供 UI 参考

**双视角对比**：

| 维度 | Platform Admin | Tenant User |
|------|---------------|-------------|
| 默认页面 | Platform Dashboard | Service Dashboard |
| Scope Bar | 四级 `Tenant → App → Service → Instance` | 三级 `App → Service → Instance`（Tenant 隐式） |
| Sidebar Logo | 平台通用 Logo + "APM Platform" | 租户品牌色 Logo + 租户名 + App 名 |
| 导航菜单 | **Platform 组**（6 项管理职能）+ **Triage 组**（4 项排查辅助） | **Overview** + **Explore** + **Infrastructure** + **Alerts** + **Account** |
| 管理端不显示 | — | ~~Alert Rules~~ / ~~Errors(Inbox)~~ / ~~Instances~~ / ~~Instrumentation~~ / ~~Deployments~~ 这些归租户 |
| 底部设置 | Settings | 用户头像 + 姓名 + 角色 badge |
| Context Tag | `Acme Corp / E-Commerce Platform / order-service` | `Acme Corp / order-service`（固定租户） |

**交互设计**：
- **Toggle 滑块**：Scope Bar 右侧，`View as [ Platform | Tenant ]`
- 滑块有滑动动画（250ms ease），active 选项文字变白
- 切换时触发全局状态变更：
  1. Tenant Picker + 分隔符隐藏/显示
  2. Sidebar Header 切换
  3. `data-admin-only` 导航组隐藏/显示（Platform + Triage）
  4. `data-tenant-only` 导航组显示/隐藏（Overview + Explore + Infrastructure + Alerts + Account）
  5. 底部 Settings/Profile 切换
  6. 面包屑 Context Tag 内容更新
- 若 Tenant 模式下当前页是 admin-only 页，自动重定向到 Service Dashboard
- 若切换到 Admin 模式，自动跳转到 Platform Dashboard

**管理端导航精简决策**（v1.4）：

管理端**不显示**租户侧的以下页面，原因：

| 移除的页面 | 原因 |
|-----------|------|
| Alert Rules | 被 Global Alerts 替代，单租户告警是租户自己的事 |
| Errors (Error Inbox) | 被 Global Errors 替代，管理端用跨租户聚合视角 |
| Instances | 已在 Resource Explorer 中可查看 |
| Instrumentation | 动态插桩是开发/SRE 的操作，管理员不应改租户规则 |
| Deployments | 发布追踪是租户侧的 DevOps 职能 |

管理端**保留**的排查辅助页面（Triage 组）：

| 保留的页面 | 原因 |
|-----------|------|
| Service Dashboard | 帮租户排查时需要看具体服务的黄金信号 |
| Service Map | 帮助理解服务依赖关系 |
| Traces | 排查问题时必须能看链路 |
| Metrics | 排查问题时必须能看指标 |

### 3.11 Error Inbox（错误聚合收件箱）

> **v1.3 新增** · 参考 New Relic Error Inbox

**目标用户**：开发（快速定位高频异常）+ SRE（错误分类管理）

**设计动机**：
- 传统错误列表按时间排序，高频异常淹没在海量事件中
- Error Inbox 按 `exception class + stack trace fingerprint` 自动聚合，降噪 90%+
- 工作流状态（New → Triaged → Resolved）让错误处理可追踪

**核心区块**：
1. **状态统计栏**：Unresolved（红）/ Triaged（黄）/ Resolved（绿）计数 badge
2. **错误组卡片列表**：
   - 左侧状态圆点 + 异常类名（JetBrains Mono 等宽）
   - 堆栈位置摘要（`ClassName.method(File.java:line)`）
   - 元信息行：所属服务 / 影响用户数 / 首次出现 / 最后出现
   - **Sparkline 趋势**（ASCII block chars `▁▂▃▅▇█`）：一目了然的频率趋势
   - 右侧：出现次数 badge + 工作流状态 badge
3. **已解决组**：降低透明度 + 删除线标记

**交互设计**：
- 卡片 hover 边框高亮 + 阴影，暗示可展开详情
- 点击卡片展开完整堆栈 + 影响的 Trace ID 列表
- 状态流转按钮：New → Triaged → Resolved
- 左侧 border 颜色编码：红色=未解决/高频，黄色=已分类，绿色=已解决

**Apdex 色阶映射**：

| 范围 | 等级 | CSS 类 | 颜色 |
|------|------|--------|------|
| ≥ 0.94 | Excellent | `.apdex-excellent` | 绿 `#3fb950` |
| 0.85–0.93 | Good | `.apdex-good` | 蓝 `#58a6ff` |
| 0.70–0.84 | Fair | `.apdex-fair` | 黄 `#d29922` |
| < 0.70 | Poor | `.apdex-poor` | 红 `#f85149` |

### 3.12 Deployments（发布追踪）

> **v1.3 新增** · 参考 Datadog Deployment Tracking

**目标用户**：SRE（关联性能劣化与版本发布）+ DevOps（发布质量监控）

**设计动机**：
- 性能劣化 80% 是由代码变更引起的
- 在时序图上叠加版本标记线，直观定位"从哪个版本开始出问题"
- 发布前后 Δ 指标对比，量化发布影响

**核心区块**：
1. **状态统计栏**：今日发布数 / Healthy 数 / Degraded 数
2. **Deployment Timeline**：
   - 背景：吞吐量柱状图（时序）
   - 叠加：版本标记线（垂直虚线 + 圆点 + 版本号标签）
   - 颜色：绿色=正常发布，红色=引发劣化的发布
3. **Recent Deployments 表格**：
   - Service / Version（旧→新）/ Status / Deployed 时间
   - **Δ Latency**（+/-ms 百分比）/ **Δ Error Rate**（+/-%）
   - Rollback 按钮（仅 Degraded 状态高亮可用）

**交互设计**：
- 标记线 hover 放大圆点 + tooltip 显示版本详情
- 红色标记线有微弱脉冲动画引导注意力
- Rollback 按钮点击后弹出确认对话框
- 表格行按状态排序：Degraded → Monitoring → Healthy

### 3.13 Latency Heatmap（延迟热力图）

> **v1.3 新增** · 参考 Grafana Tempo Heatmap + SkyWalking

**目标用户**：SRE（发现延迟模式）+ 开发（定位间歇性慢请求）

**设计动机**：
- 散点图在高流量时重叠严重，看不出密度分布
- 热力图用颜色编码密度，X=时间 Y=延迟分桶 Color=请求数量
- 能清晰展示双峰分布、长尾效应、突发延迟spike

**核心区块**：
1. **SVG 栅格热力图**：
   - X 轴：时间窗口（按当前 time range 等分）
   - Y 轴：延迟分桶（10ms / 50ms / 200ms / 500ms / 1s / 2s+）
   - 颜色：绿色(低延迟高密度) → 黄色(中延迟) → 红色(高延迟/异常)
2. **Y 轴标签**（固定左侧）
3. **色阶图例**：Low → High

**交互设计**：
- 单元格 hover 显示 outline 高亮 + tooltip（时间段 / 延迟范围 / 请求数）
- 点击单元格跳转到对应时间+延迟范围的 Trace 列表

### 3.14 Endpoint 分析（接口级性能视图）

> **v1.3 新增** · 参考 ARMS Endpoint 分析 + SkyWalking Endpoint

**目标用户**：开发（定位慢接口）+ SRE（API 性能基线管理）

**设计动机**：
- 服务级指标是聚合值，无法定位具体哪个接口慢
- Endpoint 级分析精确到 `Method + Path`，配合 Apdex 量化接口健康度
- Top N Slow Endpoints 排行帮助优先级排序

**核心区块**：
1. **Metrics 页 Endpoints Tab**（新增 Tab）
2. **Top Endpoints 表格**：
   - Method badge（GET 绿 / POST 蓝 / PUT 黄 / DELETE 红）
   - Endpoint Path（等宽字体，链接样式可点击）
   - 所属 Service
   - Throughput / P50 / P95 / P99 / Error Rate / Apdex
   - 按 P99 降序排列，最慢接口在最上面

**交互设计**：
- Method badge 颜色区分直观（RESTful 语义色）
- 点击 Endpoint Path 跳转该接口的详细 Trace 列表
- 表头可排序：点击 P99/Error Rate/Apdex 切换排序维度

### 3.15 Platform Dashboard（管理端总览）

> **v1.4 新增** · 管理端默认首页

**目标用户**：平台管理员（全局健康一览 + 快速定位问题租户）

**设计动机**：
- 管理端的核心职能是①租户管理 ②资源使用查看 ③快速帮助租户排查问题
- 之前管理端和租户端几乎一样，缺少独立管理职能
- Platform Dashboard 作为管理端专属入口，聚合跨租户全局数据

**核心区块**：
1. **平台 KPI 卡片组**（6 张）：Active Tenants / Total Services / Total Instances / Data Ingested / Active Alerts / Platform Health
2. **Tenant Health Overview 表格**：租户名 + Apps + Services + Instances + Health(Apdex) + Data Ingested + Active Alerts + Status
3. **Quick Action 双栏**：
   - 左：Recent Cross-Tenant Errors（最新跨租户错误，带 "View All →" 跳转）
   - 右：Resource Utilization（Span/Metric/Log/Alert 四项进度条）

**交互设计**：
- Tenant Health 表格行点击可跳转 Tenant 详情
- Quick Action 卡片的 "View All →" / "Details →" 按钮联动到对应管理页
- 切换到 Tenant 模式时自动重定向到 Service Dashboard

### 3.16 Tenants（租户管理）

> **v1.4 新增** · 管理端专属

**目标用户**：平台管理员（租户接入管理 + 状态监控）

**核心区块**：
1. **状态统计栏**（3 张 stat-card）：Total Tenants / Connected / Degraded
2. **Tenant Directory 表格**：
   - 租户头像+名称+ID / Plan(Enterprise/Standard badge) / Apps / Services / Instances
   - Agent Version（过期版本黄色标记 + ↑ 箭头）/ Onboarded 日期 / Status
   - Actions 列：👁 View as tenant / 🔍 Investigate / ⚙ Settings

**交互设计**：
- "View as tenant" 按钮点击后触发角色切换到 Tenant 模式
- Degraded 租户行的 Investigate 按钮用黄色高亮
- "Add Tenant" 按钮用蓝色主色调

### 3.17 Resource Usage（资源使用）

> **v1.4 新增** · 管理端专属

**目标用户**：平台管理员（容量规划 + 成本控制 + 配额管理）

**核心区块**：
1. **全局资源 KPI**（4 张）：Spans/Day / Metric Series / Storage Used / Quota Usage
2. **Per-Tenant Resource Usage 表格**：
   - 租户名 / Spans/Day / Metrics Series / Storage / Quota / Quota Usage(进度条+百分比) / Trend(7d sparkline)
   - 进度条颜色：绿(<50%) / 黄(50-75%) / 红(>75%)
3. **Storage Growth Trend 图表**：时序柱状图

**交互设计**：
- 配额即将超标的租户自动标红
- 点击租户行展开详细的用量明细
- Sparkline 趋势一眼看出增长方向

### 3.18 Global Errors（跨租户错误排查）

> **v1.4 新增** · 管理端专属

**目标用户**：平台管理员（快速帮助租户排查问题）

**设计动机**：
- 管理端需要一个全局视角，看到所有租户的错误
- 能快速定位是哪个租户的哪个服务出了问题
- 一键跳转到对应租户的 Error Inbox 继续深入排查

**核心区块**：
1. **全局错误统计**（3 张）：Total Error Groups / Affected Tenants / Avg Resolution
2. **Cross-Tenant Error Groups 表格**：
   - 状态圆点 + Error Name(代码+堆栈位置) + Tenant + Service + Count + Trend(sparkline)
   - First Seen / Status(New/Triaged/Resolved) / Action(跳转按钮)
   - Resolved 行降低透明度 + 删除线

**交互设计**：
- ↗ 跳转按钮一键进入对应租户的 Errors 页面，携带筛选上下文
- 按 Count 降序排列，最高频错误在最上面
- Status badge 颜色编码与 Error Inbox 一致

### 3.19 Global Alerts（跨租户告警管理）

> **v1.4 新增** · 管理端专属

**目标用户**：平台管理员（全局告警监控 + 快速响应）

**核心区块**：
1. **告警统计**（3 张）：Active Alerts / Avg Response Time / Resolved(24h)
2. **Cross-Tenant Alerts 表格**：
   - 状态圆点(critical 带脉冲动画) + Alert 标题+描述 + Tenant + Service
   - Severity(Critical/Warning badge) + Duration + Actions(Ack/跳转)
   - 平台级告警（如 Storage quota、Agent outdated）Service 列显示 "Platform" badge

**交互设计**：
- Critical 告警的圆点有脉冲动画
- Ack 按钮一键确认
- ↗ 按钮跳转到对应租户的服务详情
- 告警按严重程度排序：Critical → Warning

---

## 4. 色彩体系

### 4.1 基础色板

| Token | 值 | 用途 |
|-------|------|------|
| `--bg-primary` | `#0d1117` | 页面背景 |
| `--bg-secondary` | `#161b22` | 卡片/侧边栏背景 |
| `--bg-tertiary` | `#21262d` | 输入框/进度条背景 |
| `--border-default` | `#30363d` | 默认边框 |
| `--text-primary` | `#e6edf3` | 主要文字 |
| `--text-secondary` | `#8b949e` | 次要文字 |
| `--text-tertiary` | `#6e7681` | 辅助文字 |

### 4.2 功能色

| Token | 值 | 用途 |
|-------|------|------|
| `--accent-blue` | `#58a6ff` | 主色调/链接/信息 |
| `--accent-green` | `#3fb950` | 成功/健康 |
| `--accent-yellow` | `#d29922` | 警告/降级 |
| `--accent-red` | `#f85149` | 错误/严重 |
| `--accent-purple` | `#bc8cff` | 辅助信息 |

### 4.3 图表色序

8 色渐进色序用于多系列图表：Blue → Green → Yellow → Purple → Pink → Cyan → Orange → Red

---

## 5. 排版规范

| 层级 | 字体 | 大小 | 重量 | 用途 |
|------|------|------|------|------|
| H1 | IBM Plex Sans | 1.5rem | 700 | 页面标题 |
| H2 | IBM Plex Sans | 1.1rem | 600 | 区块标题 |
| Body | IBM Plex Sans | 0.85rem | 400 | 正文 |
| Caption | IBM Plex Sans | 0.75rem | 400 | 标签/辅助 |
| Data | JetBrains Mono | 0.85rem | 500 | 数值/代码/ID |
| Stat | JetBrains Mono | 1.8rem | 700 | 大数字指标 |

---

## 6. 组件设计清单

| 组件 | 说明 | 变体 |
|------|------|------|
| Stat Card | 指标卡片 | 带趋势/带图标/带 sparkline/带 Apdex 环 |
| Badge | 状态标签 | healthy/warning/critical/info/neutral |
| Dot | 状态圆点 | 4 种颜色 + 脉冲动画(critical) |
| Card | 内容容器 | 默认/带 header/带 border-left |
| Chart Area | 图表占位 | 柱状图/散点图/时序线/热力图 |
| Filter Bar | 搜索过滤 | 标签式/自由文本 |
| Table | 数据表格 | hover 高亮/可选中 |
| Nav Item | 导航项 | active/带 badge |
| Button | 操作按钮 | primary/ghost |
| Topology Node | 拓扑节点 | 不同颜色/hover 放大 |
| Span Bar | Trace 时间条 | 不同颜色表示不同服务 |
| Progress Bar | 资源进度条 | 颜色渐变(绿→黄→红) |
| Tab | 页签切换 | 默认/带计数 badge |
| Alert Card | 告警卡片 | critical/warning/info |
| Scope Picker | 级联选择器 | 带搜索/带下拉/四级联动 |
| Scope Context Tag | 上下文标签 | 蓝色胶囊标签，显示当前 scope |
| Tree Node | 树形导航节点 | app/service/instance 三级缩进 |
| Role Switcher | 角色视角切换 | Platform Admin / Tenant User，滑块式 toggle |
| Apdex Score | Apdex 分值显示 | 四级颜色编码（v1.3） |
| Error Group Card | 错误聚合卡片 | 含 sparkline + 状态流转 + 计数 badge（v1.3） |
| Error Count Badge | 错误计数徽章 | 红色/黄色/绿色变体（v1.3） |
| Deploy Marker | 发布标记线 | 叠加在时序图上，success/danger 变体（v1.3） |
| Endpoint Method | HTTP 方法标签 | GET 绿/POST 蓝/PUT 黄/DELETE 红（v1.3） |
| Heatmap | 热力图 | SVG 栅格 + 颜色密度映射（v1.3） |
| Modal | 弹窗容器 | 含遮罩 + 步骤条 + 分步表单 + 底部操作栏（v1.3） |
| Form Input | 表单输入 | 默认 / 等宽(mono) / focus 发光（v1.3） |
| Segmented Control | 分段选择器 | 多选一，active 态蓝色高亮（v1.3） |
| Checkbox Group | 复选框组 | checked 态蓝色填充 + ✓（v1.3） |
| Toggle Switch | 开关控件 | On=绿色/Off=灰色（v1.3） |
| Toast | 轻提示 | 底部右侧，3s 自动消失（v1.3） |
| Admin Nav Group | 管理端导航组 | Platform 专属导航，蓝色标签高亮（v1.4） |
| Tenant Row | 租户表格行 | 头像+名称+ID + Plan badge + Action 按钮组（v1.4） |
| Usage Bar | 用量进度条 | 行内进度条 + 百分比，三级颜色编码（v1.4） |
| Cross-Tenant Error Row | 跨租户错误行 | 错误+堆栈 + 租户 + 服务 + 跳转按钮（v1.4） |
| Cross-Tenant Alert Row | 跨租户告警行 | 告警详情 + 租户 + 严重等级 + Ack 按钮（v1.4） |

---

## 7. 交互规范

### 7.1 动效时间

| 场景 | 时长 | 缓动 |
|------|------|------|
| 页面切换 | 300ms | ease-out |
| Hover 反馈 | 150ms | ease |
| 抽屉滑入 | 250ms | ease-out |
| 数据加载 | 骨架屏闪烁 | 1.5s loop |
| 告警脉冲 | 2s | infinite pulse |

### 7.2 导航下钻路径

```
Dashboard → 点击异常服务行 → Service Detail
Service Detail → 点击 Trace → Trace Timeline
Trace Timeline → 点击异常 Span → Span Detail + 关联 Metrics
Alert 触发 → 点击告警 → 关联 Service + Trace
```

---

## 8. 使用说明

1. 用浏览器打开 `docs/prototype/apm-prototype.html`
2. **顶部 Scope Bar**：点击四级 Picker 切换 Tenant/App/Service/Instance 上下文
3. **角色切换**：点击右上角 `View as` 切换器，在 `Platform` 和 `Tenant` 视角间切换
   - **Platform 模式**：全局管理视角，可跨租户切换，显示 Resource Explorer
   - **Tenant 模式**：租户端视角，隐藏 Tenant Picker，显示品牌 Logo + 用户信息 + 账户管理菜单
4. 点击左侧导航切换页面，所有页面数据受 Scope Bar 过滤
5. **Resource Explorer**：左侧树形导航浏览资源层级，点击节点查看右侧详情
6. 图表区域使用随机数据模拟，展示视觉效果
7. 所有交互仅为前端演示，无真实 API 调用

---

## 9. 后续迭代方向

| 优先级 | 功能 | 说明 | 状态 |
|--------|------|------|------|
| P0 | ~~Resource Explorer~~ | 树形资源导航 + 详情面板 | ✅ v1.1 |
| P0 | ~~Global Scope Bar~~ | 四级级联资源选择器，全页面共享上下文 | ✅ v1.1 |
| P0 | ~~多租户支持~~ | Tenant 层作为最高隔离维度 | ✅ v1.1 |
| P0 | ~~角色切换器~~ | Platform Admin ↔ Tenant User 双视角演示 | ✅ v1.2 |
| P0 | ~~Apdex 评分~~ | 全局 + 服务级 Apdex 健康分，四级颜色编码 | ✅ v1.3 |
| P0 | ~~Error Inbox~~ | 错误聚合收件箱，按异常类+堆栈聚合，工作流状态管理 | ✅ v1.3 |
| P0 | ~~Deployment Tracking~~ | 发布追踪，版本标记线 + Δ 指标对比 + Rollback | ✅ v1.3 |
| P1 | ~~Latency Heatmap~~ | X=时间 Y=延迟分桶 Color=密度，替代散点图 | ✅ v1.3 |
| P1 | ~~Endpoint 分析~~ | 接口级 Method+Path+P50/P95/P99/Apdex 排行 | ✅ v1.3 |
| P0 | ~~Platform Dashboard~~ | 管理端专属总览：跨租户 KPI + 快速操作入口 | ✅ v1.4 |
| P0 | ~~Tenant Management~~ | 租户目录 + 接入状态 + Agent 版本 + 一键切换视角 | ✅ v1.4 |
| P0 | ~~Resource Usage~~ | 按租户配额/用量/趋势 + Storage Growth 图表 | ✅ v1.4 |
| P0 | ~~Global Errors~~ | 跨租户错误聚合排查 + 一键跳转 | ✅ v1.4 |
| P0 | ~~Global Alerts~~ | 跨租户告警统一管理 + 严重等级排序 | ✅ v1.4 |
| P0 | ~~Scope Bar 优化~~ | Service 默认选第一个具体服务，去掉 All Services | ✅ v1.4 |
| P0 | Logs 查看器 | 可观测三支柱补齐，支持 Trace ↔ Log 关联 | 待实施 |
| P1 | Scope Bar 联动 | 选择 App 后自动过滤 Service/Instance 下拉选项 | 待实施 |
| P1 | Alert Rule 编辑器 | 支持 PromQL 条件 + 通知渠道配置 | 待实施 |
| P1 | Service Detail 页 | 单个服务的深度指标视图 | 待实施 |
| P2 | SLO/SLI + Error Budget | 服务等级目标 + 错误预算 + Burn Rate | 待实施 |
| P2 | Custom Dashboard | 拖拽式自定义面板布局 | 待实施 |
| P2 | AI Anomaly Detection | Watchdog 式异常自动检测 + 根因分析 | 待实施 |
| P3 | Profiling 视图 | 持续性能分析（CPU/Memory Flame Graph） | 待实施 |
| P3 | Service Catalog | 服务目录 + 所有者 + SLO + 依赖关系元数据 | 待实施 |

---

*本文档随原型迭代持续更新。*
