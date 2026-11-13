export type FollowedRun = { project: string; run: string };
export type FallbackOrigin = { project: string; reason: string; missing?: boolean; ready?: boolean };
export type FallbackProject = {
  id: string;
  active_run?: string;
  current_task?: string;
  source?: { kind: string };
  recovery?: { project_id: string; run_id: string; reason: string; unavailable?: boolean };
  tasks: { id: string; status: string }[];
  runs: { id: string; status: string }[];
  head?: string;
  versions: { id: string; preview_ready?: boolean }[];
};

const projectID = /^[a-f0-9]{32}$/;
export function projectLocation(id: string, from?: string) {
  return `/?project=${id}${from ? `&fallbackFrom=${from}` : ''}`;
}
export function parseProjectLocation(url: URL): { project: string; from?: string } | undefined {
  const id = url.searchParams.get('project');
  const from = url.searchParams.get('fallbackFrom');
  const keys = [...url.searchParams.keys()];
  if (url.pathname !== '/' || url.hash || !id || !projectID.test(id)) return;
  if (keys.length === 1 && keys[0] === 'project') return { project: id };
  if (keys.length === 2 && new Set(keys).size === 2 && keys.includes('fallbackFrom') && from && projectID.test(from) && from !== id) return { project: id, from };
}

// Only IDs are stored, scoped to this account and browser tab. Storage is optional.
export function readFallbackMemory(user: string): { handled: string[]; followed?: FollowedRun } {
  try {
    const saved = JSON.parse(sessionStorage.getItem(`atoms:fallback:${user}`) || 'null');
    return {
      handled: Array.isArray(saved?.handled) ? saved.handled.filter((v: unknown) => typeof v === 'string').slice(-100) : [],
      followed: typeof saved?.followed?.project === 'string' && typeof saved?.followed?.run === 'string' ? saved.followed : undefined,
    };
  } catch { return { handled: [] }; }
}
export function saveFallbackMemory(user: string, handled: Set<string>, followed?: FollowedRun) {
  try { sessionStorage.setItem(`atoms:fallback:${user}`, JSON.stringify({ handled: [...handled].slice(-100), followed })); } catch { /* In-memory deduplication still applies. */ }
}
export function fallbackRunKey(run: FollowedRun) { return `${run.project}:${run.run}`; }
export function previewVersion(project: FallbackProject) {
  return project.versions.find(v => v.id === project.head && v.preview_ready)?.id;
}
