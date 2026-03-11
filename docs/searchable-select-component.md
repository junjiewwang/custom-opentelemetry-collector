# SearchableSelect 可复用搜索下拉框组件

## 需求背景

Create Task 弹框中的 **Task Type** 和 **Target Agent** 两个字段使用原生 `<select>` 下拉框，
存在以下体验问题：
- 不支持搜索过滤，选项多时查找困难
- Target Agent 在实例数量多时无法快速定位
- 原生 select 样式不够美观

## 改造方案

将原生 `<select>` 改为**可复用的搜索下拉框组件**（SearchableSelect），纯 Alpine.js + Tailwind CSS 实现，
零第三方依赖，可在项目中任意页面复用。

## 组件设计

### 文件位置

```
webui/js/components/searchable-select.js
```

### 核心特性

| 特性 | 说明 |
|------|------|
| 搜索过滤 | 输入关键字实时过滤候选项 |
| 分组显示 | 类似 optgroup 的分组标题效果 |
| 键盘导航 | ↑ ↓ 切换高亮，Enter 选中，Escape 关闭 |
| 高亮匹配 | 搜索词在候选项中高亮标记 |
| 点击外部关闭 | `@click.away` 自动关闭面板 |
| 懒加载 | 支持首次打开时异步加载数据 |
| 自定义输入 | 允许用户输入不在列表中的值 |
| 多实例共存 | 通过可配置前缀（prefix）隔离状态 |

### API 设计

```javascript
import { SearchableSelect } from '../components/searchable-select.js';

// 在 Alpine.js data 对象中展开
export function myView() {
    return {
        ...SearchableSelect.create('myPrefix', {
            options: [{ value: 'a', label: 'Option A', group: 'Group1' }],
            value: '',
            placeholder: 'Search...',
            searchKeys: ['label'],
            displayKey: 'label',
            valueKey: 'value',
            groups: ['Group1', 'Group2'],
            allowCustom: false,
            customLabel: '-- Custom --',
            emptyText: 'No results',
        }),
    };
}
```

### 前缀命名规则

每个实例通过 `prefix` 参数隔离命名空间：

- **Task Type**: prefix = `tt` → `ttValue`, `ttOpen()`, `ttSelect()`, `ttGetValue()` ...
- **Target Agent**: prefix = `ta` → `taValue`, `taOpen()`, `taSelect()`, `taGetValue()` ...

## 实施进展

### ✅ 已完成

1. **创建 SearchableSelect 组件** (`js/components/searchable-select.js`)
   - 工厂函数 `SearchableSelect.create(prefix, config)` 
   - 支持前缀隔离的多实例共存
   - 完整的搜索、过滤、分组、键盘导航、高亮匹配功能

2. **Task Type 改造** (`tasks.js` + `tasks.html`)
   - 使用 `tt` 前缀创建搜索下拉框实例
   - 支持分组（Dynamic Instrumentation / Diagnostics）
   - 支持 Custom Type 自定义输入
   - 动态表单（dynamic_instrument / dynamic_uninstrument）条件显示已适配

3. **Target Agent 改造** (`tasks.js` + `tasks.html`)
   - 使用 `ta` 前缀创建搜索下拉框实例
   - 支持按 service_name、hostname、IP、agent_id 搜索
   - 首次打开时懒加载 instances 数据
   - Global Broadcast 默认选项保留
   - 在线状态绿色圆点标识

4. **submitTask 适配**
   - 从 SearchableSelect 实例获取选中值
   - 表单重置时同步重置搜索下拉框状态

## 修改文件清单

| 文件 | 操作 | 说明 |
|------|------|------|
| `js/components/searchable-select.js` | **新增** | 可复用搜索下拉框组件 |
| `js/views/tasks.js` | 修改 | 引入组件，创建实例，适配提交/重置逻辑 |
| `views/tasks.html` | 修改 | 替换 Task Type 和 Target Agent 的 HTML 模板 |

## 后续可复用场景

- Instances 页面：按 service name 筛选实例
- Configs 页面：选择配置模板
- Apps 页面：选择应用
- 任何需要"搜索 + 选择"的交互场景

## 遗留问题

- 暂无
