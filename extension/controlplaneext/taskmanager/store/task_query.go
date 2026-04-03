package store

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

const maxTaskListPageSize = 200

// TaskListQuery defines the read-model query for task list endpoints.
type TaskListQuery struct {
	Statuses    []model.TaskStatus
	AppID       string
	ServiceName string
	AgentID     string
	TaskType    string
	Limit       int
	Cursor      string
}

// TaskListPage represents one page of task list results.
type TaskListPage struct {
	Items      []*TaskInfo
	NextCursor string
	HasMore    bool
}

// seekCursor 表示基于 (created_at_millis, task_id) 的 seek 游标。
// 查询时从 LastScore 开始向前（倒序）取数据，跳过 LastID 及之前的记录。
type seekCursor struct {
	LastScore int64  `json:"s"` // 上一页最后一条的 created_at_millis
	LastID    string `json:"id"` // 上一页最后一条的 task_id（用于同分值去重）
}

func normalizeTaskListQuery(query TaskListQuery) TaskListQuery {
	if query.Limit < 0 {
		query.Limit = 0
	}
	if query.Limit > maxTaskListPageSize {
		query.Limit = maxTaskListPageSize
	}
	return query
}

// parseSeekCursor 解析 cursor 字符串。
// 支持三种格式：
//  1. 空字符串 → 第一页
//  2. 纯数字 → 向后兼容旧 offset cursor（转换为 offset 模式）
//  3. base64(json) → seek cursor
func parseSeekCursor(cursor string) (*seekCursor, int, error) {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return nil, 0, nil
	}

	// 向后兼容：纯数字仍按 offset 解析
	if offset, err := strconv.Atoi(cursor); err == nil && offset >= 0 {
		return nil, offset, nil
	}

	// 尝试 base64 解码
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, 0, fmt.Errorf("invalid cursor: %q", cursor)
	}

	var sc seekCursor
	if err := json.Unmarshal(data, &sc); err != nil {
		return nil, 0, fmt.Errorf("invalid cursor: %q", cursor)
	}
	if sc.LastID == "" {
		return nil, 0, fmt.Errorf("invalid cursor: missing last_id")
	}
	return &sc, 0, nil
}

// encodeSeekCursor 将 seek cursor 编码为 base64 字符串。
func encodeSeekCursor(sc *seekCursor) string {
	if sc == nil || sc.LastID == "" {
		return ""
	}
	data, err := json.Marshal(sc)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

// buildTaskListPage 用于 memory store 的分页构建（仍支持 offset 和 seek 两种模式）。
func buildTaskListPage(items []*TaskInfo, cursor string, limit int) TaskListPage {
	if limit <= 0 {
		return TaskListPage{Items: items}
	}

	sc, offset, err := parseSeekCursor(cursor)
	if err != nil {
		return TaskListPage{Items: []*TaskInfo{}}
	}

	startIdx := 0
	if sc != nil {
		// seek 模式：找到 (LastScore, LastID) 之后的第一条。
		// items 已按 created_at desc, task_id desc 排序。
		// 需要跳过所有 score > LastScore 的，以及 score == LastScore && id >= LastID 的。
		found := false
		for i, info := range items {
			if info == nil || info.Task == nil {
				continue
			}
			score := info.CreatedAtMillis
			id := info.Task.ID
			if score < sc.LastScore || (score == sc.LastScore && id < sc.LastID) {
				startIdx = i
				found = true
				break
			}
		}
		if !found {
			// 所有数据都在 cursor 之前或等于 cursor，返回空
			return TaskListPage{Items: []*TaskInfo{}}
		}
	} else if offset > 0 {
		startIdx = offset
	}

	if startIdx >= len(items) {
		return TaskListPage{Items: []*TaskInfo{}}
	}

	end := startIdx + limit
	if end > len(items) {
		end = len(items)
	}

	page := TaskListPage{
		Items: items[startIdx:end],
	}
	if end < len(items) {
		page.HasMore = true
		lastItem := page.Items[len(page.Items)-1]
		if lastItem != nil && lastItem.Task != nil {
			page.NextCursor = encodeSeekCursor(&seekCursor{
				LastScore: lastItem.CreatedAtMillis,
				LastID:    lastItem.Task.ID,
			})
		}
	}
	return page
}

func filterAndSortTaskInfos(infos []*TaskInfo, query TaskListQuery) []*TaskInfo {
	filtered := make([]*TaskInfo, 0, len(infos))
	for _, info := range infos {
		if !taskInfoMatchesQuery(info, query) {
			continue
		}
		filtered = append(filtered, cloneTaskInfo(info))
	}

	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].CreatedAtMillis != filtered[j].CreatedAtMillis {
			return filtered[i].CreatedAtMillis > filtered[j].CreatedAtMillis
		}
		leftID := ""
		rightID := ""
		if filtered[i].Task != nil {
			leftID = filtered[i].Task.ID
		}
		if filtered[j].Task != nil {
			rightID = filtered[j].Task.ID
		}
		return leftID > rightID
	})

	return filtered
}

func taskInfoMatchesQuery(info *TaskInfo, query TaskListQuery) bool {
	if info == nil || info.Task == nil {
		return false
	}

	if len(query.Statuses) > 0 {
		matched := false
		for _, status := range query.Statuses {
			if info.Status == status {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if query.AppID != "" && info.AppID != query.AppID {
		return false
	}
	if query.ServiceName != "" && info.ServiceName != query.ServiceName {
		return false
	}
	if query.AgentID != "" && info.AgentID != query.AgentID {
		return false
	}
	if query.TaskType != "" && info.Task.TypeName != query.TaskType {
		return false
	}

	return true
}

func cloneTaskInfo(info *TaskInfo) *TaskInfo {
	if info == nil {
		return nil
	}

	copied := *info
	copied.Task = cloneTask(info.Task)
	copied.Result = cloneTaskResult(info.Result)
	return &copied
}

func cloneTask(task *model.Task) *model.Task {
	if task == nil {
		return nil
	}

	copied := *task
	if task.ParametersJSON != nil {
		copied.ParametersJSON = append([]byte(nil), task.ParametersJSON...)
	}
	return &copied
}

func cloneTaskResult(result *model.TaskResult) *model.TaskResult {
	if result == nil {
		return nil
	}

	copied := *result
	if result.ResultJSON != nil {
		copied.ResultJSON = append([]byte(nil), result.ResultJSON...)
	}
	if result.ResultData != nil {
		copied.ResultData = append([]byte(nil), result.ResultData...)
	}
	return &copied
}
