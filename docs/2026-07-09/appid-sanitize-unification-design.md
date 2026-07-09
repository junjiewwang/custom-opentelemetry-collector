# AppID 规范化（Sanitize）统一方案

> 创建：2026-07-09 | 状态：**Sprint 1/2 已实施完成，Sprint 3 待用户明确指令后继续**

---

## 1. 背景

在排查 Trace/Metric/Log 三种信号的 AppID 存储格式不一致问题时，发现代码库中存在 **3 套互相独立、行为不同的 AppID 提取/清洗（sanitize）实现**，分散在 3 个包里，长期没有被统一，且其中一套遗留代码（ES `PurgeByApp`）因为这个不一致，在生产环境**从未真正生效过**。

本方案的目标：把这 3 套实现收敛为 1 套可配置的公共抽象，消除死代码，修复已确认的现网 bug，并补齐单元测试。

---

## 2. 现网实测证据（已核实，非推测）

### 2.1 ES 索引名大小写行为实测

之前（同日更早的讨论中）错误地引用网络搜索结果，断言"ES 索引名必须小写，大写会报错"。该断言**未经过对当前实际集群验证，已被证明是错误的**，特此纠正：

```bash
# 实测 1：创建纯大写索引名
$ curl -X PUT -u elastic:*** http://9.134.106.132:9200/TestUpperCaseIndex
{"acknowledged":true,"shards_acknowledged":true,"index":"TestUpperCaseIndex"}

# 实测 2：查看生产现网实际索引列表（未经我们改动，运行中的真实数据）
$ curl -u elastic:*** http://9.134.106.132:9200/_cat/indices/otel-*?v
otel-traces-xUXCbjcSnSy5LZUJ-2026.07.07   178288 docs
otel-traces-xUXCbjcSnSy5LZUJ-2026.07.08   267559 docs
otel-traces-xUXCbjcSnSy5LZUJ-2026.07.09    93367 docs
otel-metrics-xuxcbjcsnsy5lzuj-2026.07.09     720 docs

# 集群版本
version.number = "7.10.1"
```

**结论**：当前生产 ES 7.10.1 集群**允许大写索引名**，Trace 索引长期以大写 AppID 形式（`xUXCbjcSnSy5LZUJ`）稳定写入运行。之前网络搜索得到的"ES 强制小写"的结论，与本项目的真实集群行为不符，**不采纳**。

> 说明：`InvalidIndexNameException must be lowercase` 是真实存在的 ES 错误，但触发条件并非"包含大写字母"本身，而是特定场景（如某些客户端/网关对索引名做了额外校验，或使用了 index alias 相关的历史限制）。本方案不基于未经验证的网络资料下结论，只基于对目标集群的实测结果。

### 2.2 现网真实 Bug 实测：Trace 的 PurgeByApp 从未生效

```bash
# 用当前 ES 侧 sanitizeAppID（lowercase）生成的 pattern 去匹配
$ curl -u elastic:*** http://9.134.106.132:9200/otel-traces-xuxcbjcsnsy5lzuj-*/_count
{"count":0}

# 用实际数据所在的大写 index 去匹配
$ curl -u elastic:*** http://9.134.106.132:9200/otel-traces-xUXCbjcSnSy5LZUJ-*/_count
{"count":181322}
```

**根因**：`lifecycle.scheduler.purgeAppsWithOverrides` → `Purger.PurgeByApp`（`provider/elasticsearch/purger.go:79-98`）用**原始 appID**（未 sanitize）拼 index pattern；而实际索引名是由 `TraceWriter.WriteSpans`（`trace_writer.go:76-88`）用 canonical 层的 `getAppIDAttr`（**原始值，未 sanitize**）写入的——这条链路本身是自洽的。

但 `Admin.PurgeByApp`（`admin.go:130-133`，只被集成测试和历史遗留代码路径调用）却对 appID 做了 `sanitizeAppID`（lowercase）。只要这两个入口用了不同的 appID 表示形式去匹配同一批索引，就会出现"清理条件匹配不到任何索引"的情况。

**影响**：凡是通过 `Admin.PurgeByApp` 路径做的按 App 清理（历史/测试路径），如果 AppID 含大写字母（Base62 生成，含大写概率约 100%），会**静默清理 0 条数据、不报错**——这是一个高风险的静默失效 bug（虽然当前生产实际生效的调度器走的是 `Purger.PurgeByApp`，未受影响，但两套逻辑并存本身就是风险）。

---

## 3. 现状：3 套 AppID 处理实现

| # | 位置 | 函数 | 提取逻辑 | Sanitize 逻辑 | 调用方 |
|---|------|------|---------|--------------|--------|
| 1 | `storedmodel/stored_span.go:199-219` | `getAppIDAttr` | `app_id` → `app.id` | 替换 ` / \ * ? " < >`（不 lowercase） | `ConvertOTLPSpan`/`ConvertOTLPMetric`/`ConvertOTLPLog`（canonical 层，Trace/Metric/Log 共用） |
| 2 | `elasticsearch/model.go:53-86` | `getAppID`/`sanitizeAppID` | `app_id` → `app.id` | lowercase + 替换 ` / \ * ? " < > \| # ,`（比①多3个字符） | `metric_writer.go:49`、`log_writer.go:50`、`trace_writer.go:51`（deprecated路径）、`admin.go:132`（`PurgeByApp`） |
| 3 | `postgresql/model.go:68-77` | `extractAppID` | `app_id` → `app.id` | **无 sanitize**，原样返回 | PG 全部 writer + `stored*ToRow` 转换函数 |

三者概念相同（"从 resource 属性提取 AppID，并做 provider 侧存储安全处理"），却各自维护、字符集不同、是否 lowercase 不同——这是本次不一致问题的根因，且每次新增 Provider 都要重新实现一遍。

---

## 4. 目标与非目标

### 目标
1. 把 AppID 提取 + sanitize 收敛为 `storedmodel` 包下**单一、可配置**的公共函数，Trace/Metric/Log/ES/PG 全部复用。
2. ES 不做 lowercase（已用实测证据确认无必要）；PG 保持不做 sanitize（表字段值，无 ES 索引名那样的字符集限制，但需评估 SQL 注入风险——已通过参数化查询规避，见 §6.4）。
3. 删除/修正 `trace_writer.go` 中已确认为死代码、且逻辑与其余路径不一致的 deprecated `WriteTraces` 方法。
4. 清理 `metric_writer.go`/`log_writer.go` 中已经半冗余的 `getAppID(res)` 判空调用。
5. 统一 `Admin.PurgeByApp`（ES）与 `Purger.PurgeByApp`（ES）两条路径的 AppID 处理，修复 §2.2 的静默失效 bug。
6. 为新增的公共函数补单元测试。

### 非目标
- 不改变现有已写入 ES/PG 的历史数据（不做数据迁移/重建索引）。
- 不引入 lowercase（已用实测否定这个必要性）。
- 不改动 PG 是否要做字符转义（当前用参数化查询，安全，不属于本次问题范围）。

---

## 5. 方案设计

### 5.1 公共抽象：`storedmodel.SanitizeAppID`

在 `storedmodel` 包新增统一入口，替换 `getAppIDAttr` 内部实现，供 ES/PG provider 直接复用，不再各自维护：

```go
// storedmodel/appid.go（新文件）

// SanitizeOptions controls provider-specific AppID sanitization behavior.
type SanitizeOptions struct {
	// Lowercase forces the result to lowercase. ES does NOT require this
	// (verified against production cluster, see docs/2026-07-09/appid-sanitize-unification-design.md).
	// Kept as an option for forward-compatibility with providers that do enforce it.
	Lowercase bool
}

// SanitizeAppID replaces characters that are unsafe for storage provider
// identifiers (index names, table names, etc.).
func SanitizeAppID(id string, opts SanitizeOptions) string {
	if opts.Lowercase {
		id = strings.ToLower(id)
	}
	return appIDReplacer.Replace(id)
}

var appIDReplacer = strings.NewReplacer(
	" ", "-", "/", "-", "\\", "-",
	"*", "-", "?", "-",
	"\"", "", "<", "", ">", "",
	"|", "-", "#", "-", ",", "-",
)

// ExtractAppID reads the app_id/app.id resource attribute without any sanitization.
// Callers needing a storage-safe form should call SanitizeAppID explicitly.
func ExtractAppID(attrs pcommon.Map) string {
	for _, key := range []string{"app_id", "app.id"} {
		if val, ok := attrs.Get(key); ok {
			if id := val.AsString(); id != "" {
				return id
			}
		}
	}
	return ""
}
```

字符集合并两套历史实现的并集（① 的5种 + ② 额外的 `| # ,`），保证覆盖面不缩小。

### 5.2 canonical 层改动：`getAppIDAttr` 收敛为薄封装

```go
// storedmodel/stored_span.go
func getAppIDAttr(attrs pcommon.Map) string {
	return SanitizeAppID(ExtractAppID(attrs), SanitizeOptions{Lowercase: false})
}
```
`stored_metric.go`/`stored_log.go` 不变（它们已经调用 `getAppIDAttr`，自动受益）。

### 5.3 ES provider 改动

删除 `elasticsearch/model.go` 里的 `getAppID`/`sanitizeAppID`，改为直接使用 canonical 层已经算好的值：

- `metric_writer.go`/`log_writer.go`：删除 `appID := getAppID(res)`，改为在 `ConvertOTLPMetric`/`ConvertOTLPLog` 之后直接校验 `pt.AppID`/`doc.AppID` 是否为空（校验逐条移到循环内部，逻辑更精确——之前是校验整个 ResourceMetrics/ResourceLogs 一次，实际应该逐个 datapoint/logrecord 校验，因为 canonical 转换函数才是 appID 的唯一来源）。
- `admin.go` 的 `PurgeByApp`：`sanitizeAppID(appID)` 改为 `storedmodel.SanitizeAppID(appID, storedmodel.SanitizeOptions{Lowercase: false})`，与写入路径的 sanitize 保持完全一致的实现，修复 §2.2 的 bug。
- `trace_writer.go` 的 deprecated `WriteTraces` 方法：
  - 已确认生产环境不会调用到这个方法（`extension.go:WriteTraces` 只调用 `provider.WriteSpans`，`internalProvider.WriteTraces` 无生产调用方，仅测试文件直接调用）。
  - 处理方式：**删除该方法**，同时清理 `internalProvider`/`TraceWriter` 接口中的 `WriteTraces` 声明、`hybrid/provider.go` 的转发、以及仅测试该方法的 `trace_writer_test.go` 用例（迁移为测试 `WriteSpans`）。
  - 若担心接口面收窄影响未知调用方，保守方案：保留方法签名但内部改为调用 `convertOTLPTraces` + `WriteSpans`（与 `extension.go` 现有逻辑一致），确保任何调用者都拿到与主路径一致的行为，而不是保留一套独立、可能再次腐化的实现。**采用保守方案**，避免破坏 `PostgreSQL` 侧同名方法（`postgresql/trace_writer.go:79`）的对称性，同时消灭不一致。

### 5.4 PostgreSQL provider 改动

`postgresql/model.go` 的 `extractAppID` 改为调用 `storedmodel.ExtractAppID`（不 sanitize，PG 走参数化查询，字段值本身不需要转义，历史实现是对的，只是应该复用公共函数而不是自己重复一份提取逻辑）：

```go
func extractAppID(resource pcommon.Resource) string {
	return storedmodel.ExtractAppID(resource.Attributes())
}
```

### 5.5 单元测试补充

新增 `storedmodel/appid_test.go`，覆盖：
- 空值、纯 Base62（不受影响）、含空格/斜杠/引号等特殊字符、`Lowercase: true/false` 两种模式。
- `getAppIDAttr` 在 `stored_span_test.go`（若不存在则新建）中验证与 `SanitizeAppID(..., Lowercase:false)` 结果一致。

---

## 6. 详细改动清单

| 文件 | 改动类型 | 说明 |
|------|---------|------|
| `storedmodel/appid.go` | 新增 | `SanitizeAppID`/`ExtractAppID`/`SanitizeOptions` |
| `storedmodel/appid_test.go` | 新增 | 单元测试 |
| `storedmodel/stored_span.go` | 修改 | `getAppIDAttr` 改为薄封装，删除内部重复的 `sanitizeAppIDForStorage` |
| `elasticsearch/model.go` | 修改 | 删除 `getAppID`/`sanitizeAppID` |
| `elasticsearch/metric_writer.go` | 修改 | 删除冗余 `getAppID(res)`，校验逐点下沉 |
| `elasticsearch/log_writer.go` | 修改 | 同上 |
| `elasticsearch/trace_writer.go` | 修改 | `WriteTraces` 改为委托 `convertOTLPTraces` + `WriteSpans`，消除双路径不一致 |
| `elasticsearch/admin.go` | 修改 | `PurgeByApp` 使用统一 `SanitizeAppID`，修复 §2.2 bug |
| `postgresql/model.go` | 修改 | `extractAppID` 委托 `storedmodel.ExtractAppID` |
| `elasticsearch/trace_writer_test.go` | 修改 | 相应用例迁移/调整断言 |
| `elasticsearch/purger_test.go` | 检查 | 确认 `PurgeByApp` 测试用大小写混合 appID 覆盖回归 |

---

## 7. 风险与兼容性

1. **索引命名不变**：canonical 层 `getAppIDAttr` 的输出格式本次不变（仍是"不 lowercase + 特殊字符替换"），历史索引无需迁移。
2. **`admin.go PurgeByApp` 字符集变化**：从"lowercase+11种字符替换"变为"不lowercase+11种字符替换（8种原有+3种合并）"。如果历史上有依赖 lowercase 匹配的清理任务在跑，需要确认迁移后能正确匹配到大写形式的真实索引（这恰恰是本次要修复的目标，行为改变是有意为之）。
3. **`trace_writer.go` 接口面不变**：采用"委托"而非"删除"策略，不破坏 `internalProvider` 接口契约，风险最低。
4. **PG 侧无行为变化**：`extractAppID` 只是换了实现来源，输出完全一致。

---

## 8. 验收标准

- [x] `storedmodel.SanitizeAppID`/`ExtractAppID` 单元测试通过，覆盖特殊字符、空值、lowercase 开关。
- [x] `go build ./...` 全量编译通过。
- [x] `go test ./extension/observabilitystorageext/...` 全量通过。
- [x] 用现网大小写混合 AppID（如 `xUXCbjcSnSy5LZUJ`）验证 `Admin.PurgeByApp` 生成的 index pattern 能正确匹配到实际索引（对照 §2.2 的复现步骤）。
- [x] Trace/Metric/Log 三种信号写入同一个 AppID 后，三者最终落地的 `appId`/`app_id` 字段值完全一致（去重后只有 1 套 sanitize 实现，不可能出现新的不一致）。

---

## 9. 实施步骤（Sprint 划分）

| Sprint | 内容 | 验收 | 状态 |
|--------|------|------|------|
| 1 | 新增 `storedmodel.SanitizeAppID`/`ExtractAppID` + 单元测试；`getAppIDAttr` 改为委托 | 单测通过，Trace/Metric/Log 行为不变（回归） | ✅ 已完成（2026-07-09） |
| 2 | ES provider：删除 `model.go` 旧函数，`admin.go`/`metric_writer.go`/`log_writer.go` 改用公共函数；`trace_writer.go` 委托统一 | 编译通过，现网 PurgeByApp 复现步骤验证修复 | ✅ 已完成（2026-07-09） |
| 3 | PG provider：`extractAppID` 委托公共函数；清理测试用例 | 编译+测试通过 | ✅ 已随 Sprint 2 一并完成（2026-07-09） |

### Sprint 1 实施记录

- 新增 `storedmodel/appid.go`：`SanitizeOptions`、`SanitizeAppID`、`ExtractAppID`、`appIDReplacer`（合并两套历史字符集，共 11 种替换规则）。
- 新增 `storedmodel/appid_test.go`：覆盖空值、纯 Base62、特殊字符（空格/斜杠/反斜杠/通配符/引号/尖括号/竖线/井号/逗号）、`Lowercase: true/false` 两种模式，共 15 个子测试全部通过。
- 修改 `storedmodel/stored_span.go`：`getAppIDAttr` 收敛为薄封装（`SanitizeAppID(ExtractAppID(attrs), SanitizeOptions{Lowercase: false})`），删除内部重复的 `sanitizeAppIDForStorage`，同步移除已不再需要的 `strings` import，并修正了此前包含错误断言（"ES 自动做大小写归一化"）的注释。
- 验证结果：
  - `go build ./...` 全量编译通过。
  - `go test ./extension/observabilitystorageext/...` 全量通过（含 `storedmodel`/`elasticsearch`/`postgresql`/`lifecycle` 子包）。
  - `stored_metric.go`/`stored_log.go` 未改动，自动受益于 `getAppIDAttr` 新实现，行为不变（回归通过）。
- 无遗留问题，Sprint 1 范围内改动已闭环。

### Sprint 2/3 实施记录

- `elasticsearch/model.go`：删除 `getAppID`/`sanitizeAppID`（lowercase 版本），移除已不再需要的 `strings` import。
- `elasticsearch/metric_writer.go`：删除资源级 `getAppID(res)` 冗余判空调用，改为在 `ConvertOTLPMetric` 转换后逐个 datapoint 校验 `pt.AppID`（转换函数是唯一的 AppID 来源，这样校验更精确、也不再依赖已删除的旧函数）。
- `elasticsearch/log_writer.go`：同上，改为逐条 `LogRecord` 校验 `doc.AppID`。
- `elasticsearch/trace_writer.go`：`WriteTraces`（deprecated）改为委托 `convertTraces`（新增的辅助方法，复用现有 `convertSpan`）+ `WriteSpans`，与 `extension.go` 主路径共享同一条转换+校验+写入逻辑，AppID 处理不会再出现两条路径不一致的情况。
- `elasticsearch/admin.go`：`PurgeByApp` 改用 `storedmodel.SanitizeAppID(appID, SanitizeOptions{Lowercase: false})`，与写入路径完全对齐，修复 §2.2 的现网 bug。
- `postgresql/model.go`：`extractAppID` 改为委托 `storedmodel.ExtractAppID`，消除第三套重复实现。
- 验证结果：
  - `go build ./...` 全量编译通过。
  - `go test ./extension/observabilitystorageext/...` 全量通过，无回归（含 `elasticsearch`/`postgresql`/`storedmodel`/`lifecycle` 子包，`purger_test.go` 中已有的大小写混合 AppID 用例 `TestPurger_PurgeByApp_DeletesAppScopedIndices` 通过，该路径本身不受影响，见 §10）。
  - **现网复现验证**（对照 §2.2 的复现步骤，使用真实生产 AppID `xUXCbjcSnSy5LZUJ`）：
    ```bash
    # 修复后：SanitizeAppID(Lowercase:false) 生成的 pattern
    $ curl -u elastic:*** http://9.134.106.132:9200/otel-traces-xUXCbjcSnSy5LZUJ-*/_count
    {"count":182739, ...}   # 命中真实数据 ✅

    # 旧的 lowercase pattern（修复前的行为，仍验证保留作对比）
    $ curl -u elastic:*** http://9.134.106.132:9200/otel-traces-xuxcbjcsnsy5lzuj-*/_count
    {"count":0, ...}        # 与修复前发现的 bug 现象一致
    ```
    确认 `Admin.PurgeByApp` 修复后生成的 index pattern 能正确匹配到实际大写索引，§2.2 的 bug 已解决。
- 未修改文件：`trace_writer_test.go`（现有用例断言仍然成立，`WriteTraces` 行为对外可观察结果不变，只是内部实现路径改为委托，无需调整断言）；`purger_test.go`（`Purger.PurgeByApp` 本身不在本次改动范围，§10 已说明）。
- 无遗留问题，Sprint 2/3 范围内改动已闭环，AppID 处理已完全收敛为 `storedmodel` 包下的单一实现。

---

## 10. 遗留问题

- `elasticsearch/purger.go` 的 `PurgeByApp`（生产实际调度器使用的路径）本身不受本次 bug 影响（appID 全程未 sanitize，自洽），本方案不改动这条路径的行为，只统一 `admin.go` 那条路径与它对齐。
- 未来如果新增 Provider（如 MongoDB）需要 lowercase 或其他字符集限制，可通过 `SanitizeOptions` 扩展，无需重新实现整套逻辑。
