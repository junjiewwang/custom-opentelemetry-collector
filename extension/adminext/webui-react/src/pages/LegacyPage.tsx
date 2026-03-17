/**
 * Legacy 页面 - 通过 iframe 嵌入旧版 Alpine.js 前端
 *
 * 旧页面（Dashboard、Apps、Instances 等）在 React 完全迁移前，
 * 通过 iframe 嵌入到新的 React Shell 中。
 */

import { useEffect, useRef } from 'react';
import { apiClient } from '@/api/client';

interface LegacyPageProps {
  /** 旧页面中的视图名称（对应 Alpine.js 的 currentView） */
  view: string;
}

export default function LegacyPage({ view }: LegacyPageProps) {
  const iframeRef = useRef<HTMLIFrameElement>(null);

  useEffect(() => {
    // 当 iframe 加载完成后，尝试传递 API Key 和视图切换指令
    const iframe = iframeRef.current;
    if (!iframe) return;

    const handleLoad = () => {
      try {
        // 通过 postMessage 将 API Key 和目标视图传递给旧前端
        iframe.contentWindow?.postMessage({
          type: 'OTEL_ADMIN_NAVIGATE',
          apiKey: apiClient.getApiKey(),
          view,
        }, '*');
      } catch {
        // 跨域情况下会失败，这是预期的
      }
    };

    iframe.addEventListener('load', handleLoad);
    return () => iframe.removeEventListener('load', handleLoad);
  }, [view]);

  return (
    <div className="h-[calc(100vh-4rem)] -m-8">
      <iframe
        ref={iframeRef}
        src={`/legacy/?view=${view}&apiKey=${encodeURIComponent(apiClient.getApiKey())}`}
        className="w-full h-full border-0"
        title={`Legacy ${view} view`}
      />
    </div>
  );
}
