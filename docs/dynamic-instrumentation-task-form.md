# Dynamic Instrumentation 任务创建表单优化

> **日期**: 2026-03-11  
> **状态**: ✅ 已完成

---

## 一、需求背景

根据 Java Agent 侧的 `dynamic-instrumentation-test-cases.md` 文档，需要在控制面 Admin WebUI 的"创建任务"弹窗中新增 `dynamic_instrument`（动态增强）和 `dynamic_uninstrument`（动态还原）两种任务类型。

由于 `dynamic_instrument` 的 `parameters_json` 参数较为复杂（class_name、method_name、type、config.* 等），采用**动态表单字段**代替原始 JSON textarea，用户只需填写各个输入框，前端自动组装成 `parameters_json` 提交。

## 二、设计方案

### 2.1 方案选型

**方案 B+ — 动态表单字段 + 自动组装 JSON**

- 选择 `dynamic_instrument` / `dynamic_uninstrument` 时，Parameters 区域替换为结构化表单
- 选择其他类型（arthas_*, async-profiler, custom）时，保持原有 JSON textarea
- 利用 Alpine.js 的 `x-show`、`x-model`、`x-transition` 实现条件渲染和双向绑定

### 2.2 表单字段设计

#### dynamic_instrument

| 字段 | 类型 | 必填 | 条件显示 | 说明 |
|------|------|------|----------|------|
| class_name | text | ✅ | — | 全限定类名 |
| method_name | text | ✅ | — | 方法名 |
| type | select | ✅ | — | trace / metric / log |
| span_name | text | ❌ | type=trace | 自定义 Span 名 |
| parameter_types | text | ❌ | — | 匹配重载，如 `String,int` |
| rule_id | text | ❌ | — | 不填自动生成 |
| capture_args | text | ❌ | type=trace | `0,2` / `*` / `userId` |
| capture_return | text | ❌ | type=trace | `*` / `id,name` |
| capture_max_length | text | ❌ | type=trace | 默认 256 |
| force | checkbox | ❌ | — | 强制增强（忽略冲突） |
| method_descriptor | text | ❌ | — | 高级 JVM 描述符 |

#### dynamic_uninstrument

支持两种模式（Tab 切换）：

**模式 1 — By Rule ID**：
| 字段 | 类型 | 必填 |
|------|------|------|
| rule_id | text | ✅ |

**模式 2 — By Method**：
| 字段 | 类型 | 必填 |
|------|------|------|
| class_name | text | ✅ |
| method_name | text | ✅ |
| type | select | ❌ (不选=还原所有类型) |

### 2.3 提交逻辑

`submitTask()` 根据 `task_type_preset` 分支处理：
- `dynamic_instrument` → 调用 `buildInstrumentParams()` 从表单 state 组装 JSON
- `dynamic_uninstrument` → 调用 `buildUninstrumentParams()` 从表单 state 组装 JSON
- 其他类型 → 走原有 `JSON.parse(parameters_json)` 逻辑

组装时只包含非空字段，保持 JSON 简洁。

## 三、改动文件

| 文件 | 改动说明 |
|------|----------|
| `webui/js/views/tasks.js` | 新增 `dynInstrumentForm` / `dynUninstrumentForm` state、`hasDynamicForm()`、`resetDynamicForms()`、`buildInstrumentParams()`、`buildUninstrumentParams()`；`submitTask()` 增加动态表单组装分支和必填校验 |
| `webui/views/tasks.html` | 下拉框新增 2 个 option（分组）；新增 `dynamic_instrument` 动态表单区域；新增 `dynamic_uninstrument` Tab 切换表单；原 JSON textarea 条件渲染（非 dynamic 类型才显示）；modal 宽度从 `max-w-lg` 调整为 `max-w-2xl` |

## 四、向下兼容

- 原有 `arthas_attach`、`arthas_detach`、`async-profiler`、`custom` 类型完全不受影响
- JSON textarea 仍保留给非 dynamic 类型使用
- 新增的 state 字段不影响现有 `newTask` 对象结构

## 五、Bug 修复记录

### 5.1 dynamic_uninstrument 表单输入框不显示（2026-03-11）

**现象**：选择 `Dynamic Uninstrument` 时，"By Rule ID" 和 "By Method" 两个 Tab 按钮可见，但下方的输入框区域不显示。

**原因**：`<template x-if>` 内部嵌套的 `<div x-show x-transition>` 存在 Alpine.js 已知的时序问题 —— 当 `x-if` 条件从 false 变为 true 首次插入 DOM 时，`x-show` + `x-transition` 的过渡动画初始化时序导致元素未能正确显示。

**修复**：去掉两个模式内容区域 div 的 `x-transition` 属性，只保留 `x-show` 进行条件控制。

### 5.2 切换 Tab 时弹框跳动（2026-03-11）

**现象**：在 `Dynamic Uninstrument` 表单中，从 "By Rule ID" 切换到 "By Method" 时，弹框产生明显的抖动/跳动。

**原因**：两个模式区域高度差异较大（Rule ID 只有 1 个输入框，Method 有 2 个输入框 + 1 个下拉框），`x-show` 切换时容器高度突变导致整个 modal 重新布局产生跳动。

**修复**：用 `<div class="min-h-[160px]">` 容器包裹两个模式内容区域，确保切换 Tab 时容器始终保持最小高度，避免布局跳动。同时修复了 Rule ID 区域残留的 `x-transition` 属性。

## 六、遗留事项

- 无
