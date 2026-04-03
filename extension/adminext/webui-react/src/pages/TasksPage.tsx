/**
 * TasksPage - 任务管理页面
 *
 * 从旧版 Alpine.js tasks 视图完整移植到 React。
 *
 * 功能：
 *   - 左侧 App→Service→Instance 三级树（TreeNav）
 *   - 右侧任务卡片列表
 *   - 6 个可点击统计卡片（全部/运行中/等待中/成功/失败/超时）
 *   - 搜索过滤（Task ID / App / Service / Agent / Type）
 *   - 树-列表联动筛选
 *   - 详情抽屉（目标信息 / 参数 / 执行结果 / 时间统计 / 性能分析结果 / Artifact）
 *   - 创建任务模态框（SearchableSelect 选类型/目标 + 动态表单）
 *   - 停止任务操作
 */

import { useState, useCallback, useEffect, useMemo, useRef } from 'react';
import { apiClient } from '@/api/client';
import { useToast } from '@/contexts/ToastContext';
import { useConfirm } from '@/components/ConfirmDialog';
import TreeNav, { type TreeNode } from '@/components/TreeNav';
import DetailDrawer, { DrawerSection } from '@/components/DetailDrawer';
import SearchableSelect, { type SelectOption } from '@/components/SearchableSelect';
import type {
  TaskInfoV2, TaskResultRaw, Task, TaskStatus, NormalizedTaskResult,
  Instance, App, AppService, ApiError,
} from '@/types/api';

// ── 常量 ──────────────────────────────────────────────

type StatusFilter = 'all' | 'running' | 'pending' | 'success' | 'failed' | 'timeout';

const STAT_CARDS: { label: string; filter: StatusFilter; icon: string; color: string }[] = [
  { label: 'Total',   filter: 'all',     icon: 'fas fa-list',               color: 'text-gray-500' },
  { label: 'Running', filter: 'running', icon: 'fas fa-play-circle',        color: 'text-blue-500' },
  { label: 'Pending', filter: 'pending', icon: 'fas fa-clock',              color: 'text-yellow-500' },
  { label: 'Success', filter: 'success', icon: 'fas fa-check-circle',       color: 'text-green-500' },
  { label: 'Failed',  filter: 'failed',  icon: 'fas fa-times-circle',       color: 'text-red-500' },
  { label: 'Timeout', filter: 'timeout', icon: 'fas fa-exclamation-circle', color: 'text-orange-500' },
];

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

// ── 主组件 ──────────────────────────────────────────

export default function TasksPage() {
  const { showToast } = useToast();
  const confirm = useConfirm();

  // 数据
  const [tasks, setTasks] = useState<Task[]>([]);
  const [loading, setLoading] = useState(false);
  const [search, setSearch] = useState('');
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all');
  const [selectedTreeNodeId, setSelectedTreeNodeId] = useState<string | undefined>();

  // 级联查询：树数据
  const [apps, setApps] = useState<App[]>([]);
  const [appServicesMap, setAppServicesMap] = useState<Record<string, AppService[]>>({});
  const [expandedAppIds, setExpandedAppIds] = useState<Set<string>>(new Set());
  const [treeLoading, setTreeLoading] = useState(false);

  // 当前选中的过滤条件
  const [filterAppId, setFilterAppId] = useState<string | undefined>();
  const [filterServiceName, setFilterServiceName] = useState<string | undefined>();

  // 详情抽屉
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [selectedTask, setSelectedTask] = useState<Task | null>(null);
  const detailReqSeq = useRef(0);

  // 创建任务模态框
  const [showCreateModal, setShowCreateModal] = useState(false);
  const [taskType, setTaskType] = useState('arthas_attach');
  const [targetAgentId, setTargetAgentId] = useState('');
  const [timeoutMs, setTimeoutMs] = useState('60000');
  const [priority, setPriority] = useState('0');
  const [parametersJson, setParametersJson] = useState('');
  const [agentOptions, setAgentOptions] = useState<SelectOption[]>([]);

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

  // ── 加载 Apps 列表（树第一级） ──────────────────────

  const loadApps = useCallback(async () => {
    setTreeLoading(true);
    try {
      const appsList = await apiClient.getApps();
      setApps(appsList);
    } catch (e) {
      showToast(`Failed to load apps: ${(e as ApiError).message}`, 'error');
    } finally {
      setTreeLoading(false);
    }
  }, [showToast]);

  // ── 加载 App 下的 Services（树第二级，按需加载） ──

  const loadAppServices = useCallback(async (appId: string) => {
    if (appServicesMap[appId]) return;
    try {
      const services = await apiClient.getAppServices(appId);
      setAppServicesMap(prev => ({ ...prev, [appId]: services }));
    } catch (e) {
      showToast(`Failed to load services: ${(e as ApiError).message}`, 'error');
    }
  }, [appServicesMap, showToast]);

  // ── 加载任务（带过滤参数） ──────────────────────────

  const loadTasks = useCallback(async (appId?: string, serviceName?: string) => {
    if (loading) return;
    setLoading(true);
    try {
      const res = await apiClient.getTasks({
        app_id: appId,
        service_name: serviceName,
        limit: 200,
      });
      setTasks(parseTasks(res.tasks || []));
    } catch (e) {
      showToast(`Failed to load tasks: ${(e as ApiError).message}`, 'error');
    } finally {
      setLoading(false);
    }
  }, [loading, showToast]);

  useEffect(() => {
    loadApps();
    loadTasks();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // ── 懒加载 Agent 选项 ──────────────────────────────

  const loadAgentOptions = useCallback(async () => {
    try {
      const instances = await apiClient.getInstances('online');
      const opts: SelectOption[] = [
        { value: '', label: '🌐 Global Broadcast (All Online Agents)' },
        ...instances.filter((i: Instance) => i.status?.state === 'online').map((inst: Instance) => ({
          value: inst.agent_id,
          label: `${inst.service_name || '-'} — ${inst.hostname || inst.ip || '-'} (${(inst.agent_id || '').substring(0, 8)})`,
        })),
      ];
      setAgentOptions(opts);
    } catch { /* ignore */ }
  }, []);

  // ── 统计数据 ──────────────────────────────────────

  const stats = useMemo(() => {
    const result: Record<StatusFilter, number> = { all: tasks.length, running: 0, pending: 0, success: 0, failed: 0, timeout: 0 };
    for (const t of tasks) {
      if (t.status === 'running') result.running++;
      else if (t.status === 'pending') result.pending++;
      else if (t.status === 'success') result.success++;
      else if (t.status === 'failed') result.failed++;
      else if (t.status === 'timeout') result.timeout++;
    }
    return result;
  }, [tasks]);

  // ── 过滤逻辑 ──────────────────────────────────────

  const filteredTasks = useMemo(() => {
    let list = tasks;
    if (statusFilter !== 'all') list = list.filter(t => t.status === statusFilter);
    const q = search.toLowerCase().trim();
    if (q) {
      list = list.filter(t =>
        t.task_id.toLowerCase().includes(q) ||
        t.app_id.toLowerCase().includes(q) ||
        (t.app_name || '').toLowerCase().includes(q) ||
        (t.service_name || '').toLowerCase().includes(q) ||
        (t.target_agent_id || '').toLowerCase().includes(q) ||
        (t.task_type || '').toLowerCase().includes(q),
      );
    }
    return list;
  }, [tasks, statusFilter, search]);

  // ── 构建两级树（从 Apps API 数据构建） ──────────────

  const treeData = useMemo((): TreeNode[] => {
    const result: TreeNode[] = [];
    for (const app of apps) {
      const svcChildren: TreeNode[] = [];
      const services = appServicesMap[app.id] || [];
      for (const svc of services) {
        svcChildren.push({
          id: `svc-${app.id}-${svc.service_name}`,
          name: svc.service_name === '_unknown_service_' ? 'Unknown Service' : svc.service_name,
          icon: 'fas fa-sitemap',
          iconColor: 'text-purple-400',
          badge: svc.instance_count,
          badgeColor: 'bg-gray-100 text-gray-500',
        });
      }
      svcChildren.sort((a, b) => ((b.badge as number) || 0) - ((a.badge as number) || 0));

      result.push({
        id: `app-${app.id}`,
        name: app.name || app.id,
        icon: 'fas fa-cube',
        iconColor: 'text-blue-500',
        badge: app.agent_count ?? 0,
        badgeColor: 'bg-gray-100 text-gray-500',
        defaultExpanded: expandedAppIds.has(app.id),
        children: svcChildren.length > 0 ? svcChildren : undefined,
        ...(app.service_count && app.service_count > 0 && svcChildren.length === 0
          ? { badge: `${app.agent_count ?? 0} (${app.service_count} svc)` }
          : {}),
      });
    }
    result.sort((a, b) => {
      const aCount = typeof a.badge === 'number' ? a.badge : 0;
      const bCount = typeof b.badge === 'number' ? b.badge : 0;
      return bCount - aCount;
    });
    return result;
  }, [apps, appServicesMap, expandedAppIds]);

  // ── 树节点选中 ──────────────────────────────────────

  const handleTreeSelect = useCallback((node: TreeNode | null) => {
    if (!node) {
      setSelectedTreeNodeId(undefined);
      setFilterAppId(undefined);
      setFilterServiceName(undefined);
      loadTasks();
      return;
    }

    // App 节点被点击：展开并加载 services，过滤该 App 下的任务
    if (node.id.startsWith('app-')) {
      const appId = node.id.replace('app-', '');
      setExpandedAppIds(prev => {
        const next = new Set(prev);
        if (next.has(appId)) next.delete(appId);
        else next.add(appId);
        return next;
      });
      loadAppServices(appId);
      setSelectedTreeNodeId(node.id);
      setFilterAppId(appId);
      setFilterServiceName(undefined);
      loadTasks(appId);
      return;
    }

    // Service 节点被点击：过滤该 Service 下的任务
    if (node.id.startsWith('svc-')) {
      setSelectedTreeNodeId(node.id);
      const parts = node.id.replace('svc-', '').split('-');
      const appId = parts[0];
      const serviceName = parts.slice(1).join('-');
      setFilterAppId(appId);
      setFilterServiceName(serviceName);
      loadTasks(appId, serviceName);
    }
  }, [loadAppServices, loadTasks]);

  const selectedTreeLabel = useMemo(() => {
    if (!selectedTreeNodeId) return '';
    if (selectedTreeNodeId.startsWith('app-')) {
      for (const app of treeData) {
        if (app.id === selectedTreeNodeId) return app.name;
      }
    }
    for (const app of treeData) {
      for (const svc of (app.children || [])) {
        if (svc.id === selectedTreeNodeId) return `${app.name} / ${svc.name}`;
      }
    }
    return '';
  }, [selectedTreeNodeId, treeData]);

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

  // ── 停止任务 ──────────────────────────────────────

  const cancelTask = useCallback(async (task: Task) => {
    const ok = await confirm({
      title: 'Cancel Task',
      message: `Cancel task "${formatShortId(task.task_id)}"?\n\nThis will send a cancellation request to the agent.`,
      confirmText: 'Cancel Task',
      variant: 'danger',
    });
    if (!ok) return;
    try {
      await apiClient.cancelTask(task.task_id);
      showToast('Task cancelled successfully', 'success');
      setDrawerOpen(false);
      loadTasks(filterAppId, filterServiceName);
    } catch (e) {
      showToast(`Failed to cancel task: ${(e as ApiError).message}`, 'error');
    }
  }, [confirm, showToast, loadTasks, filterAppId, filterServiceName]);

  // ── 创建任务提交 ──────────────────────────────────

  const handleSubmitTask = useCallback(async () => {
    const realType = taskType === '__custom__' ? '' : taskType;
    if (!realType) { showToast('Please specify task type', 'error'); return; }

    const taskData: Record<string, unknown> = {
      task_type_name: realType,
      timeout_millis: parseInt(timeoutMs) || 60000,
      priority_num: parseInt(priority) || 0,
    };
    if (targetAgentId) taskData.target_agent_id = targetAgentId;

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
      loadTasks(filterAppId, filterServiceName);
    } catch (e) {
      showToast(`Failed to create task: ${(e as ApiError).message}`, 'error');
    }
  }, [taskType, targetAgentId, timeoutMs, priority, parametersJson,
      instrClassName, instrMethodName, instrType, instrSpanName, instrRuleId,
      instrParamTypes, instrCaptureArgs, instrCaptureReturn, instrCaptureMaxLen,
      instrForce, instrMethodDesc,
      uninstrMode, uninstrRuleId, uninstrClassName, uninstrMethodName, uninstrType,
      showToast, loadTasks, filterAppId, filterServiceName]);

  const resetCreateForm = useCallback(() => {
    setTaskType('arthas_attach');
    setTargetAgentId('');
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
    try { await navigator.clipboard.writeText(text); showToast('Copied to clipboard', 'success'); }
    catch { showToast('Failed to copy', 'error'); }
  }, [showToast]);

  // ── 渲染 ──────────────────────────────────────────

  return (
    <div className="space-y-6">
      {/* 头部 */}
      <div className="flex items-center justify-between">
        <h2 className="text-2xl font-bold text-gray-800">Task Management</h2>
        <div className="flex items-center gap-3">
          <div className="relative">
            <input type="text" value={search} onChange={e => setSearch(e.target.value)}
              placeholder="Search Task ID, App, Service..."
              className="pl-9 pr-4 py-2 border border-gray-200 rounded-lg focus:ring-2 focus:ring-blue-500 focus:border-blue-500 w-56 text-sm" />
            <i className="fas fa-search absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
          </div>
          <button onClick={() => { setShowCreateModal(true); loadAgentOptions(); }}
            className="px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 transition flex items-center gap-2 shadow-sm text-sm">
            <i className="fas fa-plus" /> Create Task
          </button>
          <button onClick={() => { loadApps(); loadTasks(filterAppId, filterServiceName); }} className="px-4 py-2 bg-gray-100 text-gray-700 rounded-lg hover:bg-gray-200 transition text-sm">
            <i className={`fas fa-sync ${loading ? 'fa-spin' : ''}`} />
          </button>
        </div>
      </div>

      {/* 统计卡片 */}
      <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-4">
        {STAT_CARDS.map(stat => (
          <div key={stat.filter} onClick={() => setStatusFilter(stat.filter)}
            className={`p-4 rounded-xl shadow-sm border border-gray-100 cursor-pointer hover:shadow-md transition group ${
              statusFilter === stat.filter ? 'ring-2 ring-blue-500 bg-blue-50' : 'bg-white'}`}>
            <div className="flex items-center justify-between mb-1">
              <p className="text-[10px] font-bold text-gray-400 uppercase tracking-widest">{stat.label}</p>
              <i className={`${stat.icon} ${stat.color} opacity-40 group-hover:opacity-100 transition text-xs`} />
            </div>
            <p className="text-2xl font-bold text-gray-800">{stats[stat.filter]}</p>
          </div>
        ))}
      </div>

      {/* 主体：左侧树 + 右侧列表 */}
      <div className="flex gap-6" style={{ minHeight: 500 }}>
        {/* 左侧导航树 */}
        <div className="w-72 flex-shrink-0">
          <div className="bg-white rounded-xl shadow-sm border border-gray-100 overflow-hidden sticky top-4 flex flex-col" style={{ maxHeight: 'calc(100vh - 320px)' }}>
            <div className="px-4 py-3 bg-gray-50 border-b border-gray-100 flex-shrink-0">
              <h3 className="text-xs font-bold text-gray-500 uppercase tracking-widest">App / Service</h3>
            </div>
            <button onClick={() => {
                setSelectedTreeNodeId(undefined);
                setFilterAppId(undefined);
                setFilterServiceName(undefined);
                loadTasks();
              }}
              className={`w-full flex items-center gap-2 px-4 py-2.5 text-left transition select-none border-b border-gray-50 ${
                !selectedTreeNodeId ? 'bg-blue-50 border-l-2 border-l-blue-500' : 'border-l-2 border-l-transparent hover:bg-gray-50'}`}>
              <i className="fas fa-globe text-blue-500 text-xs" />
              <span className={`text-xs font-bold ${!selectedTreeNodeId ? 'text-blue-700' : 'text-gray-600'}`}>All Tasks</span>
              <span className="text-[9px] font-bold text-gray-400 bg-gray-100 px-1.5 py-0.5 rounded ml-auto">{tasks.length}</span>
            </button>
            <TreeNav data={treeData} selectedId={selectedTreeNodeId} onSelect={handleTreeSelect}
              allowSelectParent={true} emptyText={treeLoading ? 'Loading...' : 'No apps found'} />
          </div>
        </div>

        {/* 右侧任务列表 */}
        <div className="flex-1 min-w-0">
          {selectedTreeNodeId ? (
            <div className="mb-4 flex items-center gap-2">
              <div className="bg-blue-50 border border-blue-200 rounded-lg px-3 py-1.5 flex items-center gap-2">
                <i className="fas fa-server text-blue-500 text-xs" />
                <span className="text-xs font-bold text-blue-700">{selectedTreeLabel}</span>
                <button onClick={() => {
                    setSelectedTreeNodeId(undefined);
                    setFilterAppId(undefined);
                    setFilterServiceName(undefined);
                    loadTasks();
                  }} className="text-blue-400 hover:text-blue-600 transition ml-1">
                  <i className="fas fa-times text-[10px]" />
                </button>
              </div>
              <span className="text-[10px] text-gray-400">{filteredTasks.length} tasks</span>
            </div>
          ) : (
            <div className="mb-4">
              <p className="text-xs text-gray-400"><i className="fas fa-info-circle mr-1" />Click an instance node on the left to filter the task list</p>
            </div>
          )}

          <div className="space-y-3">
            {filteredTasks.map(task => (
              <div key={task.task_id} onClick={() => openTaskDetail(task)}
                className="bg-white border border-gray-100 rounded-xl p-4 hover:border-blue-300 hover:shadow-md transition cursor-pointer group">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-3 overflow-hidden flex-1">
                    <div className={`w-9 h-9 rounded-lg flex items-center justify-center flex-shrink-0 ${getStatusIconBg(task.status)}`}>
                      <i className={`${getStatusIcon(task.status)} text-xs`} />
                    </div>
                    <div className="overflow-hidden flex-1 min-w-0">
                      <div className="flex items-center gap-2">
                        <span className="text-sm font-bold text-gray-800 truncate">{task.task_type}</span>
                        <span className={`px-1.5 py-0.5 rounded-full text-[9px] font-bold uppercase tracking-tight flex-shrink-0 ${getStatusColor(task.status)}`}>{task.status}</span>
                      </div>
                      <div className="flex items-center gap-3 mt-1">
                        <code className="text-[9px] font-mono text-gray-400 truncate">{formatShortId(task.task_id)}</code>
                        <span className="text-[9px] text-gray-300">|</span>
                        <span className="text-[9px] text-gray-400 truncate">{(task.app_name || task.app_id || '-') + ' / ' + (task.service_name || '-')}</span>
                        {task.target_agent_id && (
                          <span className="text-[9px] text-gray-400 truncate">
                            <i className="fas fa-server text-[8px] mr-0.5" />{formatShortId(task.target_agent_id)}
                          </span>
                        )}
                      </div>
                    </div>
                  </div>
                  <div className="text-right flex-shrink-0 ml-3">
                    <div className="text-[9px] text-gray-400 font-medium">{formatRelativeTime(task.created_at_millis)}</div>
                    <i className="fas fa-chevron-right text-gray-200 group-hover:text-blue-500 transition text-[10px] mt-1" />
                  </div>
                </div>
              </div>
            ))}
            {filteredTasks.length === 0 && (
              <div className="bg-white rounded-xl shadow-sm border border-gray-100 p-12 text-center">
                <i className="fas fa-tasks text-4xl mb-3 text-gray-200" />
                <p className="text-gray-400">No tasks match the current filters</p>
              </div>
            )}
          </div>
        </div>
      </div>

      {/* ── 详情抽屉 ────────────────────────────────── */}
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
            {/* 执行目标 */}
            <DrawerSection title="Target">
              <div className="grid grid-cols-2 gap-4">
                <div className="bg-gray-50 p-4 rounded-xl border border-gray-100">
                  <p className="text-[10px] font-bold text-gray-400 mb-1 uppercase">Service</p>
                  <p className="font-bold text-gray-800">{selectedTask.service_name || '-'}</p>
                </div>
                <div className="bg-gray-50 p-4 rounded-xl border border-gray-100">
                  <p className="text-[10px] font-bold text-gray-400 mb-1 uppercase">Instance ID</p>
                  <p className="font-mono text-xs text-gray-800 truncate">{selectedTask.target_agent_id || 'Global Broadcast'}</p>
                </div>
              </div>
            </DrawerSection>

            {/* 任务参数 */}
            <DrawerSection title="Parameters">
              <div className="flex items-center justify-between mb-2">
                <span className="text-[10px] text-gray-400">JSON</span>
                <button onClick={() => copyToClipboard(JSON.stringify(selectedTask.parameters || {}))}
                  className="text-[10px] font-bold text-blue-600 hover:underline uppercase">Copy</button>
              </div>
              <div className="bg-gray-900 rounded-xl p-4 overflow-hidden shadow-inner">
                <pre className="text-blue-300 font-mono text-xs leading-relaxed overflow-x-auto">{JSON.stringify(selectedTask.parameters || {}, null, 2)}</pre>
              </div>
            </DrawerSection>

            {/* 执行结果 */}
            <DrawerSection title="Execution Result">
              <div className="bg-gray-50 rounded-xl p-4 border border-gray-100 min-h-[100px]">
                {selectedTask._detailLoading ? (
                  <div className="flex items-center justify-center py-10 text-gray-400">
                    <i className="fas fa-spinner fa-spin mr-2" /> Loading task detail...
                  </div>
                ) : selectedTask._detailError ? (
                  <div className="py-6 text-sm text-red-600">
                    <div className="font-bold mb-2">Load Failed</div>
                    <div className="bg-red-50 border border-red-100 rounded-lg p-3">{selectedTask._detailError}</div>
                  </div>
                ) : selectedTask._result ? (
                  <div className="space-y-6">
                    {/* 时间统计 */}
                    <div className="bg-white p-5 rounded-xl border border-gray-100 shadow-sm">
                      <div className="flex items-center gap-2 mb-4">
                        <i className="fas fa-history text-blue-500 text-xs" />
                        <p className="text-[10px] font-bold text-gray-400 uppercase tracking-widest">Time Statistics</p>
                      </div>
                      <div className="grid grid-cols-2 gap-y-4 gap-x-6">
                        <div><p className="text-[9px] font-bold text-gray-400 uppercase mb-1">Created</p><p className="text-xs text-gray-700 font-medium">{formatTimestamp(selectedTask.created_at_millis)}</p></div>
                        <div><p className="text-[9px] font-bold text-gray-400 uppercase mb-1">Started</p><p className="text-xs text-gray-700 font-medium">{formatTimestamp(selectedTask._result.started_at_millis)}</p></div>
                        <div><p className="text-[9px] font-bold text-gray-400 uppercase mb-1">Completed</p><p className="text-xs text-gray-700 font-medium">{formatTimestamp(selectedTask._result.completed_at_millis)}</p></div>
                        <div><p className="text-[9px] font-bold text-gray-400 uppercase mb-1">Duration</p><p className="text-sm font-bold text-blue-600">{selectedTask._result.has_execution_info ? `${selectedTask._result.execution_time_millis} ms` : '-'}</p></div>
                      </div>
                      {selectedTask._result.error_code && (
                        <div className="mt-4 pt-4 border-t border-gray-50 flex items-center justify-between">
                          <span className="text-[9px] font-bold text-red-400 uppercase">Error Code</span>
                          <span className="font-mono text-xs font-bold text-red-600 bg-red-50 px-2 py-0.5 rounded">{selectedTask._result.error_code}</span>
                        </div>
                      )}
                    </div>

                    {/* 性能分析结果（async-profiler） */}
                    {(selectedTask._result.analysis_status || selectedTask._result.analysis_view_url) && (
                      <div className="bg-white p-5 rounded-xl border border-gray-100 shadow-sm">
                        <div className="flex items-center gap-2 mb-4">
                          <i className="fas fa-chart-bar text-indigo-500 text-xs" />
                          <p className="text-[10px] font-bold text-gray-400 uppercase tracking-widest">Profiling Result</p>
                          <span className={`text-[9px] font-bold px-2 py-0.5 rounded-full uppercase ${
                            selectedTask._result.analysis_status === 'completed' ? 'bg-green-100 text-green-700'
                            : selectedTask._result.analysis_status === 'processing' ? 'bg-yellow-100 text-yellow-700'
                            : selectedTask._result.analysis_status === 'failed' ? 'bg-red-100 text-red-700'
                            : 'bg-gray-100 text-gray-600'}`}>
                            {selectedTask._result.analysis_status || 'unknown'}
                          </span>
                          {selectedTask._result.analysis_mode && (
                            <span className="text-[9px] font-bold px-2 py-0.5 rounded-full bg-indigo-100 text-indigo-700">{selectedTask._result.analysis_mode}</span>
                          )}
                        </div>
                        {selectedTask._result.analysis_summary && (
                          <div className="bg-indigo-50/50 border border-indigo-100 rounded-lg p-3 mb-3">
                            <p className="text-[9px] font-bold text-indigo-400 uppercase mb-0.5">Total Records</p>
            <p className="text-sm font-bold text-indigo-800">{String((selectedTask._result.analysis_summary as Record<string, unknown>).total_records ?? '-')}</p>
                          </div>
                        )}
                        {selectedTask._result.analysis_view_url && (
                          <a href={selectedTask._result.analysis_view_url} target="_blank" rel="noopener noreferrer"
                            className="flex items-center gap-3 bg-indigo-50 hover:bg-indigo-100 border border-indigo-200 rounded-xl p-4 transition group">
                            <div className="w-10 h-10 bg-indigo-500 rounded-lg flex items-center justify-center flex-shrink-0 group-hover:bg-indigo-600 transition">
                              <i className="fas fa-fire text-white text-sm" />
                            </div>
                            <div className="flex-1 min-w-0">
                              <p className="text-sm font-bold text-indigo-800">View Flame Graph / Analysis Report</p>
                              <p className="text-[10px] text-indigo-500 truncate font-mono">{selectedTask._result.analysis_view_url}</p>
                            </div>
                            <i className="fas fa-external-link-alt text-indigo-400" />
                          </a>
                        )}
                        {selectedTask._result.analysis_error && (
                          <div className="bg-red-50 border border-red-100 rounded-lg p-3 mt-3">
                            <p className="text-[9px] font-bold text-red-400 uppercase mb-1">Analysis Error</p>
                            <pre className="text-xs text-red-800 font-mono whitespace-pre-wrap">{selectedTask._result.analysis_error}</pre>
                          </div>
                        )}
                      </div>
                    )}

                    {/* Artifact */}
                    {selectedTask._result.artifact_ref && (
                      <div className="bg-white p-4 rounded-xl border border-gray-100 shadow-sm">
                        <div className="flex items-center gap-2 mb-3">
                          <i className="fas fa-archive text-amber-500 text-xs" />
                          <p className="text-[10px] font-bold text-gray-400 uppercase tracking-widest">Artifact</p>
                        </div>
                        <div className="grid grid-cols-2 gap-3">
                          <div className="bg-gray-50 p-3 rounded-lg border border-gray-100">
                            <p className="text-[9px] font-bold text-gray-400 uppercase mb-0.5">Ref</p>
                            <p className="text-xs font-mono text-gray-800 break-all">{selectedTask._result.artifact_ref}</p>
                          </div>
                          <div className="bg-gray-50 p-3 rounded-lg border border-gray-100">
                            <p className="text-[9px] font-bold text-gray-400 uppercase mb-0.5">Size</p>
                            <p className="text-xs font-mono text-gray-800">{selectedTask._result.artifact_size ? Math.round(selectedTask._result.artifact_size / 1024) + ' KB' : '-'}</p>
                          </div>
                        </div>
                      </div>
                    )}

                    {/* 关键结果摘要 */}
                    {selectedTask._result.result_summary.length > 0 && (
                      <div className="bg-white p-4 rounded-xl border border-gray-100">
                        <div className="flex items-center justify-between mb-3">
                          <div className="flex items-center gap-2">
                            <i className="fas fa-list-ul text-blue-500 text-[10px]" />
                            <p className="text-[10px] font-bold text-gray-400 uppercase tracking-widest">Result Summary</p>
                          </div>
                          <button onClick={() => copyToClipboard(JSON.stringify(selectedTask._result?.result_json_obj || {}, null, 2))}
                            className="text-[10px] font-bold text-blue-600 hover:underline uppercase">Copy JSON</button>
                        </div>
                        <div className="grid grid-cols-2 gap-3">
                          {selectedTask._result.result_summary.map(item => (
                            <div key={item.key} className="bg-gray-50 p-3 rounded-lg border border-gray-100">
                              <p className="text-[9px] font-bold text-gray-400 uppercase mb-0.5">{item.key}</p>
                              <p className="text-xs font-mono text-gray-800 break-all leading-relaxed">{item.valueText}</p>
                            </div>
                          ))}
                        </div>
                      </div>
                    )}

                    {/* 完整输出 */}
                    <div className="space-y-4">
                      <div className="flex items-center justify-between px-1">
                        <p className="text-[10px] font-bold text-gray-400 uppercase tracking-widest">Full Output</p>
                        <button onClick={() => copyToClipboard(JSON.stringify(selectedTask._result?._raw || {}, null, 2))}
                          className="text-[9px] font-bold text-gray-400 hover:text-gray-600 uppercase transition">Copy Raw</button>
                      </div>
                      {selectedTask._result.error_message && (
                        <div className="bg-red-50 border border-red-100 rounded-lg p-3">
                          <p className="text-[9px] font-bold text-red-400 uppercase mb-1">Error Message</p>
                          <pre className="text-xs text-red-800 font-mono whitespace-pre-wrap leading-relaxed">{selectedTask._result.error_message}</pre>
                        </div>
                      )}
                      {selectedTask._result.result_json_pretty && (
                        <div className="bg-gray-900 rounded-xl p-4 overflow-hidden shadow-inner group relative">
                          <button onClick={() => copyToClipboard(selectedTask._result?.result_json_pretty || '')}
                            className="absolute top-4 right-4 text-[10px] font-bold text-gray-500 hover:text-white opacity-0 group-hover:opacity-100 transition uppercase">Copy</button>
                          <pre className="text-blue-300 font-mono text-[11px] leading-relaxed overflow-x-auto">{selectedTask._result.result_json_pretty}</pre>
                        </div>
                      )}
                      {selectedTask._result.result_data_text && (
                        <div className="bg-white border border-gray-100 rounded-xl p-4 shadow-sm group relative">
                          <button onClick={() => copyToClipboard(selectedTask._result?.result_data_text || '')}
                            className="absolute top-4 right-4 text-[10px] font-bold text-gray-400 hover:text-blue-600 opacity-0 group-hover:opacity-100 transition uppercase">Copy</button>
                          <pre className="text-xs text-gray-700 font-mono overflow-x-auto whitespace-pre-wrap leading-relaxed">{selectedTask._result.result_data_text}</pre>
                        </div>
                      )}
                      {!selectedTask._result.result_json_pretty && !selectedTask._result.result_data_text && !selectedTask._result.error_message && (
                        <div className="text-center py-10 bg-white border border-dashed border-gray-200 rounded-xl text-gray-400 italic text-xs">
                          <i className="fas fa-inbox mb-3 block text-2xl opacity-20" />
                          No output content available
                        </div>
                      )}
                    </div>
                  </div>
                ) : (
                  <div className="space-y-4">
                    <div className="bg-yellow-50 border border-yellow-100 rounded-lg p-4 text-sm text-yellow-800">
                      <div className="font-bold mb-1 flex items-center gap-2"><i className="fas fa-exclamation-triangle" /> Result Not Available</div>
                      <div className="text-xs leading-relaxed opacity-80">The agent has not reported a TaskResult yet.</div>
                    </div>
                    <div className="flex items-center justify-center">
                      <button onClick={() => openTaskDetail(selectedTask)}
                        className="px-4 py-2 bg-gray-900 text-white rounded-lg text-xs font-bold hover:bg-gray-800 transition flex items-center gap-2">
                        <i className="fas fa-sync text-[10px]" /> Refresh Detail
                      </button>
                    </div>
                  </div>
                )}
              </div>
            </DrawerSection>
          </div>
        )}
      </DetailDrawer>

      {/* ── 创建任务模态框 ──────────────────────────── */}
      {showCreateModal && (
        <div className="fixed inset-0 z-[70] flex items-center justify-center p-4 bg-black/50 backdrop-blur-sm">
          <div className="bg-white rounded-2xl shadow-2xl w-full max-w-2xl overflow-hidden flex flex-col max-h-[90vh]">
            <div className="px-6 py-4 border-b border-gray-100 flex items-center justify-between bg-gray-50/50">
              <h3 className="text-lg font-bold text-gray-800">Create New Task</h3>
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

              {/* Target Agent */}
              <div>
                <label className="block text-[10px] font-bold text-gray-400 uppercase tracking-widest mb-2">Target Agent (Optional)</label>
                <SearchableSelect options={agentOptions} value={targetAgentId} onChange={setTargetAgentId}
                  placeholder="Search by service, hostname, IP..." emptyText="No online agents found"
                  onLazyLoad={loadAgentOptions} />
              </div>

              <div className="grid grid-cols-2 gap-4">
                <div>
                  <label className="block text-[10px] font-bold text-gray-400 uppercase tracking-widest mb-2">Timeout (ms)</label>
                  <input type="number" value={timeoutMs} onChange={e => setTimeoutMs(e.target.value)}
                    className="w-full px-4 py-2.5 bg-gray-50 border border-gray-200 rounded-xl focus:ring-2 focus:ring-blue-500 focus:border-blue-500 text-sm transition" />
                </div>
                <div>
                  <label className="block text-[10px] font-bold text-gray-400 uppercase tracking-widest mb-2">Priority</label>
                  <input type="number" value={priority} onChange={e => setPriority(e.target.value)}
                    className="w-full px-4 py-2.5 bg-gray-50 border border-gray-200 rounded-xl focus:ring-2 focus:ring-blue-500 focus:border-blue-500 text-sm transition" />
                </div>
              </div>

              {/* Dynamic Instrument Form */}
              {taskType === 'dynamic_instrument' && (
                <div className="space-y-4 bg-gradient-to-br from-emerald-50/50 to-blue-50/50 border border-emerald-200/60 rounded-xl p-5">
                  <div className="flex items-center gap-2 mb-1">
                    <i className="fas fa-magic text-emerald-500 text-xs" />
                    <span className="text-[10px] font-bold text-emerald-600 uppercase tracking-widest">Dynamic Instrument Parameters</span>
                  </div>
                  <div className="grid grid-cols-2 gap-3">
                    <div>
                      <label className="block text-[10px] font-bold text-gray-400 uppercase tracking-widest mb-1.5">Class Name <span className="text-red-400">*</span></label>
                      <input type="text" value={instrClassName} onChange={e => setInstrClassName(e.target.value)} placeholder="com.example.UserService"
                        className="w-full px-3 py-2 bg-white border border-gray-200 rounded-lg focus:ring-2 focus:ring-emerald-500 text-xs font-mono transition placeholder:text-gray-300" />
                    </div>
                    <div>
                      <label className="block text-[10px] font-bold text-gray-400 uppercase tracking-widest mb-1.5">Method Name <span className="text-red-400">*</span></label>
                      <input type="text" value={instrMethodName} onChange={e => setInstrMethodName(e.target.value)} placeholder="handleLogin"
                        className="w-full px-3 py-2 bg-white border border-gray-200 rounded-lg focus:ring-2 focus:ring-emerald-500 text-xs font-mono transition placeholder:text-gray-300" />
                    </div>
                  </div>
                  <div className="grid grid-cols-2 gap-3">
                    <div>
                      <label className="block text-[10px] font-bold text-gray-400 uppercase tracking-widest mb-1.5">Type <span className="text-red-400">*</span></label>
                      <select value={instrType} onChange={e => setInstrType(e.target.value)}
                        className="w-full px-3 py-2 bg-white border border-gray-200 rounded-lg focus:ring-2 focus:ring-emerald-500 text-xs font-medium transition">
                        <option value="trace">🔍 Trace</option><option value="metric">📊 Metric</option><option value="log">📝 Log</option>
                      </select>
                    </div>
                    {instrType === 'trace' && (
                      <div>
                        <label className="block text-[10px] font-bold text-gray-400 uppercase tracking-widest mb-1.5">Span Name</label>
                        <input type="text" value={instrSpanName} onChange={e => setInstrSpanName(e.target.value)} placeholder="custom-span-name"
                          className="w-full px-3 py-2 bg-white border border-gray-200 rounded-lg focus:ring-2 focus:ring-emerald-500 text-xs font-mono transition placeholder:text-gray-300" />
                      </div>
                    )}
                  </div>
                  <div className="grid grid-cols-2 gap-3">
                    <div>
                      <label className="block text-[10px] font-bold text-gray-400 uppercase tracking-widest mb-1.5">Parameter Types</label>
                      <input type="text" value={instrParamTypes} onChange={e => setInstrParamTypes(e.target.value)} placeholder="String,int"
                        className="w-full px-3 py-2 bg-white border border-gray-200 rounded-lg focus:ring-2 focus:ring-emerald-500 text-xs font-mono transition placeholder:text-gray-300" />
                    </div>
                    <div>
                      <label className="block text-[10px] font-bold text-gray-400 uppercase tracking-widest mb-1.5">Rule ID</label>
                      <input type="text" value={instrRuleId} onChange={e => setInstrRuleId(e.target.value)} placeholder="auto-generated if empty"
                        className="w-full px-3 py-2 bg-white border border-gray-200 rounded-lg focus:ring-2 focus:ring-emerald-500 text-xs font-mono transition placeholder:text-gray-300" />
                    </div>
                  </div>
                  {instrType === 'trace' && (
                    <div className="space-y-3">
                      <div className="flex items-center gap-2 pt-2 border-t border-emerald-200/40">
                        <i className="fas fa-camera text-blue-400 text-[10px]" />
                        <span className="text-[10px] font-bold text-blue-500 uppercase tracking-widest">Capture Options (Trace only)</span>
                      </div>
                      <div className="grid grid-cols-3 gap-3">
                        <div>
                          <label className="block text-[10px] font-bold text-gray-400 uppercase tracking-widest mb-1.5">Capture Args</label>
                          <input type="text" value={instrCaptureArgs} onChange={e => setInstrCaptureArgs(e.target.value)} placeholder="0,2 or *"
                            className="w-full px-3 py-2 bg-white border border-gray-200 rounded-lg focus:ring-2 focus:ring-blue-500 text-xs font-mono transition placeholder:text-gray-300" />
                        </div>
                        <div>
                          <label className="block text-[10px] font-bold text-gray-400 uppercase tracking-widest mb-1.5">Capture Return</label>
                          <input type="text" value={instrCaptureReturn} onChange={e => setInstrCaptureReturn(e.target.value)} placeholder="* or id,name"
                            className="w-full px-3 py-2 bg-white border border-gray-200 rounded-lg focus:ring-2 focus:ring-blue-500 text-xs font-mono transition placeholder:text-gray-300" />
                        </div>
                        <div>
                          <label className="block text-[10px] font-bold text-gray-400 uppercase tracking-widest mb-1.5">Max Length</label>
                          <input type="text" value={instrCaptureMaxLen} onChange={e => setInstrCaptureMaxLen(e.target.value)} placeholder="256"
                            className="w-full px-3 py-2 bg-white border border-gray-200 rounded-lg focus:ring-2 focus:ring-blue-500 text-xs font-mono transition placeholder:text-gray-300" />
                        </div>
                      </div>
                    </div>
                  )}
                  <div className="flex items-center gap-4 pt-2 border-t border-emerald-200/40">
                    <label className="flex items-center gap-2 cursor-pointer select-none">
                      <input type="checkbox" checked={instrForce} onChange={e => setInstrForce(e.target.checked)}
                        className="w-4 h-4 text-emerald-600 bg-gray-100 border-gray-300 rounded focus:ring-emerald-500" />
                      <span className="text-[10px] font-bold text-gray-500 uppercase">Force (ignore conflicts)</span>
                    </label>
                    <input type="text" value={instrMethodDesc} onChange={e => setInstrMethodDesc(e.target.value)}
                      placeholder="JVM descriptor (advanced)"
                      className="flex-1 px-3 py-1.5 bg-white/60 border border-gray-200 rounded-lg text-[10px] font-mono text-gray-400 transition placeholder:text-gray-300 focus:ring-1 focus:ring-emerald-500" />
                  </div>
                </div>
              )}

              {/* Dynamic Uninstrument Form */}
              {taskType === 'dynamic_uninstrument' && (
                <div className="space-y-4 bg-gradient-to-br from-amber-50/50 to-orange-50/50 border border-amber-200/60 rounded-xl p-5">
                  <div className="flex items-center gap-2 mb-1">
                    <i className="fas fa-undo-alt text-amber-500 text-xs" />
                    <span className="text-[10px] font-bold text-amber-600 uppercase tracking-widest">Dynamic Uninstrument Parameters</span>
                  </div>
                  <div className="flex gap-1 bg-gray-100 rounded-lg p-1">
                    <button type="button" onClick={() => setUninstrMode('rule_id')}
                      className={`flex-1 px-3 py-1.5 rounded-md text-[10px] uppercase tracking-widest transition font-medium ${uninstrMode === 'rule_id' ? 'bg-white shadow-sm text-amber-700 font-bold' : 'text-gray-500 hover:text-gray-700'}`}>
                      <i className="fas fa-key mr-1" /> By Rule ID
                    </button>
                    <button type="button" onClick={() => setUninstrMode('method')}
                      className={`flex-1 px-3 py-1.5 rounded-md text-[10px] uppercase tracking-widest transition font-medium ${uninstrMode === 'method' ? 'bg-white shadow-sm text-amber-700 font-bold' : 'text-gray-500 hover:text-gray-700'}`}>
                      <i className="fas fa-crosshairs mr-1" /> By Method
                    </button>
                  </div>
                  <div className="min-h-[160px]">
                    {uninstrMode === 'rule_id' ? (
                      <div>
                        <label className="block text-[10px] font-bold text-gray-400 uppercase tracking-widest mb-1.5">Rule ID <span className="text-red-400">*</span></label>
                        <input type="text" value={uninstrRuleId} onChange={e => setUninstrRuleId(e.target.value)} placeholder="UserService.handleLogin_trace"
                          className="w-full px-3 py-2 bg-white border border-gray-200 rounded-lg focus:ring-2 focus:ring-amber-500 text-xs font-mono transition placeholder:text-gray-300" />
                        <p className="text-[9px] text-gray-400 mt-1.5"><i className="fas fa-info-circle mr-0.5" /> Format: <code className="bg-gray-100 px-1 rounded">SimpleClassName.methodName_type</code></p>
                      </div>
                    ) : (
                      <div className="space-y-3">
                        <div className="grid grid-cols-2 gap-3">
                          <div>
                            <label className="block text-[10px] font-bold text-gray-400 uppercase tracking-widest mb-1.5">Class Name <span className="text-red-400">*</span></label>
                            <input type="text" value={uninstrClassName} onChange={e => setUninstrClassName(e.target.value)} placeholder="com.example.UserService"
                              className="w-full px-3 py-2 bg-white border border-gray-200 rounded-lg focus:ring-2 focus:ring-amber-500 text-xs font-mono transition placeholder:text-gray-300" />
                          </div>
                          <div>
                            <label className="block text-[10px] font-bold text-gray-400 uppercase tracking-widest mb-1.5">Method Name <span className="text-red-400">*</span></label>
                            <input type="text" value={uninstrMethodName} onChange={e => setUninstrMethodName(e.target.value)} placeholder="handleLogin"
                              className="w-full px-3 py-2 bg-white border border-gray-200 rounded-lg focus:ring-2 focus:ring-amber-500 text-xs font-mono transition placeholder:text-gray-300" />
                          </div>
                        </div>
                        <div>
                          <label className="block text-[10px] font-bold text-gray-400 uppercase tracking-widest mb-1.5">Type (Optional)</label>
                          <select value={uninstrType} onChange={e => setUninstrType(e.target.value)}
                            className="w-full px-3 py-2 bg-white border border-gray-200 rounded-lg focus:ring-2 focus:ring-amber-500 text-xs font-medium transition">
                            <option value="">All types (revert all)</option><option value="trace">🔍 Trace</option><option value="metric">📊 Metric</option><option value="log">📝 Log</option>
                          </select>
                        </div>
                      </div>
                    )}
                  </div>
                </div>
              )}

              {/* Generic Parameters JSON */}
              {taskType !== 'dynamic_instrument' && taskType !== 'dynamic_uninstrument' && (
                <div>
                  <div className="flex items-center justify-between mb-2">
                    <label className="block text-[10px] font-bold text-gray-400 uppercase tracking-widest">Parameters (JSON)</label>
                    <span className="text-[9px] text-gray-300 font-mono">Optional</span>
                  </div>
                  <textarea value={parametersJson} onChange={e => setParametersJson(e.target.value)} rows={4} placeholder='{ "key": "value" }'
                    className="w-full px-4 py-3 bg-gray-900 text-blue-300 font-mono text-xs rounded-xl focus:ring-2 focus:ring-blue-500 transition shadow-inner" />
                </div>
              )}
            </div>

            <div className="px-6 py-4 bg-gray-50 border-t border-gray-100 flex gap-3">
              <button onClick={() => { setShowCreateModal(false); resetCreateForm(); }}
                className="flex-1 px-4 py-2.5 border border-gray-200 text-gray-600 rounded-xl font-bold hover:bg-gray-100 transition">Cancel</button>
              <button onClick={handleSubmitTask}
                className="flex-1 px-4 py-2.5 bg-blue-600 text-white rounded-xl font-bold hover:bg-blue-700 transition shadow-lg shadow-blue-200">Submit Task</button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
