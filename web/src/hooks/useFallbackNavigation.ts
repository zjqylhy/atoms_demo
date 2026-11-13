import { useEffect, useRef, useState } from 'react';
import { apiJSON, apiText, ApiError } from '../api';
import { fallbackRunKey, previewVersion, readFallbackMemory, saveFallbackMemory, type FallbackOrigin, type FallbackProject, type FollowedRun } from '../fallbackNavigation';

type Candidate<P> = { project: P; version: string; token: number };
type Attempt<P> = { token: number; navigation: number; source: P; automatic: boolean; controller: AbortController; timer: ReturnType<typeof setTimeout> };

export function useFallbackNavigation<P extends FallbackProject>(options: {
  user: string; current?: P; selected: string;
  onCommit: (project: P, version: string, from: string) => void;
  onError: (message: string) => void;
}) {
  const latest = useRef(options); latest.current = options;
  const [memory] = useState(() => readFallbackMemory(options.user));
  const handled = useRef(new Set(memory.handled));
  const [followed, setFollowed] = useState<FollowedRun>();
  const [candidate, setCandidate] = useState<Candidate<P>>();
  const [phase, setPhase] = useState<'checking' | 'loading'>();
  const [origin, setOrigin] = useState<FallbackOrigin>();
  const loaded = useRef('');
  const navigation = useRef(0), serial = useRef(0), alive = useRef(true);
  const attempt = useRef<Attempt<P> | undefined>(undefined);

  function remember(next?: FollowedRun) {
    setFollowed(next);
    saveFallbackMemory(options.user, handled.current, next);
  }
  function clearAttempt() {
    if (attempt.current) { clearTimeout(attempt.current.timer); attempt.current.controller.abort(); }
    attempt.current = undefined; setCandidate(undefined); setPhase(undefined);
  }
  function cancel() {
    navigation.current++; clearAttempt(); remember(); setOrigin(undefined); loaded.current = '';
    return navigation.current;
  }
  function isCurrent(stamp: number) { return alive.current && navigation.current === stamp; }
  function follow(project: P, stamp: number, restoring = false) {
    if (!isCurrent(stamp) || !project.active_run) return;
    const next = { project: project.id, run: project.active_run };
    if (restoring && (memory.followed?.project !== next.project || memory.followed?.run !== next.run)) return;
    if (!handled.current.has(fallbackRunKey(next))) remember(next);
  }
  function valid(item: Attempt<P>) {
    const current = latest.current.current;
    return alive.current && attempt.current === item && isCurrent(item.navigation) && latest.current.selected === item.source.id && current?.id === item.source.id &&
      (!item.automatic || (!current.active_run && current.current_task === item.source.current_task && current.recovery?.run_id === item.source.recovery?.run_id));
  }
  function fail(item: Attempt<P>, message: string) {
    const notify = valid(item);
    if (attempt.current !== item) return;
    clearAttempt();
    if (notify) latest.current.onError(`演示应用未能打开：${message}。原任务与成果已保留，可手动重试。`);
  }
  async function openDemo(source: P, automatic = false) {
    const recovery = source.recovery;
    if (!recovery || recovery.unavailable || attempt.current || latest.current.selected !== source.id) return;
    const key = fallbackRunKey({ project: source.id, run: recovery.run_id });
    if (automatic && handled.current.has(key)) return;
    handled.current.add(key);
    if (handled.current.size > 100) handled.current.delete(handled.current.values().next().value!);
    remember();
    const controller = new AbortController();
    const item: Attempt<P> = {
      token: ++serial.current, navigation: navigation.current, source, automatic, controller,
      timer: setTimeout(() => fail(item, '预览载入超时'), 20000),
    };
    attempt.current = item; setPhase('checking'); latest.current.onError('');
    try {
      const demo: P = await apiJSON(`/projects/${recovery.project_id}`, { signal: controller.signal });
      const version = previewVersion(demo);
      if (demo.source?.kind !== 'fallback' || !version) throw Error('没有可用的演示交付版本');
      const html = await apiText(`/projects/${demo.id}/preview?version=${version}`, { signal: controller.signal });
      if (!html.trim()) throw Error('预览内容为空');
      if (!valid(item)) { if (attempt.current === item) clearAttempt(); return; }
      setCandidate({ project: demo, version, token: item.token }); setPhase('loading');
    } catch (error) {
      if ((error as Error).name !== 'AbortError') fail(item, (error as Error).message);
    }
  }
  function previewReady(project: string, token?: number) {
    loaded.current = project;
    const item = attempt.current;
    if (token !== undefined) {
      if (!item || item.token !== token || !candidate || candidate.project.id !== project) return;
      if (!valid(item)) { clearAttempt(); return; }
      clearTimeout(item.timer); attempt.current = undefined; navigation.current++;
      setOrigin({ project: item.source.id, reason: item.source.recovery!.reason, ready: true });
      latest.current.onCommit(candidate.project, candidate.version, item.source.id);
      setCandidate(undefined); setPhase(undefined);
    } else if (latest.current.current?.id === project) {
      setOrigin(value => value ? { ...value, ready: true } : value);
    }
  }
  function previewFailed(project: string, error: Error, token?: number) {
    const item = attempt.current;
    if (item && item.token === token) fail(item, error.message);
    else if (latest.current.current?.id === project) setOrigin(value => value ? { ...value, ready: false } : value);
  }
  async function restoreOrigin(demo: P, from: string, stamp: number) {
    try {
      const source: P = await apiJSON(`/projects/${from}`);
      if (!isCurrent(stamp)) return;
      if (demo.source?.kind !== 'fallback' || source.recovery?.project_id !== demo.id || source.recovery.unavailable) return false;
      setOrigin({ project: from, reason: source.recovery.reason, ready: loaded.current === demo.id });
      return true;
    } catch (error) {
      if (isCurrent(stamp) && error instanceof ApiError && error.status === 404 && demo.source?.kind === 'fallback') {
        setOrigin({ project: from, reason: '原任务已删除或不可用。', missing: true, ready: loaded.current === demo.id }); return true;
      }
      if (isCurrent(stamp) && (error as Error).name !== 'AbortError') latest.current.onError('暂时无法读取原任务，请刷新后重试。');
    }
  }
  useEffect(() => {
    const p = options.current;
    if (!followed || !p || options.selected !== followed.project || p.id !== followed.project) return;
    const run = p.runs.find(r => r.id === followed.run);
    if (run && ['completed', 'stopped', 'ended', 'interrupted', 'waiting_input', 'waiting_plan'].includes(run.status)) { remember(); return; }
    if (!p.active_run && run?.status === 'failed' && p.tasks.find(t => t.id === p.current_task)?.status === 'failed' && p.recovery?.run_id === followed.run && !p.recovery.unavailable) void openDemo(p, true);
  }, [options.current, options.selected, followed]);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false; navigation.current++;
      if (attempt.current) { clearTimeout(attempt.current.timer); attempt.current.controller.abort(); attempt.current = undefined; }
    };
  }, []);
  return { candidate, phase, origin, cancel, follow, openDemo, restoreOrigin, previewReady, previewFailed, stamp: () => navigation.current, isCurrent };
}
