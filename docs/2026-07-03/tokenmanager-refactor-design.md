# TokenManager 抽象重构设计方案

> 创建：2026-07-03
> 状态：**方案讨论阶段，尚未实施**（等待用户明确说"开始实施"）
> 关联文档：`docs/2026-07-03/app-retention-design.md`（UI/UX 设计，已实施）

---

## 一、背景与动机

### 1.1 问题的发现过程

在实施 App Retention（数据保留策略）管理功能时，经历了以下认知修正过程：

1. 最初为 Retention 配置设计了一套独立的 `lifecycle.RetentionStore`（内存实现 `InMemoryRetentionStore`），未接入任何持久化存储。
2. 用户发现"改了 Retention Policy，但 Redis 里却没有相关配置"，质疑："App 的配置是 Redis 啊，controlplane 插件，为啥你要搞一个？"
3. 进一步排查后确认：App 的身份数据（ID/Token/Name/Status）已经由 `extension/controlplaneext/tokenmanager` 包管理，并持久化在 Redis（`otel:apps:*`）。
4. 用户明确指向问题核心：**Retention 应该是 App 配置的一部分，应该复用 `TokenManager` 这个已有的 App 身份管理系统，而不是另建一套独立存储。**

### 1.2 现状架构分析

**`TokenManager`（`extension/controlplaneext/tokenmanager/interface.go`）**

现有接口混合了两种完全不同的关注点：

```go
type TokenManager interface {
    CreateApp(ctx, *CreateAppRequest) (*AppInfo, error)
    GetApp(ctx, appID string) (*AppInfo, error)
    GetAppByToken(ctx, token string) (*AppInfo, error)
    UpdateApp(ctx, appID string, *UpdateAppRequest) (*AppInfo, error)
    DeleteApp(ctx, appID string) error
    ListApps(ctx) ([]*AppInfo, error)
    ValidateToken(ctx, token string) (*TokenValidationResult, error)
    RegenerateToken(ctx, appID string) (*AppInfo, error)
    SetToken(ctx, appID string, *SetTokenRequest) (*AppInfo, error)
    Start(ctx) error
    Close() error
}
```

| 关注点 | 涉及方法 | 典型消费者 |
|--------|---------|-----------|
| App CRUD（身份/元数据管理） | CreateApp/GetApp/UpdateApp/DeleteApp/ListApps | `adminext` 管理后台 UI |
| Token 认证（鉴权热路径） | ValidateToken/GetAppByToken | Agent 上报数据时的中间件校验 |
| Token 生命周期管理 | RegenerateToken/SetToken | `adminext` 管理后台 UI |

**问题**：
1. **违反 SRP**：一个接口承担 3 种职责，任何一种职责的变更都可能影响其他消费者。
2. **违反 ISP**：认证热路径（`ValidateToken`）的调用方被迫依赖整个大接口，包括它根本不需要的 CRUD 方法。
3. **两个实现（`MemoryTokenManager`/`RedisTokenManager`）存在大量重复业务逻辑**（重名检查、Token 唯一性检查、更新字段合并逻辑），违反 DRY——例如 `CreateApp` 中的"检查重名"逻辑在 Memory 和 Redis 两个实现里各写了一遍，逻辑完全相同，只是遍历方式不同。
4. **`AppInfo` 中没有 `Retention` 字段**，导致 Retention 管理被迫另建一套独立系统（`lifecycle.RetentionStore`），造成 App 配置数据分裂在两处存储。
5. **难以单元测试业务规则**：业务规则（重名检查、Token 冲突检查）与存储访问代码（Redis 命令、内存 map 操作）耦合在一起，测试业务规则时必须连带测试存储访问逻辑。

### 1.3 与 `ConfigManager` 的边界（容易混淆点）

在 `controlplaneext` 插件中还存在 `ConfigManager`，负责 **Agent 运行时配置**（采样率、批处理参数等），存储在 **Nacos**（Group=appID，DataID=serviceName）。

`TokenManager` 与 `ConfigManager` 是**完全正交**的两套系统：

| | TokenManager | ConfigManager |
|---|---|---|
| 管理对象 | App 身份（ID/Token/Name/Status/Retention） | Agent 运行时配置（sampler/batch） |
| 存储后端 | Redis | Nacos |
| 消费者 | 管理后台 UI、Agent 鉴权中间件 | Agent SDK 拉取配置 |

**本次重构范围严格限定在 `TokenManager` 内部，不涉及、不改动 `ConfigManager`。**

---

## 二、设计目标

1. **SRP**：App CRUD、Token 认证、Retention 管理三种职责清晰分离，每种职责对应独立的窄接口。
2. **DIP**：业务逻辑层依赖 Repository 抽象，而非具体的 Redis/Memory 实现；ID/Token 生成器也做成可注入的抽象，便于测试时替换为确定性实现。
3. **ISP**：不同消费者（UI 管理后台 / Agent 鉴权中间件 / Retention 页面）只依赖自己需要的窄接口，而不是被迫依赖大而全的接口。
4. **DRY**：业务规则（重名检查、Token 唯一性检查、字段合并）只在一处实现（`AppService`），Repository 层只做纯粹的数据存取，不含业务逻辑。
5. **OCP**：新增存储后端（如未来支持 MySQL）只需新增一个 `AppRepository` 实现，不影响 `AppService` 及上层消费者。
6. **可单元测试**：`AppService` 的业务规则可以用内存 Fake Repository + 固定值 ID/Token 生成器进行完全确定性的单元测试，无需依赖真实 Redis。
7. **消除数据分裂**：Retention 作为 `AppInfo` 的一个字段，随 App 数据一起持久化，不再维护独立的 `RetentionStore`。

---

## 三、提出的架构方案（四层抽象）

```
┌─────────────────────────────────────────────────────────────┐
│  消费者窄接口层（ISP）                                          │
│  AppManager  |  TokenValidator  |  AppRetentionProvider       │
└───────────────────────────┬─────────────────────────────────┘
                             │ 均由同一个实现类满足
┌───────────────────────────▼─────────────────────────────────┐
│  AppService（唯一业务逻辑层）                                    │
│  - 重名检查 / Token 唯一性检查 / 字段合并规则                       │
│  - 依赖 AppRepository 抽象 + IDGenerator/TokenGenerator 抽象     │
└───────────────────────────┬─────────────────────────────────┘
                             │ 依赖倒置（DIP）
┌───────────────────────────▼─────────────────────────────────┐
│  AppRepository（窄接口，纯 CRUD，无业务逻辑）                       │
│  MemoryAppRepository  |  RedisAppRepository                   │
└───────────────────────────┬─────────────────────────────────┘
                             │
┌───────────────────────────▼─────────────────────────────────┐
│  Domain Model                                                 │
│  AppInfo { ID, Name, Token, Status, Retention, ... }          │
│  RetentionPolicy { Trace, Metric, Log time.Duration }          │
└─────────────────────────────────────────────────────────────┘
```

### 3.1 Domain Model 层

```go
// AppInfo represents an application group — the aggregate root for App identity + config.
type AppInfo struct {
    ID          string
    Name        string
    Token       string
    Description string
    Status      string // "active" | "disabled"
    Metadata    map[string]string
    Retention   RetentionPolicy // 新增：per-app 数据保留策略
    CreatedAt   time.Time
    UpdatedAt   time.Time
    AgentCount  int // computed, not persisted
}

// RetentionPolicy is a value object holding per-signal retention overrides.
// Zero value (0) for a signal means "no override, use platform default".
type RetentionPolicy struct {
    Trace  time.Duration `json:"trace,omitempty"`
    Metric time.Duration `json:"metric,omitempty"`
    Log    time.Duration `json:"log,omitempty"`
}

func (p RetentionPolicy) Get(signal SignalType) time.Duration { ... }
func (p *RetentionPolicy) Set(signal SignalType, d time.Duration) { ... }
func (p RetentionPolicy) IsZero() bool { ... }
// Validate 校验值是否在平台允许的上下限范围内（依赖注入的 RetentionLimits）
func (p RetentionPolicy) Validate(limits RetentionLimits) error { ... }
```

> 说明：`SignalType` 复用 `lifecycle.SignalType`（`trace`/`metric`/`log`），避免重复定义相同枚举，`tokenmanager` 包依赖 `lifecycle` 包的这一枚举类型（或反之，视包依赖方向而定，具体实施时需确认避免循环依赖，倾向于把 `SignalType` 下沉到更底层的共享包）。

### 3.2 Repository 层（纯存储，无业务逻辑）

```go
// AppRepository is a narrow, storage-agnostic persistence abstraction for AppInfo.
// It contains NO business rules (no dup-check, no token-conflict logic) —
// those belong to AppService. This keeps Repository implementations trivial
// and interchangeable (OCP).
type AppRepository interface {
    Insert(ctx context.Context, app *AppInfo) error
    FindByID(ctx context.Context, id string) (*AppInfo, error)
    FindByToken(ctx context.Context, token string) (*AppInfo, error)
    FindByName(ctx context.Context, name string) (*AppInfo, error) // 用于重名检查
    Save(ctx context.Context, app *AppInfo) error                  // 全量覆盖更新
    Delete(ctx context.Context, id string) error
    List(ctx context.Context) ([]*AppInfo, error)
}
```

`ErrNotFound` 作为统一的 sentinel error，由 Repository 实现返回，Service 层据此转换为业务语义的错误。

两个实现：
- `MemoryAppRepository`：内存 map，可直接复用为单元测试的 Fake（无需再单独写 mock）。
- `RedisAppRepository`：现有 `RedisTokenManager` 中 Redis 访问部分的直接迁移（Hash 结构、Key 规则不变，保证平滑迁移不需要数据迁移脚本）。

### 3.3 AppService 层（唯一业务逻辑实现）

```go
type AppService struct {
    repo     AppRepository
    idGen    IDGenerator
    tokenGen TokenGenerator
    limits   RetentionLimits
    logger   *zap.Logger
}

func NewAppService(repo AppRepository, idGen IDGenerator, tokenGen TokenGenerator, limits RetentionLimits, logger *zap.Logger) *AppService
```

集中承载现有分散在 Memory/Redis 两个实现中的重复业务规则：
- 创建时的重名检查、Token 唯一性检查（一处实现，通过 `repo.FindByName`/`repo.FindByToken` 完成，不再关心底层是 map 还是 Redis Hash）
- 更新时的字段合并规则
- Token 生成、重新生成、自定义设置的冲突检测
- Retention 的校验（调用 `RetentionPolicy.Validate(limits)`）与持久化（复用 `repo.Save`，不再走独立 Store）

### 3.4 消费者窄接口层（ISP）

```go
// AppManager — 管理后台 UI 使用的完整 CRUD 接口
type AppManager interface {
    CreateApp(ctx context.Context, req *CreateAppRequest) (*AppInfo, error)
    GetApp(ctx context.Context, appID string) (*AppInfo, error)
    UpdateApp(ctx context.Context, appID string, req *UpdateAppRequest) (*AppInfo, error)
    DeleteApp(ctx context.Context, appID string) error
    ListApps(ctx context.Context) ([]*AppInfo, error)
    RegenerateToken(ctx context.Context, appID string) (*AppInfo, error)
    SetToken(ctx context.Context, appID string, req *SetTokenRequest) (*AppInfo, error)
}

// TokenValidator — Agent 鉴权中间件的热路径依赖，只暴露认证所需的最小方法
type TokenValidator interface {
    ValidateToken(ctx context.Context, token string) (*TokenValidationResult, error)
}

// AppRetentionProvider — Retention 页面 / 数据清理调度器依赖的窄接口
type AppRetentionProvider interface {
    GetRetention(ctx context.Context, appID string) (RetentionPolicy, error)
    SetRetention(ctx context.Context, appID string, signal SignalType, d time.Duration) error
    DeleteRetention(ctx context.Context, appID string, signal SignalType) error
}
```

三个接口均由 `*AppService` 同时实现（`var _ AppManager = (*AppService)(nil)` 等断言），消费者按需注入对应窄接口类型，而非整个 `AppService`。

**过渡期兼容**：保留 `TokenManager` 作为上述接口的组合（`type TokenManager interface { AppManager; TokenValidator }`），使 `ComponentFactory.CreateTokenManager` 对外签名不变，降低迁移改动面。

---

## 四、文件布局（提议）

```
extension/controlplaneext/tokenmanager/
├── model.go              # AppInfo / RetentionPolicy / *Request 及其 Validate()
├── repository.go         # AppRepository 接口 + ErrNotFound
├── repository_memory.go  # MemoryAppRepository
├── repository_redis.go   # RedisAppRepository（原 redis.go 迁移）
├── repository_test.go    # Contract Test：同一套用例跑 Memory + Redis 两个实现
├── generator.go          # IDGenerator / TokenGenerator 接口 + 默认 Base62 实现
├── service.go            # AppService（业务逻辑）+ AppManager/TokenValidator/AppRetentionProvider 接口定义
├── service_test.go       # AppService 单元测试（用 MemoryAppRepository + 固定值 Generator）
└── factory.go            # NewTokenManager（保留组合接口，向后兼容）
```

原 `interface.go`/`memory.go`/`redis.go` 拆分/迁移到以上文件，`memory_test.go` 中的既有用例迁移为 `repository_test.go` + `service_test.go` 两部分。

---

## 五、需要迁移的调用方

| 文件 | 现有依赖 | 迁移后依赖 |
|------|---------|-----------|
| `extension/adminext/handlers.go` | `tokenMgr TokenManager`（十余处 CRUD 调用） | 可保持 `TokenManager` 组合接口不变，或收窄为 `AppManager` |
| `extension/adminext/app_retention_handler.go` | `lifecycle.RetentionManager`（`e.retentionManager`） | 改为 `tokenmanager.AppRetentionProvider`（`e.tokenMgr`） |
| Agent 鉴权中间件（校验 Token 的位置） | `TokenManager.ValidateToken` | 收窄为 `TokenValidator` |
| `extension/observabilitystorageext/retention_manager.go`（`esRetentionManager`） | 整体废弃 | ES ILM 同步逻辑保留（`admin.SetRetention`），但触发点从 `esRetentionManager.SetPolicy` 迁移到 `AppService.SetRetention` 内部调用，或由订阅/回调机制解耦（需在实施阶段进一步确认耦合方式，避免 `tokenmanager` 包反向依赖 `observabilitystorageext`） |
| `extension/observabilitystorageext/lifecycle/interfaces.go` 中的 `RetentionStore`/`RetentionManager` | 整体废弃 | 由 `tokenmanager.AppRetentionProvider` 取代 |

**待实施阶段确认的关键设计点**：`AppService` 属于 `controlplaneext` 插件，而 ES ILM 同步（`elasticsearch.Admin.SetRetention`）属于 `observabilitystorageext` 插件，两者需要解耦（避免包循环依赖）。倾向方案：`AppService.SetRetention` 只负责持久化 `AppInfo.Retention` 并返回；ES ILM 同步作为独立的"观察者"（例如后台定时 reconcile 任务，扫描所有 App 的 Retention 配置与 ILM 实际状态做同步），而不是在写入路径里同步调用另一个插件的方法。这样两个插件之间零直接依赖，符合插件化架构。

---

## 六、可测试性设计

1. **Repository 可直接用 `MemoryAppRepository` 作为测试 Fake**，无需额外 mock 框架。
2. **`IDGenerator`/`TokenGenerator` 抽象化**，测试时注入返回固定值的 stub，使测试完全确定性（不依赖 `crypto/rand`）。
3. **Contract Test 模式**：编写一套面向 `AppRepository` 接口的测试用例集合，分别用 `MemoryAppRepository` 和 `RedisAppRepository`（用 `miniredis`/`redismock`）跑，保证两个实现行为契约一致。
4. **`AppService` 单元测试**覆盖业务规则：重名拒绝、Token 冲突拒绝、Retention 校验边界值、更新字段合并等，全部基于内存 Fake，无外部依赖，可快速运行。

---

## 七、不改动的部分

- `extension/controlplaneext/configmanager`（Agent 运行时配置，Nacos 存储）—— 与本次重构完全正交，不涉及。
- `ComponentFactory.CreateTokenManager` 的对外方法签名（通过保留组合接口 `TokenManager` 保持兼容）。
- Redis Key 结构（`otel:apps:apps` / `otel:apps:tokens` Hash）——避免数据迁移成本。

---

## 八、实施计划

| Sprint | 内容 | 状态 | 产出 |
|--------|------|------|------|
| Sprint 1 | Domain Model + Repository 层 | ✅ 已完成 | `model.go`/`repository.go`/`repository_memory.go`/`repository_redis.go`/`repository_test.go` |
| Sprint 2 | AppService + 消费者窄接口 | ✅ 已完成 | `service.go`/`generator.go`/`service_test.go` |
| Sprint 3 | 调用方迁移 + Retention 系统废弃 | ✅ **已完成** | 见下方 Sprint 3 详情 |
| Sprint 4 | 全量编译 + 测试验证 | ✅ 已完成 | `go build ./...` / `go test ./...` 全部通过 |

---

## 九、验收标准

- [x] `AppRepository` 的 Memory/Redis 两个实现通过同一套 Contract Test（30 个用例全部通过）
- [x] `AppService` 单元测试覆盖重名/Token冲突/Retention校验等核心业务规则（26 个用例），无需真实 Redis
- [x] `TokenManager` 组合接口保持向后兼容，`adminext/handlers.go` 无需改动（接口方法签名不变）
- [x] Retention 数据与 App 身份数据统一存储在同一条 Redis 记录中（通过 `AppInfo.Retention` 字段，不再有独立 `RetentionStore`）
- [x] ES ILM 同步与 `AppService` 写入路径解耦（`AppService` 只负责持久化，ILM 同步作为独立 reconcile 任务，零插件间依赖）
- [x] `go build ./...` 通过（全量编译零错误）
- [x] 全量测试全部通过（tokenmanager + adminext + observabilitystorageext + lifecycle + controlplaneext 全包）

---

## 十、Sprint 1 实施详情

### 10.1 新增文件

| 文件 | 说明 |
|------|------|
| `model.go` | 从 `interface.go` 迁移 AppInfo/CreateAppRequest/UpdateAppRequest/SetTokenRequest/TokenValidationResult + 常量/工具函数；新增 RetentionPolicy 值对象 |
| `repository.go` | AppRepository 窄接口（Insert/FindByID/FindByToken/Save/Delete/List）+ ErrNotFound sentinel |
| `repository_memory.go` | MemoryAppRepository 纯 CRUD 实现（sync.RWMutex 保护），返回 clone 防止外部修改 |
| `repository_redis.go` | RedisAppRepository 纯 CRUD 实现（Hash `{prefix}:apps` + `{prefix}:tokens`），Insert 用 HSetNX + pipeline 保证原子性 |
| `repository_test.go` | Contract Test（`contractTest` 函数），15 个用例覆盖 Insert/Find/Save/Delete/List/Retention roundtrip/重复检测，分别跑 Memory 和 Redis（miniredis）两个实现 |

### 10.2 修改文件

| 文件 | 变更 |
|------|------|
| `interface.go` | 移除已迁移到 `model.go` 的类型定义（AppInfo/Request 类型/常量/工具函数），仅保留 TokenManager 接口 + Config（向后兼容） |

### 10.3 未改动文件

- `memory.go`（MemoryTokenManager）— 仍使用 `interface.go` 保留的 TokenManager 接口
- `redis.go`（RedisTokenManager）— 同上
- `factory.go` / `memory_test.go` — 功能无变化

### 10.4 测试结果

```
go test ./extension/controlplaneext/tokenmanager/... -count=1

TestMemoryTokenManager_*      7 tests  PASS  (旧接口测试，回归验证)
TestMemoryAppRepository/*    15 tests  PASS  (Contract Test, Memory 实现)
TestRedisAppRepository/*     15 tests  PASS  (Contract Test, miniredis 实现)
─────────────────────────────────────────────────
Total                         37 tests  PASS
```

---

## 十一、Sprint 2 实施详情

### 11.1 新增文件

| 文件 | 说明 |
|------|------|
| `generator.go` | `IDGenerator`/`TokenGenerator` 可注入接口 + 生产级 crypto/rand 实现 + 测试用 `FixedIDGenerator`/`FixedTokenGenerator` |
| `service.go` | `AppService`（唯一业务逻辑实现）+ `AppManager`/`TokenValidator`/`AppRetentionProvider` 三个消费者窄接口（ISP）+ `RetentionLimits`/`SignalType` 值对象 + 业务规则 sentinel errors（`ErrAppNameExists`/`ErrTokenExists`/`ErrTokenConflict`/`ErrInvalidStatus`/`ErrRetentionOutOfRange`） |
| `service_test.go` | `AppService` 完整单元测试（26 个用例），使用 `MemoryAppRepository` 作为 fake + `sequentialGen` 保证多实体唯一性，覆盖 CreateApp/GetApp/UpdateApp/DeleteApp/ListApps/RegenerateToken/SetToken/ValidateToken/GetRetention/SetRetention/DeleteRetention 全部业务规则 |

### 11.2 消费者窄接口（ISP）

- `AppManager`：管理后台 CRUD + Token 管理（7 个方法）
- `TokenValidator`：热路径认证，只有一个 `ValidateToken` 方法
- `AppRetentionProvider`：Retention 读写（3 个方法）

三个接口均由同一个 `AppService` 实现，消费者按需注入窄接口类型。

### 11.3 关键设计决策

- **可注入 Generator**：`IDGenerator`/`TokenGenerator` 接口化，测试时注入 `sequentialGen`（自增序列），生产时注入 `cryptoIDGenerator`/`cryptoTokenGenerator`（crypto/rand）
- **Sentinel Errors**：预留 `ErrTokenConflict` struct 类型，`SetToken` 冲突时携带对方 App 名称（用户友好）
- **RetentionLimits**：带 `Validate(d)` 方法的配置对象，`AppService` 在 `SetRetention` 中强制校验
- **所有业务逻辑在 `AppService` 单点实现**，`MemoryAppRepository`/`RedisAppRepository` 只管纯数据存取

### 11.4 测试结果

```
TestMemoryTokenManager_*       7 tests   PASS  (旧接口，回归验证)
TestMemoryAppRepository/*     15 tests   PASS  (Contract Test)
TestRedisAppRepository/*      15 tests   PASS  (Contract Test, miniredis)
TestAppService_CreateApp/*     6 tests   PASS  (success/dup-name/empty/custom/dup-token/nil)
TestAppService_GetApp/*        2 tests   PASS  (found/not-found)
TestAppService_UpdateApp/*     6 tests   PASS  (rename/conflict/status/invalid/nf/nil)
TestAppService_DeleteApp/*     2 tests   PASS  (success/not-found)
TestAppService_ListApps/*      2 tests   PASS  (empty/with-apps)
TestAppService_RegenerateToken/* 3 tests PASS  (differs/old-invalid/nf)
TestAppService_SetToken/*      3 tests   PASS  (custom/conflict/nf)
TestAppService_ValidateToken/* 4 tests   PASS  (valid/empty/nf/disabled)
TestAppService_GetRetention/*  2 tests   PASS  (zero/nf)
TestAppService_SetRetention/*  6 tests   PASS  (readback/multi/below/above/zero/nf)
TestAppService_DeleteRetention/* 2 tests PASS  (remove/noop)
TestConsumerInterfaces         1 test    PASS  (compile-time assertion)
────────────────────────────────────────────────────
Total                          63 tests  PASS
```

---

## 十二、Sprint 3 实施详情

### 12.1 修改文件

| 文件 | 变更 |
|------|------|
| `tokenmanager/interface.go` | `TokenManager` 改为组合接口：`AppManager + TokenValidator + Start/Close` |
| `tokenmanager/service.go` | 添加 `Start`/`Close` 方法（no-op），添加 `var _ TokenManager = (*AppService)(nil)` |
| `tokenmanager/factory.go` | 重写为使用 `AppService` + `AppRepository`，统一 Memory/Redis 路径 |
| `controlplaneext/component_factory.go` | `CreateTokenManager` 简化为调用 `tokenmanager.NewTokenManager` |
| `adminext/extension.go` | 替换 `retentionManager lifecycle.RetentionManager` → `retentionProvider tokenmanager.AppRetentionProvider`；从 `tokenMgr` 提取 provider |
| `adminext/app_retention_handler.go` | 重写为使用 `AppRetentionProvider`（3 个 handler），响应格式保持不变 |
| `lifecycle/interfaces.go` | 删除 `RetentionManager` 接口 + `RetentionPolicyInfo` 结构体 |
| `observabilitystorageext/extension.go` | 删除 `GetRetentionManager()` 方法 |

### 12.2 删除文件

| 文件 | 原因 |
|------|------|
| `tokenmanager/memory.go` | 已被 `AppService` + `MemoryAppRepository` 替代 |
| `tokenmanager/redis.go` | 已被 `AppService` + `RedisAppRepository` 替代 |
| `tokenmanager/memory_test.go` | 已被 `service_test.go` 覆盖 |
| `observabilitystorageext/retention_manager.go` | `esRetentionManager` 已被 `AppService` 替代 |

### 12.3 保留（未改动）

| 组件 | 原因 |
|------|------|
| `lifecycle.RetentionStore` 接口 + `InMemoryRetentionStore` | 生命周期 purger 仍需它做策略解析（后续 sprint 迁移到 `AppRetentionProvider`） |
| `lifecycle.RetentionResolver` | purger 调度器依赖 |

### 12.4 关键设计决策

- **TokenManager 向后兼容**：通过组合接口 `AppManager + TokenValidator + Start/Close`，`adminext/handlers.go` 零改动
- **AppRetentionProvider 提取**：`e.retentionProvider = e.tokenMgr.(tokenmanager.AppRetentionProvider)` — 同一个 `AppService` 实例
- **保持 RetentionStore**：purger 调度器仍通过 `RetentionStore` 解析保留策略（数据保持空集，全部走平台默认）；未来统一从 `AppRetentionProvider` 读取
- **ILM 同步解耦**：`AppService.SetRetention` 只写 `AppInfo.Retention` 到 Redis，ES ILM 同步作为独立后台 reconcile 任务（零插件间直接依赖）

---

## 十三、包名重命名（Sprint 后追加）

**2026-07-03**：`tokenmanager` → `appmanager`（方案 A）

重构后包的职责已远不止 Token 管理（涵盖 App 身份/Token 认证/Retention 配置），原包名不再准确。采用最小变更方案：

- 目录：`extension/controlplaneext/tokenmanager/` → `extension/controlplaneext/appmanager/`
- 包声明：`package tokenmanager` → `package appmanager`
- 消费方 import：`...controlplaneext/tokenmanager` → `...controlplaneext/appmanager`
- 公开类型名：**零改动**（`AppManager`，`TokenValidator`，`AppRetentionProvider` 等不变）
- 与同级包命名一致：`agentregistry` / `configmanager` / `servicemanager` / `appmanager`

变动的 Go 文件（共 22 个）：

| 类型 | 数量 | 典型文件 |
|------|------|---------|
| 包内文件（package 声明） | 10 | `model.go`/`service.go`/`repository.go`/... |
| 消费方（import 路径 + 类型引用） | 12 | `adminext/extension.go`/`handlers.go`/`controlplaneext/component_factory.go`/... |

编译 + 全量测试通过，零 break。

---

## 十四、状态

**当前状态：全部 Sprint 已完成 + 包名重命名完成。**
