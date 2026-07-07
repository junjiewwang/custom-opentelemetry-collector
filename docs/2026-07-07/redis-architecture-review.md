# Redis 架构评审与重构方案

> 2026-07-07 | 状态：P0 实施中

---

## 一、问题

Pod `custom-otlp-collector` 定期出现 `read tcp → 9.134.105.246:6380: i/o timeout`，周期 ~30s。

### 5Why 根因链

```
L1:  read_timeout=3s 不够
L2:  reaper 每 30s 全表 SCAN+MGET
L3:  无 time-based 索引（ZSET）
L4:  Store 接口约束 ZSET→Memory 实现困难
L5:  Memory 先于 Redis 锁定接口
L6:  ListTasks 扫全部 key，只取 10 条——浪费 98%
L7:  历史 task 不清理——TTL=0 永不过期
L8:  担心 admin 查不到历史 task
L9:  无热/冷数据分层——全混在一起
L10: admin 和 reaper 共享 ListTasks——读写需求冲突
L11: 架构无读写路径分离
L12: Collector→Control Plane 演进脱轨，Arch 角色断层
```

### TCP 证据（/proc/net/tcp）

| 状态 | 数量 | 含义 |
|------|------|------|
| ESTABLISHED (retransmit) | 10+ | 连接正常但数据重传中 |
| TIME_WAIT | 20+ | 大量死连接堆积 |

---

## 二、Redis 存量盘点

10 个命名空间，同实例同 pool：

| 命名空间 | 用途 | 核心操作 | 数据量 |
|---------|------|---------|--------|
| `te:task:*` | 任务引擎 | SCAN/MGET/LPush/RPop | O(500) 🚨 |
| `te:q:*`/`te:result:*`/`te:group:*` | 队列/结果/组 | LPush/RPop/Set | O(500) |
| `te:node:*` | 引擎节点 | Set/Del/SMembers | O(10) |
| `otel:agents` | Agent 注册 | HSet/Get/Exists | O(100) |
| `otel:apps` | App 身份 | HSet/HGet | O(10) |
| `otel:services` | Service 管理 | SCAN/HGetAll | O(100) |
| `otel:instrumentation` | 插桩规则 | HSet/HGetAll | O(50) |
| `otel:notifications` | 通知 | HSet | O(100) |
| `otel:chunks` | 文件分片 | HSet/Pipeline | O(50) |
| `otel:ws_token` | WS Token | HSet | O(10) |
| `arthas:tunnel` | 隧道 | Pipeline/MGet | O(100) |

---

## 三、重构方案

### 核心思路：Fast Path —— 零接口变更

```
RedisStore.ListTasks(query):
  if query.Status == "running" && query.GroupID == "":
    → listRunningTasks(query)   ← O(logN) ZSET fast path
  else:
    → SCAN + MGET                ← O(N) fallback
```

### 设计原则

| 原则 | 对策 |
|------|------|
| OCP | Store 接口不变，RedisStore 内部自优化 |
| LSP | MemoryStore 保留原有实现（O(N) 但语义正确） |
| ISP | Fast path 只在 Status==Running 时触发 |
| SRP | ZSET 索引 + Fast path 都在 store_redis.go 内 |

### 数据结构

```
SaveTask       → TTL: 0
→ RUNNING      → Lua: ZADD te:running {startedAt} {taskID}
→ SUCCESS      → Lua: ZREM te:running + EXPIRE task 14d
→ FAILED       → Lua: ZREM te:running + EXPIRE task 14d
→ TIMEOUT      → Lua: ZREM te:running + EXPIRE task 14d

reaper: ZRANGEBYSCORE te:running 0 {now-timeout}  ← O(logN)
```

### 性能对比

| 操作 | 优化前 | 优化后 | 改善 |
|------|--------|--------|------|
| reaper 查询 | SCAN(3轮) + MGET(500) ~3s | ZRANGE(1轮) + MGET(10) ~5ms | 600x |
| 连接池 | 全量扫描占用 | 小查询秒回 | 大幅降低 |
| 历史数据 | 永不过期 | 终态 14d TTL | 空间回收 |

### P0 文件变更

| 文件 | 改动 | 行数 |
|------|------|------|
| `taskengine/store_redis.go` | Lua + ZADD/ZREM/EXPIRE | +15 |
| `taskengine/store_redis.go` | listRunningTasks fast path | +45 |
| `taskengine/store_redis.go` | runningKey + chunk MGET | +10 |

---

## 四、测试策略

```
Unit:    MemoryStore.ListTasks(Status=Running) → O(N) filter ✓
Contract: miniredis → ZSET 索引 ZADD/ZREM 验证
         miniredis → ListTasks fast path vs slow path 一致性
```

---

## 五、遗留

- P1: 连接池隔离（3 个 pool 按 workload 分离）
- P2: admin 冷查询走 ES（读写路径分离）
- P3: MemoryStore 加 LRU + capacity
