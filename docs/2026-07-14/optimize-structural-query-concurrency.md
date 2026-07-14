# P2 优化：并发化 GetTrace 调用（降低结构查询延迟）

## 需求背景

结构性查询（Tempo search API + TraceQL 结构操作符 `&>>` 等）的延迟较高。

### 原始流程（串行）

```
Phase 1: ES 宽松搜索 → 候选 traces 列表（最多50个）
Phase 2 (串行): for trace in candidates:
    GetTrace(traceID)   → ES 按 traceID 获取完整 spans  (IO-bound, ~100ms/次)
    EvaluateStructural  → 验证 span 是否满足结构表达式  (CPU-bound, ~1ms/次)
```

**瓶颈**：`GetTrace` 调用是串行的，50 个候选 = `50 × 100ms ≈ 5s` 纯 IO 等待。

### 优化后流程（并发）

```
Phase 1: ES 宽松搜索 → 候选 traces 列表（最多50个）
Phase 2 (并发): errgroup(10 workers)
    ┌─ GetTrace(traceID_1) + EvaluateStructural ─┐
    ├─ GetTrace(traceID_2) + EvaluateStructural ─┤
    ├─ ...                     ...              ─┤  ← 最多10个并发
    └─ GetTrace(traceID_N) + EvaluateStructural ─┘
```

**效果**：50 个候选 = `⌈50/10⌉ × 100ms ≈ 500ms`，约 **5x 加速**。

## 修复方案

### 1. 引入 errgroup 控制并发

使用 `golang.org/x/sync/errgroup`（项目已有依赖），通过 `SetLimit(10)` 控制最大并发数。

### 2. 通道收集结果

使用 `make(chan verifyResult, evalCount)` 缓冲通道收集 goroutine 结果，避免阻塞。所有 goroutine 完成后关闭通道，主 goroutine 遍历收集全部结果。

### 3. 限制后置应用

从全部通过结构验证的结果中截取 `limit` 条，确保不超过请求限制。

## 改动文件

**`extension/adminext/tempo_handler.go`**：
- 新增常量 `structuralPostFilterConcurrency = 10`
- 新增 import `golang.org/x/sync/errgroup`
- `structuralPostFilter` 方法内部：串行 `for` 循环改为 `errgroup` 并发模式

## 测试验证

| 验证项 | 结果 |
|--------|------|
| `go test ./extension/adminext/...` | ✅ PASS |
| `go test -race ./extension/adminext/` | ✅ PASS (无数据竞争) |
| 编译检查 | ✅ PASS |

## 额外收益

- **超时控制**：`errgroup.WithContext` 内建的 context 取消传播，任何一个 GetTrace 超时会取消所有未完成的 goroutine
- **错误隔离**：单个 trace 获取失败不影响其他 trace 的处理（返回 nil error，不中断 errgroup）
- **可调并发度**：`structuralPostFilterConcurrency` 常量便于后续根据存储后端性能调优

## 实施进展

- [x] 引入 errgroup + 缓冲通道收集结果
- [x] SetLimit(10) 控制并发数
- [x] 编译通过
- [x] 全部测试通过（含 race detector）
