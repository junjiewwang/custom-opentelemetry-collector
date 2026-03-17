# 修复 Services 页面双侧边栏问题

## 需求描述

进入 Services 页面时，出现了两个侧边栏导航并排显示：
- **左边**：旧版 Alpine.js 前端 (`webui/index.html`) 的侧边栏，在 iframe 内渲染
- **右边**：新的 React 前端 (`webui-react/Sidebar.tsx`) 的侧边栏

## 根本原因

Services 页面属于旧页面，在 `App.tsx` 中通过 `<LegacyPage view="services" />` 嵌入。
`LegacyPage.tsx` 使用 iframe 加载旧版页面 `/legacy/?view=services&apiKey=xxx`。

但旧版 `index.html` 和 `app.js` 存在以下问题：
1. **没有 iframe 嵌入检测**：旧版页面不会检测自己是否在 iframe 中运行，始终渲染完整布局
2. **没有处理 URL 参数**：`?view=services&apiKey=xxx` 没有被解析，不会自动跳过登录
3. **没有 postMessage 监听**：LegacyPage.tsx 发送了 `OTEL_ADMIN_NAVIGATE` 消息，但旧版 app.js 没有监听它

## 解决方案

### 1. 修改 `webui/js/app.js`

- 添加 `embeddedMode` 和 `embeddedView` 状态变量
- 在 `init()` 方法中检测 URL 参数 `view` 和 `apiKey`
- 嵌入模式下自动使用 apiKey 登录，跳过手动登录
- 添加 `postMessage` 监听器，接收来自 React Shell 的导航事件
- 嵌入模式下给 body 添加 `embedded-mode` CSS class

### 2. 修改 `webui/index.html`

- 添加嵌入模式 CSS 规则：
  - `body.embedded-mode aside` → `display: none` (隐藏侧边栏)
  - `body.embedded-mode main.ml-64` → `margin-left: 0` (主内容占满宽度)
  - `body.embedded-mode > div[x-show="!authenticated"]` → `display: none` (隐藏登录页)
  - `body.embedded-mode main` → `padding: 1rem` (减少内边距)
- 登录页条件改为 `x-show="!authenticated && !embeddedMode"`
- 侧边栏条件改为 `x-show="!embeddedMode"`
- 主内容区域使用动态 class 绑定

## 修复效果

| 访问方式 | 渲染内容 |
|---------|---------|
| 正常访问 `/legacy/` | 完整 UI（侧边栏 + 登录 + 全功能） |
| iframe 嵌入 `/legacy/?view=services&apiKey=xxx` | 仅内容区域（无侧边栏，自动登录，指定视图） |

### 3. 修改 `webui-react/vite.config.ts`

- 添加 `base: '/ui/'` 配置，使生产构建时所有资源路径带上 `/ui/` 前缀
- 修复原因：Go 后端将 React 前端挂载在 `/ui/` 路径，但 Vite 默认打包资源路径为 `/assets/xxx`（根路径），导致通过后端访问时资源 404
- 修复后打包的 `index.html` 中资源引用变为 `/ui/assets/xxx`，与后端路由匹配

## 实施进展

- [x] 修改旧版 `app.js` - 添加嵌入模式检测和 postMessage 监听
- [x] 修改旧版 `index.html` - 添加嵌入模式 CSS 和条件渲染
- [x] 修改 `vite.config.ts` - 添加 `base: '/ui/'` 解决生产部署资源路径问题
- [x] 创建文档记录
- [x] 编译验证 - `go build ./...` 通过
- [x] Vite dev server 联调测试 - 所有页面通过
- [x] **生产部署模式测试** - React 打包 + Go 后端直接访问，所有页面通过

## 浏览器联调测试结果

### 测试 1: Vite Dev Server 模式 (2026-03-17)
- Go 后端：`localhost:8088`
- React 前端：`localhost:5173`（Vite dev server + proxy）
- 结果：所有 9 个页面 **PASS**

### 测试 2: 生产部署模式 (2026-03-17)
- Go 后端：`localhost:8088`（React 打包嵌入到 Go binary，通过 `/ui/` 路径服务）
- 访问地址：`http://localhost:8088/ui/`
- 初次测试发现资源 404 → 修复 `vite.config.ts` 添加 `base: '/ui/'` → 重新打包测试通过

### 测试项及结果

| 页面 | 嵌入模式检测 | 双侧边栏修复 | 功能正常 | 结果 |
|------|-------------|-------------|---------|------|
| Dashboard | ✅ `Embedded mode detected, view: dashboard` | ✅ 仅 React 侧边栏 | ✅ 统计数据正常 | **PASS** |
| Applications | ✅ `Embedded mode detected, view: apps` | ✅ 仅 React 侧边栏 | ✅ 表格、操作按钮正常 | **PASS** |
| Instances | ✅ `Embedded mode detected, view: instances` | ✅ 仅 React 侧边栏 | ✅ 搜索、筛选、树形导航正常 | **PASS** |
| Services | ✅ `Embedded mode detected, view: services` | ✅ 仅 React 侧边栏 | ✅ 页面正常渲染 | **PASS** |
| Tasks | ✅ `Embedded mode detected, view: tasks` | ✅ 仅 React 侧边栏 | ✅ 搜索、状态筛选正常 | **PASS** |
| Configs | ✅ `Embedded mode detected, view: configs` | ✅ 仅 React 侧边栏 | ✅ Service Tree、配置面板正常 | **PASS** |
| Traces | N/A（React 原生页面） | N/A | ✅ UI 正常，服务列表加载成功 | **PASS** |
| Metrics | N/A（React 原生页面） | N/A | ✅ PromQL + RED Dashboard 正常 | **PASS** |
| Service Map | N/A（React 原生页面） | N/A | ✅ UI 正常 | **PASS** |

### 关键验证点
1. **嵌入模式自动检测**：所有旧版页面都正确输出 `[OTel Admin] Embedded mode detected` 日志
2. **自动登录**：URL 参数 `apiKey` 被正确解析，旧版页面跳过登录流程直接显示内容
3. **postMessage 通信**：React Shell 通过 `OTEL_ADMIN_NAVIGATE` 消息与 iframe 通信正常
4. **CSS 隔离**：`body.embedded-mode` 下侧边栏隐藏、主内容区域占满宽度
5. **0 个控制台错误**：整个测试过程中没有 JS 错误

## 涉及文件

- `extension/adminext/webui/js/app.js` - 添加嵌入模式初始化逻辑
- `extension/adminext/webui/index.html` - 添加嵌入模式 CSS 和条件渲染
- `extension/adminext/webui-react/vite.config.ts` - 添加 `base: '/ui/'` 解决生产部署资源路径
- `extension/adminext/webui-react/src/pages/LegacyPage.tsx` - （无需修改，已有 postMessage 逻辑）

## 遗留问题

- Jaeger Query 后端（`jeager.http.devcloud:16686`）在本地开发环境不可达，Traces 和 Service Map 的数据查询无法验证（UI 降级展示正常）
- Prometheus 后端可达，Metrics 功能完全验证通过
- `index.html` 中 favicon 仍引用 `/vite.svg`（根路径），在生产部署时会 404，不影响功能但建议后续修复
