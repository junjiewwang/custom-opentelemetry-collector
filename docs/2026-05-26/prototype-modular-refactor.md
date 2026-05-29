# APM 原型文件模块化重构

## 需求背景

原始 `apm-prototype.html` 单文件已膨胀到 3289 行，包含所有页面 HTML、全部 JS 逻辑和组件代码，难以维护和扩展。需要按前端设计原则进行模块化拆分。

## 设计原则

- **高内聚**：每个 JS 模块只负责一个明确职责（一个视图 = 一个 JS 文件）
- **低耦合**：组件间通过 `window` 全局函数接口通信
- **可扩展**：新增页面只需新建一个 view JS 文件 + `ViewRouter.register(id, {render, init})`
- **数据与视图分离**：mock 数据集中在 `mock-data.js`，便于后续替换为真实 API
- **file:// 兼容**：不依赖 ES Modules / fetch，可以直接双击打开 HTML 文件演示

## 技术方案

### ViewRouter 模式（最终方案）

放弃 ES Modules 方案（受 `file://` CORS 限制），改用 **ViewRouter + 传统 `<script src>` 加载**：

- **ViewRouter**：每个视图通过 `ViewRouter.register(id, { render(container), init?(container), destroy?() })` 注册
- **IIFE 模块模式**：各组件用 `var X = (function() { ... return {...}; })();` 封装
- **全局函数暴露**：组件内部通过 `window.functionName = fn` 暴露给 HTML 内联 onclick 使用
- **生命周期**：navigate 时依次调用 旧视图 `destroy()` → 新视图 `render(container)` → 新视图 `init(container)`

## 文件结构

```
docs/prototype/
├── index.html              # 主骨架（~205行）：sidebar + scope-bar + header + #page-container
├── styles.css              # 所有 CSS（独立，不变）
├── js/
│   ├── mock-data.js        # Mock 数据集中管理（Trace span 数据等）
│   ├── router.js           # ViewRouter 核心（register/navigate/destroy 生命周期）
│   ├── components/
│   │   ├── charts.js       # 图表生成工具（generateBars, initDashboardCharts 等）
│   │   ├── scope-bar.js    # Scope Bar 切换组件（dropdown + role switcher）
│   │   └── trace-drawer.js # Trace Drawer 交互逻辑（open/close/selectSpan）
│   ├── views/
│   │   ├── platform-dashboard.js  # Platform Dashboard（Admin）
│   │   ├── tenants.js             # Tenants 管理（Admin）
│   │   ├── resource-usage.js      # Resource Usage（Admin）
│   │   ├── global-errors.js       # Global Errors（Admin）
│   │   ├── global-alerts.js       # Global Alerts（Admin）
│   │   ├── dashboard.js           # Service Dashboard（Golden Signals）
│   │   ├── topology.js            # Service Map（SVG 拓扑图）
│   │   ├── traces.js              # Traces + Trace Drawer 模板
│   │   ├── metrics.js             # Metrics RED Dashboard
│   │   ├── instances.js           # Instances 列表
│   │   ├── instrumentation.js     # Instrumentation + Modal Wizard
│   │   ├── alerts.js              # Alert Rules
│   │   ├── errors.js              # Error Inbox
│   │   └── resources.js           # Resource Explorer 树形导航
│   └── app.js              # 入口：bindNavigation + bindTabs + ScopeBar.init + applyRole
└── apm-prototype.html      # 旧文件（保留作为备份参考）
```

## 模块职责

| 模块 | 职责 | 暴露全局 API |
|------|------|-------------|
| `router.js` | ViewRouter 核心 | `ViewRouter.register/navigate/getCurrentView` |
| `mock-data.js` | Trace span mock 数据 | `MockData.spanData` |
| `charts.js` | 图表生成 | `Charts.generateBars/initDashboardCharts/initMetricsCharts` |
| `scope-bar.js` | Scope 下拉 + 角色切换 | `ScopeBar.init/applyRole/toggleRole` + `window.toggleRole/toggleScopeDropdown` |
| `trace-drawer.js` | Trace Drawer 全部交互 | `TraceDrawer.open/close/selectSpan` + `window.openTraceDrawer/closeTraceDrawer/selectSpanV2` |
| `app.js` | 启动入口，全局事件 | `window.navigateToPage/switchToTenantView` |
| `views/*.js` | 各页面视图 | 通过 ViewRouter.register 注册，部分暴露 window 函数 |

## 初始化流程

1. 浏览器加载 HTML → 解析 `<script>` 标签（按顺序执行）
2. 各 view 文件执行时调用 `ViewRouter.register(id, {...})` 注册自身
3. `app.js` 最后执行：
   - `bindNavigation()` — 绑定 sidebar `.nav-item[data-page]` 点击事件
   - `bindTabSwitching()` — 用事件委托处理动态渲染的 `.tabs > .tab` 点击
   - `ScopeBar.init()` — 绑定 dropdown、role-toggle、clear 按钮事件
   - `ScopeBar.applyRole('admin')` — 同步初始 DOM 状态 → 触发 `navigateToPage('platform-dashboard')`

## Bug 修复记录（2026-05-26）

### 问题 1：Scope Bar 下拉菜单点击无效
- **原因**：`onclick` 在父级 `scope-picker` 上，点击 dropdown 内部元素时事件冒泡到父级触发 toggle，把刚打开的菜单又关了
- **修复**：
  - 移除所有 inline `onclick`，改为 `ScopeBar.init()` 中通过 `addEventListener` 绑定
  - 在 picker 的 click handler 中检查 `if (e.target.closest('.scope-dropdown')) return;`
  - 为 `.scope-dropdown` 添加独立的 `e.stopPropagation()` 事件
  - dropdown-item 选中后正确关闭 dropdown

### 问题 2：Admin/Tenant 侧边栏导航混在一起
- **原因**：`app.js` 初始化时没有调用 `ScopeBar.applyRole()` 同步 DOM 状态
- **修复**：
  - `init()` 中显式调用 `ScopeBar.applyRole(initialRole)` 
  - `applyRole` 增加 null 检查，避免某个 DOM 元素不存在时 JS 报错阻断后续逻辑
  - 角色切换时调用 `window.navigateToPage()` 确保侧边栏 active 状态同步

### 问题 3：Tab 切换和按钮交互无响应
- **原因**：`bindTabSwitching()` 被定义但从未调用；且原实现是 `querySelectorAll('.tabs')` 在初始化时绑定，但 tabs 是动态渲染的
- **修复**：
  - 改为事件委托模式：在 `#page-container` 上用 `addEventListener('click')` + `e.target.closest('.tab')` 处理
  - 在 `init()` 中实际调用 `bindTabSwitching()`

### 问题 4：Trace Drawer 宽度过窄
- **修复**：CSS 中 `.trace-drawer` 宽度从 `62vw / max 1100px / min 680px` 调整为 `82vw / max 1600px / min 800px`

### 问题 5：Traces 页面初始无详情
- **修复**：为 traces 视图添加 `init()` 钩子，自动选中第一个 span 渲染详情

## 使用方式

```bash
# 直接双击打开（推荐用于团队演示）
open docs/prototype/index.html

# 或用本地 HTTP 服务器
cd docs/prototype
python3 -m http.server 8080
# 打开 http://localhost:8080/index.html
```

## 实施状态

- [x] 创建 `index.html` 主骨架（ViewRouter 模式）
- [x] 创建 4 个组件 JS（router, mock-data, charts, scope-bar, trace-drawer）
- [x] 创建 14 个视图 JS 文件
- [x] 创建 `app.js` 入口
- [x] 修复 Scope Bar 下拉菜单事件绑定
- [x] 修复 Admin/Tenant 角色切换联动
- [x] 修复 Tab 通用切换（事件委托）
- [x] Trace Drawer 宽度调整（82vw）
- [x] Traces 视图添加 init 钩子
- [ ] 清理旧的 ES Modules 版本文件（`docs/prototype/pages/` 目录）
- [ ] Account 页面占位（API Keys / Usage & Billing / Team Members 未实现）

## 遗留问题

1. **旧文件保留**：`apm-prototype.html` 保留作为备份参考；`pages/` 目录（旧 ES Modules 方案）可删除
2. **Account 页面**：sidebar 中的 API Keys / Usage & Billing / Team Members 尚未注册对应 view（点击不会有反应）
3. **演示专用**：本原型为团队演示用途，数据为 mock 静态数据，无后端交互
