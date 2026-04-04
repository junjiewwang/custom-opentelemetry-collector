/**
 * InstanceTasksTab - 实例关联任务列表 Tab 组件
 *
 * 高内聚低耦合设计：
 *   - 接收 agentId / appId / serviceName 作为 props
 *   - 内部管理任务列表的加载、创建、取消、详情查看
 *   - 可独立使用，不依赖父组件的状态管理
 */

import { useState, useCallback, useEffect, useMemo, useRef } from 'react';
import { apiClient } from '@/api/client';
import { useToast } from '@/contexts/ToastContext';
import { useConfirm } from '@/components/ConfirmDialog';
import DetailDrawer, { DrawerSection } from '@/components/DetailDrawer';
import SearchableSelect, { type SelectOption } from '@/components/SearchableSelect';
import type {
  TaskInfoV2, TaskResultRaw, Task, TaskStatus, NormalizedTaskResult,
  ApiError,
} from '@/types/api';

// ── Props ──────────────────────────────────────────────

interface InstanceTasksTabProps {
  /** 实例 Agent ID */
  agentId: string;
  /** 应用 ID（用于简化创建任务表单） */
  appId?: string;
  /** 服务名称（用于简化创建任务表单） */
  serviceName?: string;
  /** 实例是否在线 */
  isOnline?: boolean;
}

// ── 常量 ──────────────────────────────────────────────

const STATUS_NUM_MAP: Record<number, TaskStatus> = {
  0: 'unknown', 1: 'pending', 2: 'running', 3: 'success',
  4: 'failed', 5: 'timeout', 6: 'cancelled', 7: 'failed',
};

const TASK_TYPE_OPTIONS: SelectOption[] = [
  { value: 'dynamic_instrument',   label: '🔧 Dynamic Instrument',   group: 'Dynamic Instrumentation' },
  { value: 'dynamic_uninstrument', label: '🔄 Dynamic Uninstrument', group: 'Dynamic Instrumentation' },
  { value: 'arthas_attach',        label: '🔗 Arthas Attach',        group: 'Diagnostics' },
  { value: 'arthas_detach',        label: '🔌 Arthas Detach',        group: 'Diagnostics' },
  { value: 'async-profiler',       label: '📊 Async Profiler',       group: 'Diagnostics' },
];

const TASK_TYPE_GROUPS = ['Dynamic Instrumentation', 'Diagnostics'];

// ── 工具函数 ──────────────────────────────────────────

function formatTimestamp(ts: number): string {
  if (!ts) return '-';
  const ms = ts < 10000000000 ? ts * 1000 : ts;
  return new Date(ms).toLocaleString('zh-CN');
}

function formatRelativeTime(ts: number): string {
  if (!ts) return '-';
  const ms = ts < 10000000000 ? ts * 1000 : ts;
  const diff = Date.now() - ms;
  if (diff < 0) return '-';
  if (diff < 30_000) return 'just now';
  const mins = Math.floor(diff / 60_000);
  if (mins < 60) return `${mins}m ago`;
  const hrs = Math.floor(mins / 60);
  if (hrs < 24) return `${hrs}h ago`;
  const days = Math.floor(hrs / 24);
  if (days < 30) return `${days}d ago`;
  return formatTimestamp(ts);
}

function formatShortId(id: string): string {
  if (!id) return '-';
  return id.length > 12 ? id.substring(0, 12) + '…' : id;
}

function decodeBase64ToText(b64: string): string {
  try { return atob(b64); } catch { return b64; }
}

function getResultSummaryEntries(resultJSON: unknown): { key: string; valueText: string }[] {
  if (!resultJSON || typeof resultJSON !== 'object' || Array.isArray(resultJSON)) return [];
  const preferred = ['tunnel_ready', 'arthas_state', 'state', 'message', 'output', 'url'];
  const entries = Object.entries(resultJSON as Record<string, unknown>);
  if (entries.length === 0) return [];
  const scored = entries.map(([k, v]) => {
    const idx = preferred.indexOf(k);
    return { key: k, value: v, score: idx >= 0 ? idx : 1000 };
  });
  scored.sort((a, b) => a.score !== b.score ? a.score - b.score : a.key.localeCompare(b.key));
  return scored.slice(0, 12).map(({ key, value }) => ({
    key,
    valueText: typeof value === 'string' ? value : JSON.stringify(value),
  }));
}

function normalizeTaskResult(result: TaskResultRaw | null | undefined): NormalizedTaskResult | null {
  if (!result || typeof result !== 'object') return null;
  const rawJSON = result.result_json;
  let resultJSONObj: unknown = null;
  let resultJSONPretty = '';
  if (rawJSON !== undefined && rawJSON !== null && rawJSON !== '') {
    if (typeof rawJSON === 'string') {
      try { resultJSONObj = JSON.parse(rawJSON); resultJSONPretty = JSON.stringify(resultJSONObj, null, 2); }
      catch { resultJSONPretty = rawJSON; }
    } else {
      resultJSONObj = rawJSON;
      try { resultJSONPretty = JSON.stringify(rawJSON, null, 2); } catch { /* */ }
    }
  }
  const rawData = result.result_data || '';
  let resultDataText = '';
  if (rawData) {
    resultDataText = decodeBase64ToText(rawData);
    if (resultDataText.length > 20000) resultDataText = resultDataText.slice(0, 20000) + '\n... (truncated)';
  }
  const startedAt = result.started_at_millis || 0;
  const completedAt = result.completed_at_millis || 0;
  let execMillis = result.execution_time_millis || 0;
  if (!execMillis && startedAt > 0 && completedAt > 0 && completedAt >= startedAt) {
    execMillis = completedAt - startedAt;
  }
  const rjo = resultJSONObj as Record<string, unknown> | null;
  return {
    status: result.status,
    error_code: result.error_code || '',
    error_message: result.error_message || '',
    started_at_millis: startedAt,
    completed_at_millis: completedAt,
    execution_time_millis: execMillis,
    has_execution_info: !!result.execution_time_millis || (startedAt > 0 && completedAt > 0),
    result_json_obj: resultJSONObj,
    result_json_pretty: resultJSONPretty,
    result_summary: getResultSummaryEntries(resultJSONObj),
    result_data_base64: rawData,
    result_data_text: resultDataText,
    result_data_type: result.result_data_type || '',
    artifact_ref: result.artifact_ref || '',
    artifact_size: result.artifact_size || 0,
    analysis_view_url: (rjo?.analysis_view_url as string) || '',
    analysis_status: (rjo?.analysis_status as string) || '',
    analysis_error: (rjo?.analysis_error as string) || '',
    analysis_mode: (rjo?.analysis_mode as string) || '',
    analysis_summary: (rjo?.analysis_summary as Record<string, unknown>) || null,
    _raw: result,
  };
}

function parseTasks(rawInfos: TaskInfoV2[]): Task[] {
  return rawInfos.map(info => {
    const task = info.task || {};
    const taskId = task.task_id || info.task_id || '';
    const taskType = task.task_type_name || task.task_type || info.task_type_name || info.task_type || '';
    const targetAgentId = info.agent_id || task.target_agent_id || info.target_agent_id || '';
    const createdAt = info.created_at_millis || task.created_at_millis || 0;
    const statusNum = typeof info.status === 'number' ? info.status : (info.result?.status ?? 0);
    let parameters: Record<string, unknown> = {};
    const rawParams = task.parameters_json;
    if (typeof rawParams === 'string') {
      try { parameters = JSON.parse(rawParams); } catch { parameters = { raw: rawParams }; }
    } else if (rawParams && typeof rawParams === 'object') {
      parameters = rawParams as Record<string, unknown>;
    }
    return {
      task_id: taskId,
      task_type: taskType,
      target_agent_id: targetAgentId,
      app_id: info.app_id || '',
      app_name: info.app_name || '',
      service_name: info.service_name || '',
      agent_state: info.agent_state || '',
      status: STATUS_NUM_MAP[statusNum] || 'unknown',
      created_at_millis: createdAt,
      priority: task.priority_num ?? task.priority ?? 0,
      timeout_millis: task.timeout_millis || 60000,
      parameters,
      _raw: info,
      _result: normalizeTaskResult(info.result),
      _detailLoading: false,
      _detailError: '',
    };
  }).filter(t => t.task_id).sort((a, b) => b.created_at_millis - a.created_at_millis);
}

function getStatusColor(status: TaskStatus): string {
  switch (status) {
    case 'running':   return 'text-blue-600 bg-blue-50';
    case 'pending':   return 'text-yellow-600 bg-yellow-50';
    case 'success':   return 'text-green-600 bg-green-50';
    case 'failed':    return 'text-red-600 bg-red-50';
    case 'timeout':   return 'text-orange-600 bg-orange-50';
    case 'cancelled': return 'text-gray-600 bg-gray-50';
    default:          return 'text-gray-600 bg-gray-50';
  }
}

function getStatusIconBg(status: TaskStatus): string {
  switch (status) {
    case 'running':   return 'bg-blue-50 text-blue-600';
    case 'pending':   return 'bg-yellow-50 text-yellow-600';
    case 'success':   return 'bg-green-50 text-green-600';
    case 'failed':    return 'bg-red-50 text-red-600';
    case 'timeout':   return 'bg-orange-50 text-orange-600';
    default:          return 'bg-gray-50 text-gray-400';
  }
}

function getStatusIcon(status: TaskStatus): string {
  switch (status) {
    case 'running':   return 'fas fa-play';
    case 'pending':   return 'fas fa-clock';
    case 'success':   return 'fas fa-check';
    case 'failed':    return 'fas fa-times';
    case 'timeout':   return 'fas fa-exclamation';
    case 'cancelled': return 'fas fa-ban';
    default:          return 'fas fa-question';
  }
}

// ── 组件 ──────────────────────────────────────────────

export default function InstanceTasksTab({ agentId, serviceName, isOnline }: InstanceTasksTabProps) {
  const { showToast } = useToast();
  const confirm = useConfirm();

  const [tasks, setTasks] = useState<Task[]>([]);
  const [loading, setLoading] = useState(false);

  // 任务详情抽屉
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [selectedTask, setSelectedTask] = useState<Task | null>(null);
  const detailReqSeq = useRef(0);

  // 创建任务模态框
  const [showCreateModal, setShowCreateModal] = useState(false);
  const [taskType, setTaskType] = useState('arthas_attach');
  const [timeoutMs, setTimeoutMs] = useState('60000');
  const [priority, setPriority] = useState('0');
  const [parametersJson, setParametersJson] = useState('');

  // 动态 Instrument 表单
  const [instrClassName, setInstrClassName] = useState('');
  const [instrMethodName, setInstrMethodName] = useState('');
  const [instrType, setInstrType] = useState('trace');
  const [instrSpanName, setInstrSpanName] = useState('');
  const [instrRuleId, setInstrRuleId] = useState('');
  const [instrParamTypes, setInstrParamTypes] = useState('');
  const [instrCaptureArgs, setInstrCaptureArgs] = useState('');
  const [instrCaptureReturn, setInstrCaptureReturn] = useState('');
  const [instrCaptureMaxLen, setInstrCaptureMaxLen] = useState('256');
  const [instrForce, setInstrForce] = useState(false);
  const [instrMethodDesc, setInstrMethodDesc] = useState('');

  // 动态 Uninstrument 表单
  const [uninstrMode, setUninstrMode] = useState<'rule_id' | 'method'>('rule_id');
  const [uninstrRuleId, setUninstrRuleId] = useState('');
  const [uninstrClassName, setUninstrClassName] = useState('');
  const [uninstrMethodName, setUninstrMethodName] = useState('');
  const [uninstrType, setUninstrType] = useState('');

  // ── 加载任务 ──────────────────────────────────────

  const loadTasks = useCallback(async () => {
    if (!agentId) return;
    setLoading(true);
    try {
      const res = await apiClient.getTasks({ agent_id: agentId, limit: 100 });
      setTasks(parseTasks(res.tasks || []));
    } catch (e) {
      showToast(`Failed to load tasks: ${(e as ApiError).message}`, 'error');
    } finally {
      setLoading(false);
    }
  }, [agentId, showToast]);

  useEffect(() => {
    loadTasks();
  }, [loadTasks]);

  // ── 统计 ──────────────────────────────────────────

  const stats = useMemo(() => {
    const r = { total: tasks.length, running: 0, pending: 0, success: 0, failed: 0 };
    for (const t of tasks) {
      if (t.status === 'running') r.running++;
      else if (t.status === 'pending') r.pending++;
      else if (t.status === 'success') r.success++;
      else if (t.status === 'failed' || t.status === 'timeout') r.failed++;
    }
    return r;
  }, [tasks]);

  // ── 打开任务详情 ──────────────────────────────────

  const openTaskDetail = useCallback(async (task: Task) => {
    if (!task.task_id) return;
    setDrawerOpen(true);
    const seq = ++detailReqSeq.current;
    setSelectedTask({ ...task, _detailLoading: true, _detailError: '' });

    try {
      const res = await apiClient.getTask(task.task_id);
      if (seq !== detailReqSeq.current) return;

      const isTaskInfo = res && typeof res === 'object' && res.task;
      const rawResult = isTaskInfo ? (res.result || null) : (res as unknown as TaskResultRaw);
      const statusNum = typeof res.status === 'number' ? res.status : (typeof (res as TaskResultRaw).status === 'number' ? (res as TaskResultRaw).status : null);
      const statusStr = statusNum != null ? (STATUS_NUM_MAP[statusNum] || 'unknown') : task.status;

      let parameters = task.parameters;
      if (res.task?.parameters_json !== undefined) {
        const p = res.task.parameters_json;
        if (typeof p === 'string') {
          try { parameters = JSON.parse(p); } catch { parameters = { raw: p }; }
        } else if (p && typeof p === 'object') {
          parameters = p as Record<string, unknown>;
        }
      }

      setSelectedTask(prev => prev?.task_id === task.task_id ? {
        ...prev,
        status: statusStr,
        parameters,
        _raw: isTaskInfo ? res : prev._raw,
        _result: normalizeTaskResult(rawResult as TaskResultRaw),
        _detailLoading: false,
        _detailError: '',
      } : prev);
    } catch (e) {
      if (seq !== detailReqSeq.current) return;
      setSelectedTask(prev => prev?.task_id === task.task_id ? {
        ...prev!,
        _detailLoading: false,
        _detailError: (e as ApiError).message || 'Failed to load task detail',
      } : prev);
    }
  }, []);

  // ── 取消任务 ──────────────────────────────────────

  const cancelTask = useCallback(async (task: Task, e?: React.MouseEvent) => {
    if (e) e.stopPropagation();
    const ok = await confirm({
      title: 'Cancel Task',
      message: `Cancel task "${formatShortId(task.task_id)}"?`,
      confirmText: 'Cancel Task',
      variant: 'danger',
    });
    if (!ok) return;
    try {
      await apiClient.cancelTask(task.task_id);
      showToast('Task cancelled', 'success');
      loadTasks();
    } catch (e) {
      showToast(`Failed: ${(e as ApiError).message}`, 'error');
    }
  }, [confirm, showToast, loadTasks]);

  // ── 创建任务提交 ──────────────────────────────────

  const handleSubmitTask = useCallback(async () => {
    const realType = taskType === '__custom__' ? '' : taskType;
    if (!realType) { showToast('Please specify task type', 'error'); return; }

    const taskData: Record<string, unknown> = {
      task_type_name: realType,
      target_agent_id: agentId,
      timeout_millis: parseInt(timeoutMs) || 60000,
      priority_num: parseInt(priority) || 0,
    };

    if (realType === 'dynamic_instrument') {
      if (!instrClassName.trim()) { showToast('Class Name is required', 'error'); return; }
      if (!instrMethodName.trim()) { showToast('Method Name is required', 'error'); return; }
      const params: Record<string, string> = {};
      if (instrClassName.trim()) params.class_name = instrClassName.trim();
      if (instrMethodName.trim()) params.method_name = instrMethodName.trim();
      if (instrType) params.type = instrType;
      if (instrRuleId.trim()) params.rule_id = instrRuleId.trim();
      if (instrSpanName.trim()) params.span_name = instrSpanName.trim();
      if (instrParamTypes.trim()) params.parameter_types = instrParamTypes.trim();
      if (instrMethodDesc.trim()) params.method_descriptor = instrMethodDesc.trim();
      if (instrCaptureArgs.trim()) params['config.capture_args'] = instrCaptureArgs.trim();
      if (instrCaptureReturn.trim()) params['config.capture_return'] = instrCaptureReturn.trim();
      if (instrCaptureMaxLen.trim() && instrCaptureMaxLen.trim() !== '256') params['config.capture_max_length'] = instrCaptureMaxLen.trim();
      if (instrForce) params['config.force'] = 'true';
      taskData.parameters_json = params;
    } else if (realType === 'dynamic_uninstrument') {
      const params: Record<string, string> = {};
      if (uninstrMode === 'rule_id') {
        if (!uninstrRuleId.trim()) { showToast('Rule ID is required', 'error'); return; }
        params.rule_id = uninstrRuleId.trim();
      } else {
        if (!uninstrClassName.trim()) { showToast('Class Name is required', 'error'); return; }
        if (!uninstrMethodName.trim()) { showToast('Method Name is required', 'error'); return; }
        params.class_name = uninstrClassName.trim();
        params.method_name = uninstrMethodName.trim();
        if (uninstrType) params.type = uninstrType;
      }
      taskData.parameters_json = params;
    } else if (parametersJson.trim()) {
      try { taskData.parameters_json = JSON.parse(parametersJson); }
      catch { showToast('Invalid JSON in parameters field', 'error'); return; }
    }

    try {
      await apiClient.createTask(taskData as never);
      showToast('Task created successfully', 'success');
      setShowCreateModal(false);
      resetCreateForm();
      loadTasks();
    } catch (e) {
      showToast(`Failed to create task: ${(e as ApiError).message}`, 'error');
    }
  }, [taskType, agentId, timeoutMs, priority, parametersJson,
      instrClassName, instrMethodName, instrType, instrSpanName, instrRuleId,
      instrParamTypes, instrCaptureArgs, instrCaptureReturn, instrCaptureMaxLen,
      instrForce, instrMethodDesc,
      uninstrMode, uninstrRuleId, uninstrClassName, uninstrMethodName, uninstrType,
      showToast, loadTasks]);

  const resetCreateForm = useCallback(() => {
    setTaskType('arthas_attach');
    setTimeoutMs('60000');
    setPriority('0');
    setParametersJson('');
    setInstrClassName(''); setInstrMethodName(''); setInstrType('trace');
    setInstrSpanName(''); setInstrRuleId(''); setInstrParamTypes('');
    setInstrCaptureArgs(''); setInstrCaptureReturn(''); setInstrCaptureMaxLen('256');
    setInstrForce(false); setInstrMethodDesc('');
    setUninstrMode('rule_id'); setUninstrRuleId('');
    setUninstrClassName(''); setUninstrMethodName(''); setUninstrType('');
  }, []);

  const copyToClipboard = useCallback(async (text: string) => {
    try { await navigator.clipboard.writeText(text); showToast('Copied', 'success'); }
    catch { showToast('Failed to copy', 'error'); }
  }, [showToast]);

  // ── 渲染 ──────────────────────────────────────────

  return (
    <div className="space-y-4">
      {/* 头部：统计 + 操作 */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <span className="text-xs text-gray-500">
            <span className="font-bold text-gray-800">{stats.total}</span> tasks
          </span>
          {stats.running > 0 && (
            <span className="text-[10px] px-1.5 py-0.5 rounded-full bg-blue-50 text-blue-600 font-bold">
              {stats.running} running
            </span>
          )}
          {stats.pending > 0 && (
            <span className="text-[10px] px-1.5 py-0.5 rounded-full bg-yellow-50 text-yellow-600 font-bold">
              {stats.pending} pending
            </span>
          )}
          {stats.failed > 0 && (
            <span className="text-[10px] px-1.5 py-0.5 rounded-full bg-red-50 text-red-600 font-bold">
              {stats.failed} failed
            </span>
          )}
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={() => setShowCreateModal(true)}
            disabled={!isOnline}
            className="px-3 py-1.5 bg-blue-600 text-white rounded-lg hover:bg-blue-700 transition flex items-center gap-1.5 text-xs font-bold shadow-sm disabled:opacity-40 disabled:cursor-not-allowed"
          >
            <i className="fas fa-plus text-[10px]" /> Create Task
          </button>
          <button
            onClick={loadTasks}
            className="px-3 py-1.5 bg-gray-100 text-gray-600 rounded-lg hover:bg-gray-200 transition text-xs"
          >
            <i className={`fas fa-sync text-[10px] ${loading ? 'fa-spin' : ''}`} />
          </button>
        </div>
      </div>

      {/* 任务列表 */}
      {loading && tasks.length === 0 ? (
        <div className="space-y-2">
          {Array.from({ length: 4 }).map((_, i) => (
            <div key={i} className="bg-gray-50 border border-gray-100 rounded-lg p-3">
              <div className="flex items-center gap-2.5">
                <div className="w-7 h-7 rounded-md skeleton-shimmer flex-shrink-0" />
                <div className="flex-1 min-w-0 space-y-1.5">
                  <div className="flex items-center gap-2">
                    <div className="h-3 skeleton-shimmer rounded w-24" />
                    <div className="h-3 skeleton-shimmer rounded w-14" />
                  </div>
                  <div className="h-2 skeleton-shimmer rounded w-40" />
                </div>
                <div className="w-16 h-5 skeleton-shimmer rounded flex-shrink-0" />
              </div>
            </div>
          ))}
        </div>
      ) : tasks.length === 0 ? (
        <div className="py-10 text-center">
          <i className="fas fa-tasks text-3xl text-gray-200 mb-3" />
          <p className="text-sm text-gray-400">No tasks for this instance</p>
          {isOnline && (
            <button
              onClick={() => setShowCreateModal(true)}
              className="mt-3 px-4 py-2 bg-blue-50 text-blue-600 rounded-lg text-xs font-bold hover:bg-blue-100 transition"
            >
              <i className="fas fa-plus mr-1" /> Create First Task
            </button>
          )}
        </div>
      ) : (
        <div className="space-y-2 content-fade-in">
          {tasks.map(task => (
            <div
              key={task.task_id}
              onClick={() => openTaskDetail(task)}
              className="bg-gray-50 border border-gray-100 rounded-lg p-3 hover:border-blue-200 hover:bg-blue-50/30 transition cursor-pointer group"
            >
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-2.5 overflow-hidden flex-1">
                  <div className={`w-7 h-7 rounded-md flex items-center justify-center flex-shrink-0 ${getStatusIconBg(task.status)}`}>
                    <i className={`${getStatusIcon(task.status)} text-[10px]`} />
                  </div>
                  <div className="overflow-hidden flex-1 min-w-0">
                    <div className="flex items-center gap-1.5">
                      <span className="text-xs font-bold text-gray-800 truncate">{task.task_type}</span>
                      <span className={`px-1 py-0.5 rounded text-[8px] font-bold uppercase ${getStatusColor(task.status)}`}>
                        {task.status}
                      </span>
                    </div>
                    <div className="flex items-center gap-2 mt-0.5">
                      <code className="text-[9px] font-mono text-gray-400 truncate">{formatShortId(task.task_id)}</code>
                      <span className="text-[9px] text-gray-400">{formatRelativeTime(task.created_at_millis)}</span>
                    </div>
                  </div>
                </div>
                <div className="flex items-center gap-1.5 flex-shrink-0 ml-2">
                  {(task.status === 'running' || task.status === 'pending') && (
                    <button
                      onClick={(e) => cancelTask(task, e)}
                      className="p-1 text-gray-300 hover:text-red-500 hover:bg-red-50 rounded transition opacity-0 group-hover:opacity-100"
                      title="Cancel Task"
                    >
                      <i className="fas fa-stop-circle text-xs" />
                    </button>
                  )}
                  <i className="fas fa-chevron-right text-gray-200 group-hover:text-blue-400 transition text-[10px]" />
                </div>
              </div>
            </div>
          ))}
        </div>
      )}

      {/* ── 任务详情抽屉 ──────────────────────────── */}
      <DetailDrawer open={drawerOpen} onClose={() => setDrawerOpen(false)} width="xl"
        title="Task Detail"
        subtitle={selectedTask ? `#${formatShortId(selectedTask.task_id)} · ${selectedTask.status?.toUpperCase()} · ${formatTimestamp(selectedTask.created_at_millis)}` : undefined}
        footer={selectedTask && (
          <div className="flex gap-3">
            <button onClick={() => cancelTask(selectedTask)}
              disabled={selectedTask.status !== 'running' && selectedTask.status !== 'pending'}
              className="flex-1 px-4 py-3 bg-red-50 text-red-600 rounded-xl font-bold hover:bg-red-100 transition flex items-center justify-center gap-2 disabled:opacity-30 disabled:grayscale">
              <i className="fas fa-stop-circle" /> Cancel Task
            </button>
            <button onClick={() => openTaskDetail(selectedTask)}
              className="flex-1 px-4 py-3 bg-blue-600 text-white rounded-xl font-bold hover:bg-blue-700 transition flex items-center justify-center gap-2 shadow-lg">
              <i className="fas fa-sync" /> Refresh
            </button>
          </div>
        )}>
        {selectedTask && (
          <div className="space-y-8">
            <DrawerSection title="Target">
              <div className="grid grid-cols-2 gap-4">
                <div className="bg-gray-50 p-4 rounded-xl border border-gray-100">
                  <p className="text-[10px] font-bold text-gray-400 mb-1 uppercase">Service</p>
                  <p className="font-bold text-gray-800">{selectedTask.service_name || '-'}</p>
                </div>
                <div className="bg-gray-50 p-4 rounded-xl border border-gray-100">
                  <p className="text-[10px] font-bold text-gray-400 mb-1 uppercase">Instance ID</p>
                  <p className="font-mono text-xs text-gray-800 truncate">{selectedTask.target_agent_id || 'Global'}</p>
                </div>
              </div>
            </DrawerSection>

            <DrawerSection title="Parameters">
              <div className="bg-gray-900 rounded-xl p-4 overflow-hidden shadow-inner">
                <pre className="text-blue-300 font-mono text-xs leading-relaxed overflow-x-auto">{JSON.stringify(selectedTask.parameters || {}, null, 2)}</pre>
              </div>
            </DrawerSection>

            <DrawerSection title="Execution Result">
              <div className="bg-gray-50 rounded-xl p-4 border border-gray-100 min-h-[100px]">
                {selectedTask._detailLoading ? (
                  <div className="flex items-center justify-center py-10 text-gray-400">
                    <i className="fas fa-spinner fa-spin mr-2" /> Loading...
                  </div>
                ) : selectedTask._detailError ? (
                  <div className="py-6 text-sm text-red-600">
                    <div className="font-bold mb-2">Load Failed</div>
                    <div className="bg-red-50 border border-red-100 rounded-lg p-3">{selectedTask._detailError}</div>
                  </div>
                ) : selectedTask._result ? (
                  <div className="space-y-4">
                    {/* 时间统计 */}
                    <div className="grid grid-cols-2 gap-y-3 gap-x-4">
                      <div><p className="text-[9px] font-bold text-gray-400 uppercase mb-0.5">Started</p><p className="text-xs text-gray-700">{formatTimestamp(selectedTask._result.started_at_millis)}</p></div>
                      <div><p className="text-[9px] font-bold text-gray-400 uppercase mb-0.5">Completed</p><p className="text-xs text-gray-700">{formatTimestamp(selectedTask._result.completed_at_millis)}</p></div>
                      <div><p className="text-[9px] font-bold text-gray-400 uppercase mb-0.5">Duration</p><p className="text-sm font-bold text-blue-600">{selectedTask._result.has_execution_info ? `${selectedTask._result.execution_time_millis} ms` : '-'}</p></div>
                      {selectedTask._result.error_code && (
                        <div><p className="text-[9px] font-bold text-red-400 uppercase mb-0.5">Error Code</p><p className="font-mono text-xs text-red-600">{selectedTask._result.error_code}</p></div>
                      )}
                    </div>

                    {/* 性能分析结果 */}
                    {selectedTask._result.analysis_view_url && (
                      <a href={selectedTask._result.analysis_view_url} target="_blank" rel="noopener noreferrer"
                        className="flex items-center gap-3 bg-indigo-50 hover:bg-indigo-100 border border-indigo-200 rounded-lg p-3 transition">
                        <i className="fas fa-fire text-indigo-500" />
                        <div className="flex-1 min-w-0">
                          <p className="text-xs font-bold text-indigo-800">View Flame Graph</p>
                          <p className="text-[9px] text-indigo-500 truncate font-mono">{selectedTask._result.analysis_view_url}</p>
                        </div>
                        <i className="fas fa-external-link-alt text-indigo-400 text-xs" />
                      </a>
                    )}

                    {/* 结果摘要 */}
                    {selectedTask._result.result_summary.length > 0 && (
                      <div>
                        <div className="flex items-center justify-between mb-2">
                          <p className="text-[9px] font-bold text-gray-400 uppercase">Result Summary</p>
                          <button onClick={() => copyToClipboard(JSON.stringify(selectedTask._result?.result_json_obj || {}, null, 2))}
                            className="text-[9px] font-bold text-blue-600 hover:underline uppercase">Copy</button>
                        </div>
                        <div className="grid grid-cols-2 gap-2">
                          {selectedTask._result.result_summary.map(item => (
                            <div key={item.key} className="bg-white p-2 rounded border border-gray-100">
                              <p className="text-[8px] font-bold text-gray-400 uppercase">{item.key}</p>
                              <p className="text-[10px] font-mono text-gray-800 break-all">{item.valueText}</p>
                            </div>
                          ))}
                        </div>
                      </div>
                    )}

                    {/* 完整输出 */}
                    {selectedTask._result.error_message && (
                      <div className="bg-red-50 border border-red-100 rounded-lg p-3">
                        <pre className="text-xs text-red-800 font-mono whitespace-pre-wrap">{selectedTask._result.error_message}</pre>
                      </div>
                    )}
                    {selectedTask._result.result_json_pretty && (
                      <div className="bg-gray-900 rounded-lg p-3 overflow-hidden shadow-inner">
                        <pre className="text-blue-300 font-mono text-[10px] leading-relaxed overflow-x-auto">{selectedTask._result.result_json_pretty}</pre>
                      </div>
                    )}
                  </div>
                ) : (
                  <div className="py-6 text-center">
                    <p className="text-xs text-yellow-700 bg-yellow-50 border border-yellow-100 rounded-lg p-3">
                      <i className="fas fa-exclamation-triangle mr-1" /> Result not available yet
                    </p>
                    <button onClick={() => openTaskDetail(selectedTask)}
                      className="mt-3 px-3 py-1.5 bg-gray-900 text-white rounded-lg text-xs font-bold hover:bg-gray-800 transition">
                      <i className="fas fa-sync text-[10px] mr-1" /> Refresh
                    </button>
                  </div>
                )}
              </div>
            </DrawerSection>
          </div>
        )}
      </DetailDrawer>

      {/* ── 创建任务模态框（简化版：target 已锁定为当前实例） ── */}
      {showCreateModal && (
        <div className="fixed inset-0 z-[70] flex items-center justify-center p-4 bg-black/50 backdrop-blur-sm">
          <div className="bg-white rounded-2xl shadow-2xl w-full max-w-2xl overflow-hidden flex flex-col max-h-[90vh]">
            <div className="px-6 py-4 border-b border-gray-100 flex items-center justify-between bg-gray-50/50">
              <div>
                <h3 className="text-lg font-bold text-gray-800">Create Task</h3>
                <p className="text-[10px] text-gray-400 mt-0.5">
                  Target: <span className="font-mono font-bold text-gray-600">{formatShortId(agentId)}</span>
                  {serviceName && <span className="ml-1">· {serviceName}</span>}
                </p>
              </div>
              <button onClick={() => { setShowCreateModal(false); resetCreateForm(); }} className="text-gray-400 hover:text-gray-600 transition">
                <i className="fas fa-times" />
              </button>
            </div>

            <div className="p-6 overflow-y-auto space-y-5">
              {/* Task Type */}
              <div>
                <label className="block text-[10px] font-bold text-gray-400 uppercase tracking-widest mb-2">Task Type</label>
                <SearchableSelect options={TASK_TYPE_OPTIONS} value={taskType} onChange={setTaskType}
                  placeholder="Search task type..." groups={TASK_TYPE_GROUPS} allowCustom customLabel="⌨️ Custom Type" emptyText="No matching task types" />
              </div>

              <div className="grid grid-cols-2 gap-4">
                <div>
                  <label className="block text-[10px] font-bold text-gray-400 uppercase tracking-widest mb-2">Timeout (ms)</label>
                  <input type="number" value={timeoutMs} onChange={e => setTimeoutMs(e.target.value)}
                    className="w-full px-4 py-2.5 bg-gray-50 border border-gray-200 rounded-xl focus:ring-2 focus:ring-blue-500 text-sm transition" />
                </div>
                <div>
                  <label className="block text-[10px] font-bold text-gray-400 uppercase tracking-widest mb-2">Priority</label>
                  <input type="number" value={priority} onChange={e => setPriority(e.target.value)}
                    className="w-full px-4 py-2.5 bg-gray-50 border border-gray-200 rounded-xl focus:ring-2 focus:ring-blue-500 text-sm transition" />
                </div>
              </div>

              {/* Dynamic Instrument Form */}
              {taskType === 'dynamic_instrument' && (
                <div className="space-y-4 bg-gradient-to-br from-emerald-50/50 to-blue-50/50 border border-emerald-200/60 rounded-xl p-5">
                  <div className="flex items-center gap-2 mb-1">
                    <i className="fas fa-magic text-emerald-500 text-xs" />
                    <span className="text-[10px] font-bold text-emerald-600 uppercase tracking-widest">Dynamic Instrument</span>
                  </div>
                  <div className="grid grid-cols-2 gap-3">
                    <div>
                      <label className="block text-[10px] font-bold text-gray-400 uppercase mb-1.5">Class Name <span className="text-red-400">*</span></label>
                      <input type="text" value={instrClassName} onChange={e => setInstrClassName(e.target.value)} placeholder="com.example.UserService"
                        className="w-full px-3 py-2 bg-white border border-gray-200 rounded-lg focus:ring-2 focus:ring-emerald-500 text-xs font-mono transition placeholder:text-gray-300" />
                    </div>
                    <div>
                      <label className="block text-[10px] font-bold text-gray-400 uppercase mb-1.5">Method Name <span className="text-red-400">*</span></label>
                      <input type="text" value={instrMethodName} onChange={e => setInstrMethodName(e.target.value)} placeholder="handleLogin"
                        className="w-full px-3 py-2 bg-white border border-gray-200 rounded-lg focus:ring-2 focus:ring-emerald-500 text-xs font-mono transition placeholder:text-gray-300" />
                    </div>
                  </div>
                  <div className="grid grid-cols-2 gap-3">
                    <div>
                      <label className="block text-[10px] font-bold text-gray-400 uppercase mb-1.5">Type <span className="text-red-400">*</span></label>
                      <select value={instrType} onChange={e => setInstrType(e.target.value)}
                        className="w-full px-3 py-2 bg-white border border-gray-200 rounded-lg focus:ring-2 focus:ring-emerald-500 text-xs transition">
                        <option value="trace">🔍 Trace</option><option value="metric">📊 Metric</option><option value="log">📝 Log</option>
                      </select>
                    </div>
                    {instrType === 'trace' && (
                      <div>
                        <label className="block text-[10px] font-bold text-gray-400 uppercase mb-1.5">Span Name</label>
                        <input type="text" value={instrSpanName} onChange={e => setInstrSpanName(e.target.value)} placeholder="custom-span"
                          className="w-full px-3 py-2 bg-white border border-gray-200 rounded-lg focus:ring-2 focus:ring-emerald-500 text-xs font-mono transition placeholder:text-gray-300" />
                      </div>
                    )}
                  </div>
                  <div className="grid grid-cols-2 gap-3">
                    <div>
                      <label className="block text-[10px] font-bold text-gray-400 uppercase mb-1.5">Parameter Types</label>
                      <input type="text" value={instrParamTypes} onChange={e => setInstrParamTypes(e.target.value)} placeholder="String,int"
                        className="w-full px-3 py-2 bg-white border border-gray-200 rounded-lg text-xs font-mono transition placeholder:text-gray-300" />
                    </div>
                    <div>
                      <label className="block text-[10px] font-bold text-gray-400 uppercase mb-1.5">Rule ID</label>
                      <input type="text" value={instrRuleId} onChange={e => setInstrRuleId(e.target.value)} placeholder="auto-generated"
                        className="w-full px-3 py-2 bg-white border border-gray-200 rounded-lg text-xs font-mono transition placeholder:text-gray-300" />
                    </div>
                  </div>
                  {instrType === 'trace' && (
                    <div className="grid grid-cols-3 gap-3 pt-2 border-t border-emerald-200/40">
                      <div>
                        <label className="block text-[10px] font-bold text-gray-400 uppercase mb-1.5">Capture Args</label>
                        <input type="text" value={instrCaptureArgs} onChange={e => setInstrCaptureArgs(e.target.value)} placeholder="0,2 or *"
                          className="w-full px-3 py-2 bg-white border border-gray-200 rounded-lg text-xs font-mono transition placeholder:text-gray-300" />
                      </div>
                      <div>
                        <label className="block text-[10px] font-bold text-gray-400 uppercase mb-1.5">Capture Return</label>
                        <input type="text" value={instrCaptureReturn} onChange={e => setInstrCaptureReturn(e.target.value)} placeholder="*"
                          className="w-full px-3 py-2 bg-white border border-gray-200 rounded-lg text-xs font-mono transition placeholder:text-gray-300" />
                      </div>
                      <div>
                        <label className="block text-[10px] font-bold text-gray-400 uppercase mb-1.5">Max Length</label>
                        <input type="text" value={instrCaptureMaxLen} onChange={e => setInstrCaptureMaxLen(e.target.value)} placeholder="256"
                          className="w-full px-3 py-2 bg-white border border-gray-200 rounded-lg text-xs font-mono transition placeholder:text-gray-300" />
                      </div>
                    </div>
                  )}
                  <label className="flex items-center gap-2 cursor-pointer select-none pt-2 border-t border-emerald-200/40">
                    <input type="checkbox" checked={instrForce} onChange={e => setInstrForce(e.target.checked)}
                      className="w-4 h-4 text-emerald-600 rounded" />
                    <span className="text-[10px] font-bold text-gray-500 uppercase">Force (ignore conflicts)</span>
                  </label>
                </div>
              )}

              {/* Dynamic Uninstrument Form */}
              {taskType === 'dynamic_uninstrument' && (
                <div className="space-y-4 bg-gradient-to-br from-amber-50/50 to-orange-50/50 border border-amber-200/60 rounded-xl p-5">
                  <div className="flex items-center gap-2 mb-1">
                    <i className="fas fa-undo-alt text-amber-500 text-xs" />
                    <span className="text-[10px] font-bold text-amber-600 uppercase tracking-widest">Dynamic Uninstrument</span>
                  </div>
                  <div className="flex gap-1 bg-gray-100 rounded-lg p-1">
                    <button type="button" onClick={() => setUninstrMode('rule_id')}
                      className={`flex-1 px-3 py-1.5 rounded-md text-[10px] uppercase tracking-widest transition font-medium ${uninstrMode === 'rule_id' ? 'bg-white shadow-sm text-amber-700 font-bold' : 'text-gray-500'}`}>
                      By Rule ID
                    </button>
                    <button type="button" onClick={() => setUninstrMode('method')}
                      className={`flex-1 px-3 py-1.5 rounded-md text-[10px] uppercase tracking-widest transition font-medium ${uninstrMode === 'method' ? 'bg-white shadow-sm text-amber-700 font-bold' : 'text-gray-500'}`}>
                      By Method
                    </button>
                  </div>
                  {uninstrMode === 'rule_id' ? (
                    <div>
                      <label className="block text-[10px] font-bold text-gray-400 uppercase mb-1.5">Rule ID <span className="text-red-400">*</span></label>
                      <input type="text" value={uninstrRuleId} onChange={e => setUninstrRuleId(e.target.value)} placeholder="UserService.handleLogin_trace"
                        className="w-full px-3 py-2 bg-white border border-gray-200 rounded-lg text-xs font-mono transition placeholder:text-gray-300" />
                    </div>
                  ) : (
                    <div className="grid grid-cols-2 gap-3">
                      <div>
                        <label className="block text-[10px] font-bold text-gray-400 uppercase mb-1.5">Class Name <span className="text-red-400">*</span></label>
                        <input type="text" value={uninstrClassName} onChange={e => setUninstrClassName(e.target.value)} placeholder="com.example.UserService"
                          className="w-full px-3 py-2 bg-white border border-gray-200 rounded-lg text-xs font-mono transition placeholder:text-gray-300" />
                      </div>
                      <div>
                        <label className="block text-[10px] font-bold text-gray-400 uppercase mb-1.5">Method Name <span className="text-red-400">*</span></label>
                        <input type="text" value={uninstrMethodName} onChange={e => setUninstrMethodName(e.target.value)} placeholder="handleLogin"
                          className="w-full px-3 py-2 bg-white border border-gray-200 rounded-lg text-xs font-mono transition placeholder:text-gray-300" />
                      </div>
                    </div>
                  )}
                </div>
              )}

              {/* Generic Parameters JSON */}
              {taskType !== 'dynamic_instrument' && taskType !== 'dynamic_uninstrument' && (
                <div>
                  <label className="block text-[10px] font-bold text-gray-400 uppercase tracking-widest mb-2">Parameters (JSON)</label>
                  <textarea value={parametersJson} onChange={e => setParametersJson(e.target.value)} rows={3} placeholder='{ "key": "value" }'
                    className="w-full px-4 py-3 bg-gray-900 text-blue-300 font-mono text-xs rounded-xl focus:ring-2 focus:ring-blue-500 transition shadow-inner" />
                </div>
              )}
            </div>

            <div className="px-6 py-4 bg-gray-50 border-t border-gray-100 flex gap-3">
              <button onClick={() => { setShowCreateModal(false); resetCreateForm(); }}
                className="flex-1 px-4 py-2.5 border border-gray-200 text-gray-600 rounded-xl font-bold hover:bg-gray-100 transition">Cancel</button>
              <button onClick={handleSubmitTask}
                className="flex-1 px-4 py-2.5 bg-blue-600 text-white rounded-xl font-bold hover:bg-blue-700 transition shadow-lg shadow-blue-200">Submit</button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
