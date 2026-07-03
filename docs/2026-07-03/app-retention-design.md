# App Retention 配置 — UI/UX 设计方案 v2

> 创建：2026-07-03 | 更新：v2 列表+抽屉
> 风格：Swiss Modernism + Soft UI Evolution
> 字体：Inter (全站统一)
> 色板：Slate 体系 (#0F172A / #334155 / #F8FAFC / #E2E8F0 / #0369A1)
> 状态：设计阶段

---

## 一、设计体系

### 色板

| Token | Hex | Tailwind | 用途 |
|-------|-----|----------|------|
| Primary | `#0F172A` | slate-900 | 主标题、深色元素 |
| Secondary | `#334155` | slate-700 | 副标题、导航 |
| CTA | `#0369A1` | sky-700 | 按钮、链接 |
| Background | `#F8FAFC` | slate-50 | 页面底色 |
| Card | `#FFFFFF` | white | 抽屉面板 |
| Border | `#E2E8F0` | slate-200 | 分割线 |
| Text | `#1E293B` | slate-800 | 正文 |
| Muted | `#64748B` | slate-500 | 辅助文字 |

信号色彩（全局统一）：
| Signal | Color | Tailwind |
|--------|-------|----------|
| trace | `#7C3AED` | purple-600 |
| metric | `#059669` | emerald-600 |
| log | `#2563EB` | blue-600 |

---

## 二、AppsPage — 列表视图

```
┌──────────────────────────────────────────────────────┐
│  Applications                                       [+ New] │
│                                                      │
│  ┌──────┬─────────────┬──────────────────┬──────────┐│
│  │ Name │ Retention   │ Token            │ Actions  ││
│  ├──────┼─────────────┼──────────────────┼──────────┤│
│  │ app001│ trace  30d  │ abc123... (mask) │ ⚙️ 🔑 🗑 ││
│  │       │ metric 30d  │                  │          ││
│  │ app002│ 平台默认     │ xyz789... (mask) │ ⚙️ 🔑 🗑 ││
│  │ app003│ trace  7d   │ def456... (mask) │ ⚙️ 🔑 🗑 ││
│  └──────┴─────────────┴──────────────────┴──────────┘│
└──────────────────────────────────────────────────────┘
```

**Actions 列：**
- ⚙️ 配置 → 打开右侧抽屉（Retention 设置）
- 🔑 Token → Token 管理模态框（已有）
- 🗑 删除 → 确认对话框（已有）

**Retention 列：** 显示信号缩写 + 天数，用彩色圆点标识：

```
trace ● 30d   ← 紫色圆点，表示已设置自定义值
metric ○ 默认  ← 灰色空心圆，表示使用平台默认
```

---

## 三、AppDetailDrawer — 右侧抽屉

点击 ⚙️ 配置按钮 → 从右侧滑入抽屉面板（`transition-transform`）

```
                    ┌─────────────────────────────────┐
                    │  ✕ 关闭                          │
                    │                                  │
                    │  app001                          │
                    │  我的应用                         │
                    │  ────────────────────────         │
                    │                                  │
                    │  Retention Policy                │
                    │                                  │
                    │  ⚡ Traces                       │
                    │  ┌─────────────────────────┐     │
                    │  │  30 天              ▼   │     │
                    │  └─────────────────────────┘     │
                    │  7d  14d  30d  60d  90d  自定义  │
                    │                                  │
                    │  📊 Metrics                      │
                    │  ┌─────────────────────────┐     │
                    │  │  平台默认               │     │
                    │  └─────────────────────────┘     │
                    │  7d  14d  30d  60d  90d  自定义  │
                    │                                  │
                    │  📝 Logs                         │
                    │  ┌─────────────────────────┐     │
                    │  │  14 天              ▼   │     │
                    │  └─────────────────────────┘     │
                    │  7d  14d  30d  60d  90d  自定义  │
                    │                                  │
                    │  ────────────────────────         │
                    │              [保存]               │
                    └─────────────────────────────────┘
```

**抽屉交互：**
- 打开：`translate-x-full` → `translate-x-0` (300ms ease-out)
- 关闭：反向动画 + 点击遮罩层 / ✕ 按钮 / ESC
- 不改变 URL（保持 `/apps` 不变，纯 UI 状态）

**信号排布：** 垂直排列三行，每行：
- 图标 + 信号名 | 下拉选择 | chip 预设按钮 | 平台默认提示

---

## 四、单行 Retention 详细设计

```
⚡ Traces                                  平台默认: 7天
┌──────────────────────┐  [7d] [14d] [30d] [60d] [90d] [自定义]
│  30 天            ▼  │
└──────────────────────┘
```

### 三种编辑状态

**状态 A：显示静态值**（默认，无编辑）
```
⚡ Traces                                  自定义 · 平台默认 7天
┌─────────────────────────┐
│  30 天                   │  ← 蓝色文字，卡片左侧带 2px 蓝色竖线
└─────────────────────────┘
```

**状态 B：点击 chip 瞬间设定**（不需要保存按钮）
```
⚡ Traces                                  自定义 · 平台默认 7天
[7d] [14d] [30d✓] [60d] [90d] [自定义 ⋯]

点击 chip 后立即调用 API 保存 → Toast "已更新 Traces 保留策略"
```

**状态 C：下拉选"自定义"**
```
⚡ Traces
┌──────────────┐  [7d] [14d] [30d] [60d] [90d] [自定义✓]
│ 720      h   │  [确认] [取消]
└──────────────┘
```

### 重置为默认

在"已自定义"状态下，每行右侧显示一个小链接：
```
自定义 · 平台默认 7天  [重置]
```
点击 → API DeleteForApp → 回到"平台默认"状态

---

## 五、Token 管理

抽屉底部分割线下方放 Token 管理：

```
──────────────────────────
Token
┌──────────────────────────────────┐
│ ●●●●●●●●●●●●●●●●●●●●     👁 [复制] │
│                      [重新生成]    │
└──────────────────────────────────┘
[删除应用]
```

---

## 六、API 端点

```
GET  /api/v2/apps/{appID}/retention
  → 返回该 app 各 signal 的 retention + 平台默认值对照

PUT  /api/v2/apps/{appID}/retention/{signal}
  Body: {"duration": "720h"}
  → 写入 RetentionStore + 更新 ES ILM

DELETE /api/v2/apps/{appID}/retention/{signal}
  → 删除 app override，回退到平台默认
```

响应格式：
```json
// GET /apps/app001/retention
{
  "trace":  {"value": "720h", "source": "app",   "platform_default": "168h"},
  "metric": {"value": "720h", "source": "platform", "platform_default": "720h"},
  "log":    {"value": null,   "source": "platform", "platform_default": "168h"}
}
```

---

## 七、实施计划

| Sprint | 内容 | 文件数 |
|--------|------|--------|
| **Sprint 1** 后端 | Handler + router + adapter | 3 Go 文件 |
| **Sprint 2** 前端 | `AppDetailDrawer.tsx` + 重构 `AppsPage.tsx` 列表 | 3 TSX 文件 |

### 前端文件

| 文件 | 操作 | 说明 |
|------|------|------|
| `components/AppDetailDrawer.tsx` | 新增 | 右侧抽屉容器，含 Retention + Token |
| `components/AppRetentionRow.tsx` | 新增 | 单个信号的 retention 编辑行 |
| `pages/AppsPage.tsx` | 重构 | 列表增加 Retention 列 + ⚙️ 按钮打开抽屉 |

### 后端文件

| 文件 | 操作 | 说明 |
|------|------|------|
| `adminext/router.go` | 新增路由 | `GET/PUT/DELETE /apps/{appID}/retention` |
| `adminext/app_handler.go` | 新增 handler | 3 个 handler（get/set/delete retention） |
| `observabilitystorageext/extension.go` | 复用 | `GetRetentionStore()` 已有 |

---

## 八、验收标准

- [ ] AppsPage 列表包含 Retention 列（显示各信号配置摘要）
- [ ] 点击 ⚙️ → 右侧抽屉滑入
- [ ] 抽屉内三行 signal retention，带 chip 快捷预设 + 自定义输入
- [ ] 点击 chip → 瞬间保存 + Toast 提示
- [ ] 抽屉内包含 Token 管理 + 删除按钮
- [ ] 点击遮罩/✕/ESC → 抽屉关闭
- [ ] 暗黑模式完整支持
- [ ] TypeScript 零错误
