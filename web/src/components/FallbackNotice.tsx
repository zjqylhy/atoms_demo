import type { FallbackOrigin } from '../fallbackNavigation';

export function FallbackNotice({ origin, onReturn }: { origin?: FallbackOrigin; onReturn: (id: string) => void }) {
  return <div className="source-banner fallback-notice" role="status">
    <div><strong>{origin?.ready ? '大模型服务不可用，已切换至演示应用兜底。' : '演示模式'}</strong><p>此为预置演示，原需求尚未完成。</p></div>
    {origin && <div className="fallback-actions">
      <details><summary>查看原因</summary><p>{origin.reason}</p></details>
      {origin.missing ? <span>原任务不可用</span> : <button onClick={() => onReturn(origin.project)}>返回原任务</button>}
    </div>}
  </div>;
}
