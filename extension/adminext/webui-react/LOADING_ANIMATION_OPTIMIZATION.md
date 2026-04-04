# 加载动画优化需求文档

## 需求描述

优化数据加载时页面的动画效果，提升美感和用户体验。

### 核心目标
- 用**骨架屏（Skeleton）**替代简单的 spinner 加载
- 数据加载完成后内容**渐入过渡**
- 升级 **LazyLoadFallback** 组件的加载动画
- 添加 **shimmer 闪光**效果提升骨架屏质感

## 架构设计

```mermaid
graph TD
    A[用户打开页面] --> B{数据是否已加载?}
    B -->|否| C[显示骨架屏 + Shimmer 闪光]
    B -->|是| D[内容渐入显示 fade-in]
    
    C --> E[InstancesPage 实例列表骨架屏]
    C --> F[DashboardPage 统计卡片骨架屏]
    C --> G[InstanceTasksTab 任务列表骨架屏]
    
    H[懒加载组件] --> I[LazyLoadFallback 三点脉冲 + 骨架条]
    
    subgraph 动画基础设施
        J[tailwind.config.js - shimmer/fadeIn/fadeInUp 动画]
        K[index.css - skeleton-shimmer/content-fade-in 工具类]
    end
    
    J --> E
    J --> F
    J --> G
    K --> E
    K --> F
    K --> G
```

## 涉及文件

| 文件 | 改动说明 | 状态 |
|------|----------|------|
| `tailwind.config.js` | 添加 shimmer / fadeIn / fadeInUp 动画配置 | ✅ 已完成 |
| `src/index.css` | 添加 skeleton-shimmer / content-fade-in 工具类 | ✅ 已完成 |
| `src/pages/InstancesPage.tsx` | 实例列表骨架屏（6行占位）+ 内容渐入 | ✅ 已完成 |
| `src/components/InstanceTasksTab.tsx` | 任务列表骨架屏（4行占位）+ 内容渐入 | ✅ 已完成 |
| `src/pages/DashboardPage.tsx` | 统计卡片骨架屏（4卡占位）+ 内容渐入 | ✅ 已完成 |
| `src/components/LazyLoadFallback.tsx` | 三点脉冲动画 + 骨架占位条 | ✅ 已完成 |

## 实施进展

### 2026-04-04
- [x] 创建需求文档
- [x] 实施 tailwind.config.js 动画配置（shimmer / fadeIn / fadeInUp）
- [x] 实施 index.css shimmer keyframes + 工具类
- [x] 实施 InstancesPage 骨架屏（6行实例占位 + content-fade-in）
- [x] 实施 InstanceTasksTab 骨架屏（4行任务占位 + content-fade-in）
- [x] 实施 DashboardPage 骨架屏（4卡片占位 + content-fade-in）
- [x] 实施 LazyLoadFallback 升级（三点脉冲 + 骨架条）
- [x] 编译验证通过（tsc --noEmit 零错误）

---

# 实例管理页面级联查询空状态优化

## 需求描述

优化实例管理页面在级联查询（App → Service → Instance）过程中的空状态展示，根据不同的级联阶段显示差异化的引导提示，提升用户体验。

### 核心问题
- 空状态缺乏引导：只显示 "No instances found"，不区分原因
- 右侧大面积空白：未选中实例时只有小图标，空间浪费
- 级联状态不明确：用户不清楚是"无 App"还是"无 Service"还是"无实例"
- 状态筛选栏噪音：全是 0 的筛选栏没有实际意义

## 架构设计

```mermaid
graph TD
    A[用户进入页面] --> B{emptyReason 判断}
    B -->|loading| C[骨架屏加载动画]
    B -->|no_apps| D[蓝色引导: 暂无应用 + 部署流程步骤]
    B -->|no_services| E[琥珀色引导: 该应用下暂无服务]
    B -->|no_instances| F[绿色引导: 该服务下暂无实例]
    B -->|search_empty| G[紫色引导: 没有匹配 + 清除/重置按钮]
    B -->|has_data| H[正常显示实例列表]
    
    subgraph 左侧面板
        C
        D
        E
        F
        G
        H
    end
    
    subgraph 右侧面板
        D --> D2[欢迎使用 + 部署→注册→管理 步骤指引]
        E --> E2[该应用下暂无服务 + 应用名称高亮]
        F --> F2[该服务下暂无实例 + 服务名称高亮]
        G --> G2[没有匹配的实例 + 调整建议]
        H --> H2[选择一个实例 / 实例详情]
    end
    
    subgraph 状态筛选栏
        I{instances.length === 0?}
        I -->|是| J[opacity-30 + pointer-events-none 淡化]
        I -->|否| K[正常显示]
    end
    
    style D fill:#eff6ff
    style E fill:#fef3c7
    style F fill:#f0fdf4
    style G fill:#faf5ff
```

## 涉及文件

| 文件 | 改动说明 | 状态 |
|------|----------|------|
| `src/pages/InstancesPage.tsx` | 新增 `emptyReason` 状态判断 + 左侧分层空状态 + 右侧上下文引导 + 筛选栏淡化 | ✅ 已完成 |

## 实施进展

### 2026-04-04
- [x] 新增 `EmptyReason` 类型和 `emptyReason` 计算属性（loading / no_apps / no_services / no_instances / search_empty / has_data）
- [x] 左侧面板：5 种空状态分层引导（骨架屏 / 无App / 无Service / 无实例 / 搜索无结果），各带独立图标和配色
- [x] 左侧面板：搜索无结果时提供"清除搜索"和"重置筛选"快捷按钮
- [x] 右侧面板：5 种上下文引导卡片（欢迎+步骤指引 / 无服务 / 无实例 / 搜索无结果 / 选择实例）
- [x] 右侧面板：无 App 时显示"部署应用 → 自动注册 → 开始管理"三步引导流程
- [x] 状态筛选栏：实例为空时 opacity-30 + pointer-events-none 淡化处理
- [x] 编译验证通过（tsc --noEmit 零错误）

## 遗留问题

暂无

---

# SearchableSelect 下拉框长文本溢出优化

## 需求描述

优化 SearchableSelect 组件在 Service 名称较长时（如 `test-java-delivery-service`）的显示问题，避免文本换行撑高按钮，破坏顶部栏对齐。

### 核心问题
- 选择器按钮文本无截断：长名称导致文本换行，按钮高度被撑大
- 下拉面板宽度受限：面板与按钮同宽，长选项显示不完整
- Service 下拉框宽度偏窄：`w-52`（208px）不够容纳常见的长服务名

## 架构设计

```mermaid
graph LR
    A[长文本 Service 名称] --> B{SearchableSelect 按钮}
    B --> C[truncate 截断 + 省略号]
    B --> D[title 属性 tooltip 显示完整名称]
    
    A --> E{下拉面板}
    E --> F[min-w-full 至少与按钮同宽]
    E --> G[w-max 自适应内容宽度]
    E --> H[max-w-360px 防止过宽]
    
    A --> I{InstancesPage}
    I --> J[Service 下拉框 w-52 → w-56 加宽]
    
    style C fill:#dbeafe
    style F fill:#dcfce7
    style J fill:#fef3c7
```

## 涉及文件

| 文件 | 改动说明 | 状态 |
|------|----------|------|
| `src/components/SearchableSelect.tsx` | 按钮文本 truncate + title tooltip + 下拉面板 min-w-full/w-max/max-w | ✅ 已完成 |
| `src/pages/InstancesPage.tsx` | Service 下拉框 `w-52` → `w-56` | ✅ 已完成 |

## 实施进展

### 2026-04-04
- [x] 按钮 `<span>` 添加 `truncate` 类，超长文本显示省略号
- [x] 按钮添加 `title={selectedLabel}` 属性，鼠标悬停显示完整名称
- [x] 下拉面板从 `w-full` 改为 `min-w-full w-max max-w-[360px]`，自适应内容宽度
- [x] Service 下拉框从 `w-52`（208px）加宽到 `w-56`（224px）
- [x] 编译验证通过（tsc --noEmit 零错误）

## 遗留问题

暂无
