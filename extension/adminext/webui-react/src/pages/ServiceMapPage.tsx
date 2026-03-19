/**
 * ServiceMapPage - 服务拓扑图可视化
 *
 * 功能：
 * - 基于 Jaeger Dependencies API 获取服务间调用关系
 * - 使用 ECharts Graph 图表展示服务拓扑
 * - 支持时间范围选择（lookback）
 * - 节点点击可跳转到 Traces / Metrics 页面
 * - 边上显示调用次数
 */

import { useState, useEffect, useCallback, useMemo } from 'react';
import { useNavigate } from 'react-router-dom';
import ReactEChartsCore from 'echarts-for-react/lib/core';
import * as echarts from 'echarts/core';
import { GraphChart } from 'echarts/charts';
import {
  TooltipComponent,
  LegendComponent,
} from 'echarts/components';
import { CanvasRenderer } from 'echarts/renderers';
import EmptyState from '@/components/EmptyState';
import { apiClient } from '@/api/client';
import type { JaegerDependencyLink } from '@/types/trace';
import { getServiceColor } from '@/utils/trace';

// 注册 ECharts Graph 组件
echarts.use([GraphChart, TooltipComponent, LegendComponent, CanvasRenderer]);

/** 时间范围选项 */
const LOOKBACK_OPTIONS = [
  { label: 'Last 1 hour', value: 3600_000 },
  { label: 'Last 6 hours', value: 6 * 3600_000 },
  { label: 'Last 12 hours', value: 12 * 3600_000 },
  { label: 'Last 24 hours', value: 24 * 3600_000 },
  { label: 'Last 2 days', value: 2 * 86400_000 },
  { label: 'Last 7 days', value: 7 * 86400_000 },
];

export default function ServiceMapPage() {
  const navigate = useNavigate();

  // State
  const [dependencies, setDependencies] = useState<JaegerDependencyLink[]>([]);
  const [lookback, setLookback] = useState(24 * 3600_000); // 默认 24h
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [available, setAvailable] = useState<boolean | null>(null);

  // ========================================================================
  // 加载 Dependencies 数据
  // ========================================================================

  const loadDependencies = useCallback(async () => {
    setLoading(true);
    setError('');

    try {
      const endTs = Date.now();
      const resp = await apiClient.getDependencies(endTs, lookback);
      const data = resp.data ?? [];
      setDependencies(data);
      setAvailable(true);

      if (data.length === 0) {
        setError('No service dependencies found in the selected time range.');
      }
    } catch {
      setAvailable(false);
      setError('Failed to load service dependencies. Please check Jaeger backend configuration.');
      setDependencies([]);
    } finally {
      setLoading(false);
    }
  }, [lookback]);

  useEffect(() => {
    loadDependencies();
  }, [loadDependencies]);

  // ========================================================================
  // 构建 ECharts Graph 数据
  // ========================================================================

  const chartOption = useMemo(() => {
    if (dependencies.length === 0) return {};

    // 收集所有唯一的 Service 节点
    const serviceSet = new Set<string>();
    for (const dep of dependencies) {
      serviceSet.add(dep.parent);
      serviceSet.add(dep.child);
    }

    // 统计每个 service 的总调用次数（入+出），用于决定节点大小
    const callCountMap = new Map<string, number>();
    for (const dep of dependencies) {
      callCountMap.set(dep.parent, (callCountMap.get(dep.parent) ?? 0) + dep.callCount);
      callCountMap.set(dep.child, (callCountMap.get(dep.child) ?? 0) + dep.callCount);
    }

    const maxCalls = Math.max(...callCountMap.values(), 1);

    // 构建节点
    const nodes = Array.from(serviceSet).map(name => {
      const calls = callCountMap.get(name) ?? 0;
      // 节点大小：最小 30，最大 80，按调用量对数缩放
      const size = 30 + (Math.log(calls + 1) / Math.log(maxCalls + 1)) * 50;
      return {
        name,
        symbolSize: size,
        itemStyle: {
          color: getServiceColor(name),
          borderColor: '#fff',
          borderWidth: 2,
          shadowBlur: 10,
          shadowColor: 'rgba(0,0,0,0.1)',
        },
        label: {
          show: true,
          fontSize: 11,
          color: '#374151',
          fontWeight: 'bold' as const,
        },
      };
    });

    // 构建边
    const links = dependencies.map(dep => ({
      source: dep.parent,
      target: dep.child,
      value: dep.callCount,
      lineStyle: {
        width: Math.max(1, Math.min(6, Math.log(dep.callCount + 1) * 1.5)),
        color: '#9ca3af',
        curveness: 0.2,
      },
      label: {
        show: dep.callCount > 0,
        formatter: `${formatCallCount(dep.callCount)}`,
        fontSize: 10,
        color: '#6b7280',
      },
    }));

    return {
      tooltip: {
        trigger: 'item' as const,
        backgroundColor: 'rgba(255, 255, 255, 0.95)',
        borderColor: '#e5e7eb',
        borderWidth: 1,
        textStyle: { color: '#374151', fontSize: 12 },
        formatter: (params: { dataType: string; data: { name?: string; source?: string; target?: string; value?: number } }) => {
          if (params.dataType === 'node') {
            const calls = callCountMap.get(params.data.name ?? '') ?? 0;
            return `<div style="font-weight:600">${params.data.name}</div>
              <div style="color:#6b7280;margin-top:4px">Total calls: ${calls.toLocaleString()}</div>
              <div style="color:#9ca3af;font-size:11px;margin-top:2px">Click to view traces</div>`;
          }
          if (params.dataType === 'edge') {
            return `<div><b>${params.data.source}</b> → <b>${params.data.target}</b></div>
              <div style="color:#6b7280;margin-top:4px">Calls: ${(params.data.value ?? 0).toLocaleString()}</div>`;
          }
          return '';
        },
      },
      animationDuration: 1500,
      animationEasingUpdate: 'quinticInOut' as const,
      series: [
        {
          type: 'graph' as const,
          layout: 'force' as const,
          data: nodes,
          links,
          roam: true,
          draggable: true,
          force: {
            repulsion: 300,
            gravity: 0.1,
            edgeLength: [100, 250],
            layoutAnimation: true,
          },
          emphasis: {
            focus: 'adjacency' as const,
            lineStyle: { width: 4 },
          },
          edgeSymbol: ['none', 'arrow'],
          edgeSymbolSize: [0, 8],
        },
      ],
    };
  }, [dependencies]);

  // ========================================================================
  // 节点点击事件：跳转到 Traces 页面
  // ========================================================================

  const handleChartClick = useCallback(
    (params: { dataType: string; data: { name?: string } }) => {
      if (params.dataType === 'node' && params.data.name) {
        navigate(`/traces?service=${encodeURIComponent(params.data.name)}&lookback=1h`);
      }
    },
    [navigate],
  );

  // ========================================================================
  // 渲染
  // ========================================================================

  return (
    <div className="fade-in">
      {/* Page Header */}
      <div className="mb-6">
        <h2 className="text-2xl font-bold text-gray-800 flex items-center gap-3">
          <i className="fas fa-project-diagram text-primary-600" />
          Service Map
        </h2>
        <p className="text-gray-500 mt-1">
          Service dependency topology based on Trace data
        </p>
      </div>

      {/* Controls */}
      <div className="bg-white rounded-xl shadow-sm border border-gray-200 p-6 mb-6">
        <div className="flex items-center gap-4 flex-wrap">
          {/* Lookback Selector */}
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">Time Range</label>
            <select
              value={lookback}
              onChange={(e) => setLookback(Number(e.target.value))}
              className="px-3 py-2 border border-gray-200 rounded-lg text-sm focus:ring-2 focus:ring-primary-500 focus:border-primary-500"
            >
              {LOOKBACK_OPTIONS.map(opt => (
                <option key={opt.value} value={opt.value}>{opt.label}</option>
              ))}
            </select>
          </div>

          {/* Refresh */}
          <button
            onClick={loadDependencies}
            disabled={loading}
            className="mt-6 px-4 py-2 bg-gray-100 text-gray-600 rounded-lg hover:bg-gray-200 transition flex items-center gap-2 text-sm disabled:opacity-50"
          >
            <i className={`fas fa-sync-alt ${loading ? 'fa-spin' : ''}`} />
            Refresh
          </button>

          {/* Stats */}
          {dependencies.length > 0 && (
            <div className="mt-6 flex items-center gap-4 text-sm text-gray-500">
              <span>
                <i className="fas fa-circle text-primary-400 text-xs mr-1" />
                {new Set([...dependencies.map(d => d.parent), ...dependencies.map(d => d.child)]).size} Services
              </span>
              <span>
                <i className="fas fa-arrow-right text-gray-400 text-xs mr-1" />
                {dependencies.length} Dependencies
              </span>
              <span>
                <i className="fas fa-phone text-gray-400 text-xs mr-1" />
                {dependencies.reduce((s, d) => s + d.callCount, 0).toLocaleString()} Total Calls
              </span>
            </div>
          )}
        </div>
      </div>

      {/* Graph Chart */}
      {loading && (
        <div className="bg-white rounded-xl shadow-sm border border-gray-200">
          <EmptyState
            icon="fas fa-spinner fa-spin"
            iconColor="text-blue-300"
            iconBg="bg-blue-50"
            title="Loading service dependencies..."
            size="lg"
          />
        </div>
      )}

      {!loading && available === false && (
        <div className="bg-white rounded-xl shadow-sm border border-gray-200">
          <EmptyState
            icon="fas fa-exclamation-triangle"
            iconColor="text-yellow-500"
            iconBg="bg-yellow-50"
            title="Jaeger Backend Not Available"
            description={
              <span>
                Please configure <code className="bg-gray-100 px-1 rounded">admin.observability.jaeger.endpoint</code> in Collector settings to enable Service Map.
              </span>
            }
            size="lg"
          />
        </div>
      )}

      {!loading && available && dependencies.length === 0 && error && (
        <div className="bg-white rounded-xl shadow-sm border border-gray-200">
          <EmptyState
            icon="fas fa-project-diagram"
            title="No Dependencies Found"
            description={error}
            size="lg"
          />
        </div>
      )}

      {!loading && dependencies.length > 0 && (
        <div className="bg-white rounded-xl shadow-sm border border-gray-200 p-4">
          {/* 提示 */}
          <div className="flex items-center gap-4 mb-2 px-2 text-xs text-gray-400">
            <span><i className="fas fa-mouse-pointer mr-1" />Click node → View Traces</span>
            <span><i className="fas fa-arrows-alt mr-1" />Drag to move nodes</span>
            <span><i className="fas fa-search-plus mr-1" />Scroll to zoom</span>
          </div>
          <ReactEChartsCore
            echarts={echarts}
            option={chartOption}
            style={{ height: 600, width: '100%' }}
            notMerge={true}
            lazyUpdate={true}
            onEvents={{ click: handleChartClick }}
          />
        </div>
      )}
    </div>
  );
}

// ============================================================================
// 辅助函数
// ============================================================================

/** 格式化调用次数为简写 */
function formatCallCount(count: number): string {
  if (count >= 1_000_000) return `${(count / 1_000_000).toFixed(1)}M`;
  if (count >= 1000) return `${(count / 1000).toFixed(1)}k`;
  return String(count);
}
