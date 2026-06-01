/**
 * Storage Admin API 类型定义
 *
 * 对应后端 observabilitystorageext Admin 数据模型。
 */

// ============================================================================
// Storage Status
// ============================================================================

/** 信号类型 */
export type SignalType = 'trace' | 'metric' | 'log';

/** 存储状态信息 */
export interface StorageStatus {
  provider: string;
  healthy: boolean;
  version?: string;
  indices?: IndexInfo[];
  details?: Record<string, unknown>;
}

/** 索引/表信息 */
export interface IndexInfo {
  name: string;
  docs_count: number;
  size_bytes: number;
  signal: SignalType;
}

/** 存储健康检查结果 */
export interface StorageHealth {
  healthy: boolean;
  message?: string;
  latency_ms?: number;
}

// ============================================================================
// Retention
// ============================================================================

/** 保留策略（按信号类型） */
export interface RetentionPolicies {
  trace?: RetentionPolicy;
  metric?: RetentionPolicy;
  log?: RetentionPolicy;
}

/** 单个保留策略 */
export interface RetentionPolicy {
  duration: string;  // Go duration string, e.g. "720h"
}

// ============================================================================
// Disk Usage
// ============================================================================

/** 磁盘使用信息 */
export interface DiskUsage {
  total_bytes: number;
  used_bytes: number;
  available_bytes: number;
  by_signal?: Record<SignalType, number>;
}

// ============================================================================
// Purge
// ============================================================================

/** 清除操作结果 */
export interface PurgeResult {
  deleted_count: number;
  freed_bytes?: number;
  message?: string;
}

// ============================================================================
// 前端展示辅助
// ============================================================================

/** 格式化字节数为人类可读字符串 */
export function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const k = 1024;
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return `${(bytes / Math.pow(k, i)).toFixed(1)} ${units[i]}`;
}

/** 解析 Go duration 为人类可读字符串 */
export function formatRetention(duration: string): string {
  // Go duration: "720h0m0s" → parse hours
  const match = duration.match(/^(\d+)h/);
  if (match) {
    const hours = parseInt(match[1], 10);
    if (hours >= 24) {
      const days = Math.floor(hours / 24);
      return `${days} 天`;
    }
    return `${hours} 小时`;
  }
  return duration;
}
