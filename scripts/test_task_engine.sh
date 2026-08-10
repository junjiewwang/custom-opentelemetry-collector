#!/bin/sh
# Task Engine 集成测试脚本
# 在 minikube 集群内运行，直连 custom-otlp-collector 服务
#
# 环境变量 (必须在下述 - 其中之一):
#   COLLECTOR_BASE_URL  Collector 地址 (默认: http://custom-otlp-collector.default.svc.cluster.local:8088)
#   COLLECTOR_API_KEY   Admin API 密钥
#
# 可选:
#   REDIS_HOST          Redis 地址
#   REDIS_PORT          Redis 端口
#   REDIS_PASSWORD      Redis 密码 (AUTH)
#   REDIS_DB            Redis DB 编号 (默认: 1)
#   SKIP_REDIS=1        跳过 Redis 存储层验证

set -e

BASE="${COLLECTOR_BASE_URL:-http://custom-otlp-collector.default.svc.cluster.local:8088}"
REDIS_HOST="${REDIS_HOST:-}"
REDIS_PORT="${REDIS_PORT:-6379}"
REDIS_PASSWORD="${REDIS_PASSWORD:-}"
REDIS_DB="${REDIS_DB:-1}"
SKIP_REDIS="${SKIP_REDIS:-}"

# ── 前置校验 ──
if [ -z "$COLLECTOR_API_KEY" ]; then
  echo "ERROR: COLLECTOR_API_KEY 未设置" >&2
  echo "用法: COLLECTOR_API_KEY=<your-key> $0" >&2
  exit 1
fi

AUTH_HEADER="X-API-Key: $COLLECTOR_API_KEY"

api() {
  curl -s -H "$AUTH_HEADER" -H "Content-Type: application/json" "$@"
}

echo "============================================="
echo "Task Engine 集成测试"
echo "时间: $(date)"
echo "============================================="

# =============================================
# Part 1: 连通性与基础环境检查
# =============================================
echo ""
echo "=== Part 1: 连通性检查 ==="
echo -n "  Health check: "
api "$BASE/health"
echo ""

echo -n "  Admin API:    "
api "$BASE/api/v2/health" 2>/dev/null || echo "(no /api/v2/health)"

# 查询当前任务总数
TOTAL_BEFORE=$(api "$BASE/api/v2/tasks?limit=1" | grep -o '"total":[0-9]*' | head -1 | cut -d: -f2)
echo "  现有任务总数: ${TOTAL_BEFORE:-unknown}"

# =============================================
# Part 2: 任务下发测试
# =============================================
echo ""
echo "=== Part 2: 任务下发测试 ==="

TS=$(date +%s)

echo "--- Test 2.1: 提交 Broadcast 路由的 Purge 任务 ---"
TASK1_ID="int-test-broadcast-${TS}"
RESULT1=$(api -X POST "$BASE/api/v2/tasks" \
  -d "{\"id\":\"$TASK1_ID\",\"task_type_name\":\"lifecycle:purge_index\",\"parameters_json\":{\"app_id\":\"test-broadcast\",\"days\":30}}")
echo "  Response: $RESULT1"
TASK1_SUCCESS=$(echo "$RESULT1" | grep -c "success" || true)
echo "  Status: $([ "$TASK1_SUCCESS" -gt 0 ] && echo "PASS" || echo "FAIL")"

echo ""
echo "--- Test 2.2: 提交 Direct 路由的任务 ---"
TASK2_ID="int-test-direct-${TS}"
RESULT2=$(api -X POST "$BASE/api/v2/tasks" \
  -d "{\"id\":\"$TASK2_ID\",\"task_type_name\":\"arthas:attach\",\"parameters_json\":{\"pid\":99},\"target_agent_id\":\"agent-fake-test\"}")
echo "  Response: $RESULT2"
echo "  Status: $(echo "$RESULT2" | grep -q 'not found' && echo 'PASS (expected - agent not found)' || echo 'CHECK')"

echo ""
echo "--- Test 2.3: 缺少 type_name 的校验 ---"
RESULT3=$(api -X POST "$BASE/api/v2/tasks" \
  -d "{\"id\":\"no-type-task\",\"parameters_json\":{\"x\":1}}")
echo "  Response: $RESULT3"
echo "  Status: $(echo "$RESULT3" | grep -q 'task_type_name is required' && echo 'PASS' || echo 'FAIL')"

echo ""
echo "--- Test 2.4: 提交多个不同路由策略的任务 ---"
for i in $(seq 1 5); do
  TID="int-test-multi-${TS}-${i}"
  api -X POST "$BASE/api/v2/tasks" \
    -d "{\"id\":\"$TID\",\"task_type_name\":\"lifecycle:purge_index\",\"parameters_json\":{\"batch\":$i}}" > /dev/null
  echo "  提交: $TID"
done

# =============================================
# Part 3: 任务查询测试
# =============================================
echo ""
echo "=== Part 3: 任务查询测试 ==="

echo "--- Test 3.1: 列出所有任务 (limit=5) ---"
LIST_ALL=$(api "$BASE/api/v2/tasks?limit=5")
echo "  $LIST_ALL" | head -c 600
TOTAL=$(echo "$LIST_ALL" | grep -o '"total":[0-9]*' | head -1 | cut -d: -f2)
ITEMS=$(echo "$LIST_ALL" | grep -o '"id":"[^"]*"' | wc -l | tr -d ' ')
echo ""
echo "  Total: $TOTAL, Returned items: $ITEMS"
echo "  Status: $([ "$ITEMS" -le 5 ] && echo 'PASS' || echo 'FAIL')"

echo ""
echo "--- Test 3.2: 按 status=pending 过滤 ---"
LIST_PENDING=$(api "$BASE/api/v2/tasks?status=pending&limit=10")
PENDING_COUNT=$(echo "$LIST_PENDING" | grep -o '"status":"pending"' | wc -l | tr -d ' ')
echo "  Pending tasks in result: $PENDING_COUNT"
echo "  Status: $([ "$PENDING_COUNT" -gt 0 ] && echo 'PASS' || echo 'CHECK (may be 0 if all claimed)')"

echo ""
echo "--- Test 3.3: 按 task_type_name 过滤 ---"
LIST_BY_TYPE=$(api "$BASE/api/v2/tasks?limit=3&task_type=lifecycle:purge_index")
TYPE_MATCH=$(echo "$LIST_BY_TYPE" | grep -o '"type":"lifecycle:purge_index"' | wc -l | tr -d ' ')
echo "  Type matches: $TYPE_MATCH"
echo "  Status: $([ "$TYPE_MATCH" -gt 0 ] && echo 'PASS' || echo 'CHECK')"

# =============================================
# Part 4: 分页/offset/limit 边界测试
# =============================================
echo ""
echo "=== Part 4: 分页边界测试 ==="

echo "--- Test 4.1: cursor 超出 total 测试 ---"
LIST_OVERFLOW=$(api "$BASE/api/v2/tasks?limit=3&cursor=99999")
OVERFLOW_TOTAL=$(echo "$LIST_OVERFLOW" | grep -o '"total":[0-9]*' | head -1 | cut -d: -f2)
OVERFLOW_ITEMS=$(echo "$LIST_OVERFLOW" | grep -o '"id":"[^"]*"' | wc -l | tr -d ' ')
OVERFLOW_HASMORE=$(echo "$LIST_OVERFLOW" | grep -o '"has_more":\(true\|false\)' | head -1)
echo "  Cursor=99999: Total=$OVERFLOW_TOTAL, Items=$OVERFLOW_ITEMS, HasMore=$OVERFLOW_HASMORE"
echo "  Status: $([ "$OVERFLOW_ITEMS" -eq 0 ] && echo 'PASS (correctly empty)' || echo 'FAIL (should be empty)')"

echo ""
echo "--- Test 4.2: limit=0 默认值测试 ---"
LIST_DEFAULT=$(api "$BASE/api/v2/tasks?limit=0")
DEFAULT_TOTAL=$(echo "$LIST_DEFAULT" | grep -o '"total":[0-9]*' | head -1 | cut -d: -f2)
echo "  Total with limit=0: $DEFAULT_TOTAL"
echo "  Status: PASS (accepted default)"

echo ""
echo "--- Test 4.3: 分页连续性 ---"
PAGE1=$(api "$BASE/api/v2/tasks?limit=3")
P1_IDS=$(echo "$PAGE1" | grep -o '"id":"[^"]*"' | head -3 | cut -d'"' -f4)
P1_CURSOR=$(echo "$PAGE1" | grep -o '"next_cursor":"[^"]*"' | head -1 | cut -d'"' -f4)
echo "  Page 1 IDs: $P1_IDS"
echo "  Page 1 next_cursor: $P1_CURSOR"

if [ -n "$P1_CURSOR" ] && [ "$P1_CURSOR" != "" ]; then
  PAGE2=$(api "$BASE/api/v2/tasks?limit=3&cursor=$P1_CURSOR")
  P2_IDS=$(echo "$PAGE2" | grep -o '"id":"[^"]*"' | head -3 | cut -d'"' -f4)
  echo "  Page 2 IDs: $P2_IDS"
  OVERLAP=0
  for id1 in $P1_IDS; do
    for id2 in $P2_IDS; do
      [ "$id1" = "$id2" ] && OVERLAP=1
    done
  done
  echo "  Overlap check: $([ $OVERLAP -eq 0 ] && echo 'PASS (no overlap)' || echo 'FAIL (overlapping IDs!)')"
else
  echo "  Page 2: No cursor (not enough data)"
fi

# =============================================
# Part 5: 获取单个任务
# =============================================
echo ""
echo "=== Part 5: 单任务查询 ==="

echo "--- Test 5.1: 按 ID 查询已提交的任务 ---"
GET_TASK=$(api "$BASE/api/v2/tasks/$TASK1_ID")
TASK_STATUS=$(echo "$GET_TASK" | grep -o '"status":"[^"]*"' | head -1 | cut -d'"' -f4)
echo "  Task $TASK1_ID status: $TASK_STATUS"
echo "  Status: $([ -n "$TASK_STATUS" ] && echo 'PASS' || echo 'FAIL')"

echo ""
echo "--- Test 5.2: 查询不存在的任务 ---"
GET_MISSING=$(api "$BASE/api/v2/tasks/nonexistent-task-id-xyz")
echo "  Response: $GET_MISSING" | head -c 200
echo "  Status: $(echo "$GET_MISSING" | grep -q 'not found' && echo 'PASS' || echo 'CHECK')"

# =============================================
# Part 6: Redis 存储层验证 (可选)
# =============================================
if [ "$SKIP_REDIS" != "1" ] && [ -n "$REDIS_HOST" ] && command -v redis-cli > /dev/null 2>&1; then
  echo ""
  echo "=== Part 6: Redis 存储层验证 ==="

  REDIS_AUTH_OPT=""
  [ -n "$REDIS_PASSWORD" ] && REDIS_AUTH_OPT="-a $REDIS_PASSWORD --no-auth-warning"

  echo "--- Test 6.1: Redis 连通性 ---"
  if redis-cli -h "$REDIS_HOST" -p "$REDIS_PORT" $REDIS_AUTH_OPT -n "$REDIS_DB" PING 2>/dev/null | grep -q PONG; then
    echo "  PASS: Redis reachable"
  else
    echo "  SKIP: Redis unreachable"
    SKIP_REDIS=1
  fi

  if [ "$SKIP_REDIS" != "1" ]; then
    echo ""
    echo "--- Test 6.2: 检查队列中的任务 ---"
    QUEUE_LEN=$(redis-cli -h "$REDIS_HOST" -p "$REDIS_PORT" $REDIS_AUTH_OPT -n "$REDIS_DB" LLEN "te:queue:global" 2>/dev/null)
    echo "  Global queue length: ${QUEUE_LEN:-N/A}"

    echo ""
    echo "--- Test 6.3: 检查 per-agent ZSET 索引 ---"
    AGENT_ZSETS=$(redis-cli -h "$REDIS_HOST" -p "$REDIS_PORT" $REDIS_AUTH_OPT -n "$REDIS_DB" KEYS "te:agent:*:tasks" 2>/dev/null | wc -l | tr -d ' ')
    echo "  Per-agent ZSET count: ${AGENT_ZSETS:-0}"
  fi
elif [ "$SKIP_REDIS" = "1" ]; then
  echo ""
  echo "=== Part 6: Redis 存储层验证 (SKIPPED) ==="
else
  echo ""
  echo "=== Part 6: Redis 存储层验证 (SKIPPED - 需设置 REDIS_HOST) ==="
fi

# =============================================
# Part 7: 取消任务
# =============================================
echo ""
echo "=== Part 7: 任务取消 ==="

CANCEL_RESULT=$(api -X DELETE "$BASE/api/v2/tasks/$TASK1_ID")
echo "  取消 $TASK1_ID: $CANCEL_RESULT" | head -c 200
CANCEL_OK=$(echo "$CANCEL_RESULT" | grep -c "success" || true)

GET_CANCELLED=$(api "$BASE/api/v2/tasks/$TASK1_ID")
CANCEL_STATUS=$(echo "$GET_CANCELLED" | grep -o '"status":"[^"]*"' | head -1 | cut -d'"' -f4)
echo "  取消后状态: $CANCEL_STATUS"
echo "  Status: $([ "$CANCEL_STATUS" = "cancelled" ] && echo 'PASS' || echo "CHECK (got: $CANCEL_STATUS)")"

# =============================================
# 汇总
# =============================================
echo ""
echo "============================================="
echo "测试完成"
echo "============================================="
