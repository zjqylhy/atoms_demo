import { useEffect, useRef, useState } from "react";
import {
  ArrowUp,
  Plus,
  PanelLeftClose,
  PanelLeftOpen,
  Sparkles,
  FolderOpen,
  Code2,
  Eye,
  History,
  Download,
  Smartphone,
  Monitor,
  RotateCw,
  Check,
  ChevronDown,
  X,
  FileText,
  Database,
  Square,
  Play,
  AlertCircle,
  ChevronRight,
  ListChecks,
  LoaderCircle,
  MessageCircle,
} from "lucide-react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { diffLines } from "diff";

type FileInfo = {
  hash: string;
  size: number;
  kind: string;
  read_only?: boolean;
};
type Report = {
  id: string;
  kind: string;
  status: string;
  checks: { name: string; status: string; detail: string }[];
  details?: Record<string, unknown>;
};
type Version = {
  preview_ready?: boolean;
  id: string;
  number: number;
  summary: string;
  at: string;
  manifest: Record<string, FileInfo>;
  reports: Report[];
};
type Task = {
  last_handoff?: {status:string;reason:string;evidence?:string[];todo?:string[];artifacts?:Record<string,FileInfo>};
  id: string;
  goal: string;
  mode: string;
  status: string;
  checkpoints: number;
  plan?: {
    revision: string;
    text: string;
    approved: boolean;
    acceptance: string[];
  };
  question?: { id: string; text: string; options?: string[] };
};
type Message = { id: string; role: string; text: string; kind: string };
type ActivityEvent = { seq: number; kind: string; role?: string; text: string; at: string };
type Process = { start: number; entries: (Message | ActivityEvent)[] };
type Project = {
  preview_ready?: boolean;
  id: string;
  title: string;
  updated: string;
  active_run?: string;
  current_task?: string;
  files: Record<string, FileInfo>;
  messages: Message[];
  tasks: Task[];
  runs: {
    id: string;
    status: string;
    error?: string;
    calls: number;
    tools: number;
    usage: { total: number; known: boolean };
  }[];
  versions: Version[];
  reports: Report[];
  events: ActivityEvent[];
};
type Bootstrap = {
  csrf: string;
  model_ready: boolean;
  runner_configured: boolean;
  notice: string;
};
const statusText: Record<string, string> = {
  running: "执行中",
  waiting_plan: "等待批准",
  waiting_input: "等待回答",
  completed: "已完成",
  failed: "执行失败",
  stopped: "已停止",
  interrupted: "执行中断",
  ended: "已结束",
};
const roleNames: Record<string, string> = {
  Mike: "协调",
  Emma: "产品",
  Bob: "架构",
  Alex: "开发",
  David: "分析",
  system: "系统",
  user: "你",
};
const roles = ["Mike", "Emma", "Bob", "Alex", "David"];
const examples = [
  [
    "交互应用",
    "做一个阅读清单：支持添加书名、标记已读，以及按阅读状态筛选。风格简洁。",
  ],
  [
    "产品文档",
    "为一个面向独立咖啡店的会员积分工具，梳理首版需求与验收标准，输出 Markdown 文档。",
  ],
  [
    "数据分析",
    "分析我提供的销售数据，按品类汇总销售额，生成 CSV、柱状图和简短结论。",
  ],
];
function Markdown({ text }: { text: string }) {
  return (
    <div className="markdown">
      <ReactMarkdown remarkPlugins={[remarkGfm]} skipHtml>
        {text}
      </ReactMarkdown>
    </div>
  );
}
function ChatMessage({ message: m }: { message: Message }) {
  return (
    <article className={"message " + (m.role === "user" ? "user" : "")}>
      <div className="message-author">
        {m.role !== "user" && (
          <span className={"role-dot face-" + Math.max(0, roles.indexOf(m.role))}>
            {m.role[0]}
          </span>
        )}
        <b>{m.role === "user" ? "你" : m.role}</b>
        <small>{m.role !== "user" && roleNames[m.role]}</small>
      </div>
      <Markdown text={m.text} />
    </article>
  );
}
function EventRow({ event }: { event: ActivityEvent }) {
  return (
    <div className="event">
      <time dateTime={event.at}>
        {new Date(event.at).toLocaleTimeString("zh-CN", {
          hour: "2-digit", minute: "2-digit", second: "2-digit",
        })}
      </time>
      <b>{event.role === "user" ? "你" : event.role === "system" ? "系统" : event.role || "系统"}</b>
      <span>{event.text}</span>
    </div>
  );
}
function ChatHistory({ messages, events, running }: {
  messages: Message[];
  events: ActivityEvent[];
  running: boolean;
}) {
  const timeline: (Message | ActivityEvent | Process)[] = [];
  let process: Process | undefined;
  let nextMessage = 0;
  let pending: ActivityEvent[] = [];
  for (const event of events) {
    switch (event.kind) {
      case "message": {
        // The server appends one message event for every chat message, in the same order.
        const message = messages[nextMessage++];
        if (!message) break;
        if (message.role === "user") process = undefined;
        if (message.kind === "agent" && process) process.entries.push(message);
        else timeline.push(message);
        if (message.kind === "steering") {
          process = { start: event.seq, entries: [] };
          timeline.push(process);
        }
        break;
      }
      case "restart":
      case "model_changed":
        pending.push(event);
        break;
      case "run_started":
        process = { start: event.seq, entries: [...pending, event] };
        pending = [];
        timeline.push(process);
        break;
      case "restore":
        process = undefined;
        timeline.push(event);
        break;
      case "input":
        timeline.push(event);
        break;
      default:
        if (process) process.entries.push(event);
        else timeline.push(event);
    }
  }
  timeline.push(...pending, ...messages.slice(nextMessage));
  return timeline.map((item) => {
    if ("id" in item) return <ChatMessage key={item.id} message={item} />;
    if ("seq" in item) {
      return <div className="project-event" key={item.seq}><EventRow event={item} /></div>;
    }
    const active = running && item === process;
    if (!active && !item.entries.length) return null;
    return (
      <details className="agent-process" key={`run-${item.start}`}>
        <summary>
          {active ? <LoaderCircle size={15} className="process-spinner" /> : <ListChecks size={15} />}
          <span>{active ? "正在执行" : "执行过程"}</span>
          <small>{item.entries.length} 条记录</small>
          <ChevronDown size={14} className="process-chevron" />
        </summary>
        <div className="process-entries">
          {item.entries.map((entry) => "seq" in entry
            ? <EventRow key={entry.seq} event={entry} />
            : <ChatMessage key={entry.id} message={entry} />)}
        </div>
      </details>
    );
  });
}
function QuestionCard({ question, disabled, onAnswer }: {
  question: NonNullable<Task["question"]>;
  disabled: boolean;
  onAnswer: (answer: string) => void;
}) {
  const [draft, setDraft] = useState("");
  return (
    <section className="decision-card question-card" aria-label="澄清问题">
      <div className="card-label"><MessageCircle size={16} /> 需要你补充</div>
      <Markdown text={question.text} />
      {!!question.options?.length && (
        <div className="options">
          {question.options.map((option, index) => (
            <button type="button" key={index} disabled={disabled} onClick={() => onAnswer(option)}>
              <span>{option}</span><ChevronRight size={15} />
            </button>
          ))}
        </div>
      )}
      <form className="question-reply" onSubmit={(e) => {
        e.preventDefault();
        if (!disabled && draft.trim()) onAnswer(draft.trim());
      }}>
        <textarea
          aria-label="自定义答案"
          placeholder="输入你的回答…"
          rows={2}
          disabled={disabled}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.ctrlKey || e.metaKey) && !e.nativeEvent.isComposing) {
              e.preventDefault();
              e.currentTarget.form?.requestSubmit();
            }
          }}
        />
        <button type="submit" className="primary" disabled={disabled || !draft.trim()}>
          发送回答 <ArrowUp size={15} />
        </button>
      </form>
    </section>
  );
}
function parseCSV(text: string) {
  const rows: string[][] = [];
  let row: string[] = [],
    cell = "",
    quoted = false;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (c === '"') {
      if (quoted && text[i + 1] === '"') {
        cell += '"';
        i++;
      } else quoted = !quoted;
    } else if (c === "," && !quoted) {
      row.push(cell);
      cell = "";
    } else if (c === "\n" && !quoted) {
      row.push(cell.replace(/\r$/, ""));
      rows.push(row);
      row = [];
      cell = "";
    } else cell += c;
  }
  if (cell || row.length) {
    row.push(cell);
    rows.push(row);
  }
  return rows;
}
function DataView({ text }: { text: string }) {
  const rows = parseCSV(text);
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            {rows[0]?.map((v, i) => (
              <th key={i}>{v}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.slice(1).map((r, i) => (
            <tr key={i}>
              {r.map((v, j) => (
                <td key={j}>{v}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
function Chart({ text }: { text: string }) {
  try {
    const d = JSON.parse(text);
    const pts = d.points as { label: string; value: number }[];
    if (
      !Array.isArray(pts) ||
      !pts.every((p) => typeof p.label === "string" && Number.isFinite(p.value))
    )
      return <pre>{text}</pre>;
    const min = Math.min(...pts.map((p) => p.value), 0);
    const max = Math.max(...pts.map((p) => p.value), 0);
    const span = max - min || 1;
    const y = (v: number) => 260 - ((v - min) / span) * 220;
    const step = 530 / Math.max(pts.length, 1);
    const x = (i: number) => 65 + step * (i + 0.5);
    return (
      <div className="chart">
        <h2>{d.title || "数据图表"}</h2>
        <svg
          viewBox="0 0 640 330"
          role="img"
          aria-label={`${d.title || "数据图表"}，${d.type === "line" ? "折线图" : "柱状图"}`}
        >
          {[min, min + span / 2, max].map((v, i) => (
            <g key={i}>
              <line x1="65" y1={y(v)} x2="595" y2={y(v)} stroke="#e5eadd" />
              <text
                x="57"
                y={y(v) + 4}
                textAnchor="end"
                fontSize="11"
                fill="#7e8c70"
              >
                {Number(v.toFixed(2))}
              </text>
            </g>
          ))}
          <line x1="65" y1={y(0)} x2="595" y2={y(0)} stroke="#8e9e7c" />
          {d.type === "line" && (
            <polyline
              points={pts.map((p, i) => `${x(i)},${y(p.value)}`).join(" ")}
              stroke="#789761"
              strokeWidth="2.5"
              fill="none"
            />
          )}
          {pts.map((p, i) => (
            <g key={i}>
              {d.type === "line" ? (
                <circle cx={x(i)} cy={y(p.value)} r="3" fill="#6e8d55">
                  <title>
                    {p.label}：{p.value}
                  </title>
                </circle>
              ) : (
                <rect
                  x={x(i) - step * 0.32}
                  y={Math.min(y(0), y(p.value))}
                  width={Math.max(step * 0.64, 1)}
                  height={Math.max(Math.abs(y(p.value) - y(0)), 1)}
                  rx="2"
                  fill={p.value < 0 ? "#b8876d" : "#91ab77"}
                >
                  <title>
                    {p.label}：{p.value}
                  </title>
                </rect>
              )}
              {i % Math.ceil(pts.length / 10 || 1) === 0 && (
                <text
                  x={x(i)}
                  y="288"
                  textAnchor="middle"
                  fontSize="10"
                  fill="#6d7b60"
                >
                  {p.label.slice(0, 10)}
                </text>
              )}
            </g>
          ))}
        </svg>
        <details>
          <summary>查看精确数据（{pts.length} 项）</summary>
          <table>
            <tbody>
              {pts.map((p, i) => (
                <tr key={i}>
                  <td>{p.label}</td>
                  <td>{p.value}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </details>
        <small>来源：{d.source}</small>
      </div>
    );
  } catch {
    return <pre>{text}</pre>;
  }
}

function JSONView({ text }: { text: string }) {
  try {
    const rows = JSON.parse(text);
    if (
      !Array.isArray(rows) ||
      !rows.every((r) => r && typeof r === "object" && !Array.isArray(r))
    )
      return <pre className="source">{text}</pre>;
    const headers = [...new Set(rows.flatMap((r) => Object.keys(r)))];
    return (
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              {headers.map((h) => (
                <th key={h}>{h}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((r, i) => (
              <tr key={i}>
                {headers.map((h) => (
                  <td key={h}>
                    {typeof r[h] === "object"
                      ? JSON.stringify(r[h])
                      : String(r[h] ?? "")}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    );
  } catch {
    return <pre className="source">{text}</pre>;
  }
}

export default function App() {
  const [boot, setBoot] = useState<Bootstrap>();
  const [projects, setProjects] = useState<{ id: string; title: string }[]>([]);
  const [project, setProject] = useState<Project>();
  const [text, setText] = useState("");
  const [mode, setMode] = useState("build");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [sidebar, setSidebar] = useState(false);
  const [mobileNav, setMobileNav] = useState(false);
  const [pane, setPane] = useState<"chat" | "work">("chat");
  const [tab, setTab] = useState("preview");
  const [version, setVersion] = useState("");
  const [path, setPath] = useState("");
  const [content, setContent] = useState("");
  const [oldContent, setOldContent] = useState("");
  const [mobile, setMobile] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const [inputOpen, setInputOpen] = useState(false);
  const [inputName, setInputName] = useState("sales.csv");
  const [inputText, setInputText] = useState("");
  const [rename, setRename] = useState<{ id: string; title: string }>();
  const [renameError, setRenameError] = useState("");
  const [confirm, setConfirm] = useState<{
    action: string;
    title: string;
    body: string;
  }>();
  const [streamState, setStreamState] = useState("");
  const end = useRef<HTMLDivElement>(null);
  const selection = useRef("");
  const retry = useRef<{ key: string; id: string } | undefined>(undefined);
  const task = project?.tasks.find((t) => t.id === project.current_task);
  const running = !!project?.active_run;
  const canSteer = running && task?.status === "running";
  const question = task?.status === "waiting_input" ? task.question : undefined;
  const selected = project?.versions?.find((v) => v.id === version);
  const manifest = selected?.manifest || project?.files || {};
  const priorIndex = version
    ? (project?.versions || []).findIndex((v) => v.id === version) - 1
    : (project?.versions?.length || 0) - 1;
  const previousManifest = project?.versions?.[priorIndex]?.manifest || {};
  const files = [
    ...new Set([
      ...Object.keys(manifest),
      ...(tab === "history" ? Object.keys(previousManifest) : []),
    ]),
  ].sort();
  const reports = selected?.reports || project?.reports || [];
  useEffect(() => {
    if (task) setMode(task.mode);
  }, [task?.id, task?.mode]);
  async function api(url: string, body?: unknown, method = "POST") {
    const r = await fetch(
      "/api" + url,
      body === undefined
        ? {}
        : {
            method,
            headers: {
              "Content-Type": "application/json",
              "X-CSRF-Token": boot?.csrf || "",
            },
            body: JSON.stringify(body),
          },
    );
    const data = await r.json();
    if (!r.ok) throw Error(data.error || `请求失败 (${r.status})`);
    return data;
  }
  async function list() {
    setProjects(await api("/projects"));
  }
  function applySnapshot(next: Project) {
    if (selection.current !== next.id) return;
    // UTC timestamps use RFC3339Nano with trailing fractional zeroes omitted.
    setProject((current) =>
      current?.id === next.id && current.updated.slice(0, -1) > next.updated.slice(0, -1)
        ? current
        : next,
    );
  }
  async function open(id: string) {
    setMobileNav(false);
    selection.current = id;
    setError("");
    setVersion("");
    setPath("");
    setPane("chat");
    try {
      const p = await api("/projects/" + id);
      if (selection.current === id) {
        applySnapshot(p);
        localStorage.setItem("atoms:last", id);
      }
    } catch (e) {
      setError(String((e as Error).message));
    }
  }
  async function reload(id: string) {
    const p = await api("/projects/" + id);
    applySnapshot(p);
  }
  useEffect(() => {
    fetch("/api/bootstrap")
      .then((r) => r.json())
      .then(async (b) => {
        setBoot(b);
        await list();
        const id = localStorage.getItem("atoms:last");
        if (id) await open(id);
      })
      .catch(() => setError("暂时无法连接服务，请刷新页面重试。"));
  }, []);
  useEffect(() => {
    if (!project?.id) return;
    const id = project.id;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const es = new EventSource("/api/projects/" + id + "/events");
    const update = () => {
      if (!timer)
        timer = setTimeout(() => {
          timer = undefined;
          reload(id).catch(() => {});
        }, 250);
    };
    es.onmessage = update;
    for (const kind of ["progress", "snapshot"])
      es.addEventListener(kind, update);
    es.onopen = () => {
      setStreamState("");
      update();
    };
    es.onerror = () => setStreamState("连接恢复中，后台任务仍会继续");
    return () => {
      es.close();
      if (timer) clearTimeout(timer);
    };
  }, [project?.id]);
  useEffect(() => {
    end.current?.scrollIntoView({ behavior: "smooth", block: "end" });
  }, [project?.messages?.length, task?.status]);
  useEffect(() => {
    if (!files.includes(path))
      setPath(files.find((p) => p.endsWith("index.html")) || files[0] || "");
  }, [JSON.stringify(files), version]);
  useEffect(() => {
    setContent("");
    if (!project || !path || !manifest[path]) return;
    let live = true;
    api(
      `/projects/${project.id}/files?path=${encodeURIComponent(path)}&version=${version}`,
    )
      .then((r) => {
        if (live) setContent(r.content);
      })
      .catch((e) => {
        if (live) setError(e.message);
      });
    return () => {
      live = false;
    };
  }, [project?.id, path, version, manifest[path]?.hash]);
  useEffect(() => {
    setOldContent("");
    if (!project || !path || tab !== "history") return;
    const versions = project.versions || [];
    const index = version
      ? versions.findIndex((v) => v.id === version)
      : versions.length;
    const previous = versions[index - 1];
    if (!previous?.manifest[path]) return;
    let live = true;
    api(
      `/projects/${project.id}/files?path=${encodeURIComponent(path)}&version=${previous.id}`,
    ).then((r) => {
      if (live) setOldContent(r.content);
    });
    return () => {
      live = false;
    };
  }, [tab, version, path, project?.id, project?.versions?.length]);
  async function action(
    actionName: string,
    extra: Record<string, string> = {},
    p = project,
  ) {
    if (!p || selection.current !== p.id) return;
    setBusy(true);
    setError("");
    const key = JSON.stringify([p.id, actionName, extra]);
    if (retry.current?.key !== key)
      retry.current = { key, id: crypto.randomUUID() };
    try {
      const result = await api("/projects/" + p.id + "/actions", {
        action: actionName,
        mode,
        request_id: retry.current.id,
        ...extra,
      });
      retry.current = undefined;
      if (selection.current === p.id) {
        applySnapshot(result);
        if (actionName === "submit" || actionName === "steer") {
          setText((draft) => (draft === extra.text ? "" : draft));
        }
        if (actionName !== "steer") setVersion("");
      }
      if (actionName !== "steer") await list();
    } catch (e) {
      if (selection.current === p.id) setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function submit() {
    if (!text.trim() || busy || !boot) return;
    if (running) {
      if (canSteer && project?.active_run) {
        await action("steer", { text, run_id: project.active_run });
      }
      return;
    }
    if (unfinished || question) return;
    let p = project;
    if (!p) {
      setBusy(true);
      try {
        p = await api("/projects", { title: text.trim().slice(0, 28) });
        selection.current = p!.id;
        setProject(p);
        localStorage.setItem("atoms:last", p!.id);
      } catch (e) {
        setError((e as Error).message);
        setBusy(false);
        return;
      }
    }
    await action("submit", { text }, p);
  }
  async function saveInput() {
    setBusy(true);
    setError("");
    try {
      let p = project;
      if (!p) {
        p = await api("/projects", { title: "数据分析项目" });
        selection.current = p!.id;
        setProject(p);
        localStorage.setItem("atoms:last", p!.id);
      }
      const result = await api("/projects/" + p!.id + "/inputs", {
        name: inputName,
        content: inputText,
      });
      setProject(result);
      setInputOpen(false);
      setInputText("");
      await list();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  function newProject() {
    setMobileNav(false);
    selection.current = "";
    setProject(undefined);
    setVersion("");
    setPath("");
    setText("");
    setError("");
    localStorage.removeItem("atoms:last");
    setPane("chat");
  }
  const unfinished =
    !!task &&
    !["completed", "ended", "waiting_plan", "waiting_input"].includes(
      task.status,
    );
  const composer = (
    <div className="composer">
      <textarea
        aria-label={canSteer ? "补充当前任务指令" : "描述需求"}
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
            e.preventDefault();
            submit();
          }
        }}
        placeholder={
          canSteer
            ? "补充或更正当前任务的指令…"
            : task?.status === "waiting_plan"
              ? "告诉 Mike，计划需要如何调整…"
              : project
                ? "继续描述你的想法…"
                : "描述你想做的产品、文档，或想解决的问题…"
        }
        disabled={running && !canSteer}
      />
      <div className="composer-tools">
        <button
          className="icon"
          title="添加 CSV / JSON 数据"
          aria-label="添加数据"
          onClick={() => setInputOpen(true)}
          disabled={running || (!!task && task.status !== "completed")}
        >
          <Plus size={20} />
        </button>
        <span className="composer-hint">Ctrl + Enter 发送</span>
        <div className="mode">
          <select
            aria-label="构建模式"
            value={mode}
            onChange={(e) => setMode(e.target.value)}
            disabled={running || task?.status === "waiting_plan"}
          >
            <option value="build">Build</option>
            <option value="plan">Plan</option>
          </select>
          <ChevronDown size={14} />
        </div>
        {running && (
          <button
            className="icon"
            title="停止任务"
            aria-label="停止任务"
            disabled={busy}
            onClick={() => action("stop")}
          >
            <Square size={13} fill="currentColor" />
          </button>
        )}
        <button
          className="send"
          title={running ? "发送指令" : "发送需求"}
          aria-label={running ? "发送指令" : "发送需求"}
          disabled={busy || !text.trim() || !boot || (running ? !canSteer : unfinished)}
          onClick={submit}
        >
          <ArrowUp size={20} />
        </button>
      </div>
    </div>
  );
  return (
    <div className={`app ${sidebar ? "" : "collapsed"} ${mobileNav ? "nav-open" : ""}`}>
      {mobileNav && <button className="nav-backdrop" aria-label="关闭项目列表" onClick={() => setMobileNav(false)} />}
      <aside className="sidebar">
        <a
          className="brand"
          href="#"
          onClick={(e) => {
            e.preventDefault();
            newProject();
          }}
        >
          <span className="brand-mark">
            a<span>✦</span>
          </span>
          <strong>
            atoms<span>lab</span>
          </strong>
        </a>
        <button className="new-project" onClick={newProject}>
          <Plus size={18} /> 新建项目
        </button>
        <div className="section-label">
          你的工作空间 <span>{projects.length}</span>
        </div>
        <nav>
          {projects.map((p) => (
            <button
              className={p.id === project?.id ? "project active" : "project"}
              key={p.id}
              onClick={() => open(p.id)}
            >
              <FolderOpen size={16} />
              <span>{p.title}</span>
              <ChevronRight size={14} />
            </button>
          ))}
        </nav>
        <div className="sidebar-bottom">
          <div className="availability">
            <i className={boot?.model_ready ? "green" : ""} />
            {boot?.model_ready ? "模型服务已配置" : "模型服务待配置"}
          </div>
          <div className="profile">
            <span>L</span>
            <div>
              我的工作区<small>保存在当前浏览器凭证下</small>
            </div>
          </div>
        </div>
      </aside>
      <main>
        <header className="topbar">
          <button className="icon mobile-nav-toggle" aria-label="打开项目列表" aria-expanded={mobileNav} onClick={() => setMobileNav(!mobileNav)}><PanelLeftOpen size={18} /></button>
          <button
            className="icon"
            aria-label="切换侧边栏"
            aria-expanded={sidebar}
            title={sidebar ? "收起项目列表" : "展开项目列表"}
            onClick={() => setSidebar(!sidebar)}
          >
            {sidebar ? (
              <PanelLeftClose size={18} />
            ) : (
              <PanelLeftOpen size={18} />
            )}
          </button>
          <span className="crumb">工作空间</span>
          <ChevronRight size={13} />
          <button
            className="project-title"
            disabled={!project || busy}
            onClick={() => {
              if (!project) return;
              setRenameError("");
              setRename({ id: project.id, title: project.title });
            }}
          >
            {project?.title || "新想法"}
          </button>
          <div className="top-right">
            <span className={"status " + (running ? "working" : "")}>
              {task ? statusText[task.status] || task.status : "创作工作台"}
            </span>
            <span className="avatar">L</span>
          </div>
        </header>
        {error && (
          <div className="error-banner" role="alert">
            <AlertCircle size={17} />
            <span>{error}</span>
            <button
              className="icon"
              aria-label="关闭错误"
              onClick={() => setError("")}
            >
              <X size={15} />
            </button>
          </div>
        )}
        {task?.last_handoff && !running && task.status === "failed" && (
          <details className="error-banner">
            <summary>{task.last_handoff.reason}</summary>
            <p>已保留 {Object.keys(task.last_handoff.artifacts || {}).length} 个文件；可继续核对已有结果并调整下一步。</p>
            {!!task.last_handoff.evidence?.length && <p>关联 {task.last_handoff.evidence.length} 条实际执行记录，可在任务过程查看。</p>}
            {task.last_handoff.todo?.map((item,index) => <p key={index}>{item}</p>)}
          </details>
        )}
        {!project ? (
          <div className="landing">
            <div className="eyebrow">
              <span /> 从一个想法开始
            </div>
            <div className="team">
              {roles.map((r, i) => (
                <div
                  key={r}
                  className={"agent-face face-" + i}
                  title={`${r} · ${roleNames[r]}`}
                >
                  <span>{["◕", "◡", "•", "⌘", "⌕"][i]}</span>
                </div>
              ))}
            </div>
            <h1>
              Ideas in.
              <br className="small-only" /> Products out<span>.</span>
            </h1>
            <p className="intro">和你的 AI 团队一起，把想法变成真正的作品。</p>
            {composer}
            <div className="mode-explain">
              <Sparkles size={14} />
              {mode === "build"
                ? "Build · 从需求出发，直接构建"
                : "Plan · 先一起完善计划，批准后开始构建"}
            </div>
            <div className="examples">
              {examples.map(([label, prompt], i) => (
                <button key={label} onClick={() => setText(prompt)}>
                  <span className={"example-icon e" + i}>
                    {i === 0 ? (
                      <Code2 size={19} />
                    ) : i === 1 ? (
                      <FileText size={19} />
                    ) : (
                      <Database size={19} />
                    )}
                  </span>
                  <b>{label}</b>
                  <span>
                    {
                      [
                        "构建你的下一个小工具",
                        "从想法到清晰的需求",
                        "让数据讲一个有据可查的故事",
                      ][i]
                    }
                  </span>
                  <ChevronRight size={15} />
                </button>
              ))}
            </div>
            <p className="landing-note">
              支持网页、文档、Node.js 脚本与数据分析。工作进度自动保存。
            </p>
          </div>
        ) : (
          <>
            <div className="mobile-switch">
              <button
              className={pane === "chat" ? "active" : ""}
              aria-pressed={pane === "chat"}
                onClick={() => setPane("chat")}
              >
                对话与任务
              </button>
              <button
              className={pane === "work" ? "active" : ""}
              aria-pressed={pane === "work"}
                onClick={() => setPane("work")}
              >
                产物工作区 <span>{files.length}</span>
              </button>
            </div>
            <div className={"workspace show-" + pane}>
              <section className="conversation">
                <div className="conversation-heading">
                  <span>
                    <span className="mini-atom">✦</span> 你的 AI 团队
                  </span>
                  <div className="mini-team">
                    {roles.map((r, i) => (
                      <span
                        title={`${r} · ${roleNames[r]}`}
                        key={r}
                        className={"mini-role face-" + i}
                      >
                        {r[0]}
                      </span>
                    ))}
                  </div>
                </div>
                <div className="messages">
                  {!(project.messages || []).length && (
                    <div className="empty-chat">
                      <Sparkles size={30} />
                      <h3>准备好开始了</h3>
                      <p>描述需求，或者先添加用于分析的 CSV / JSON 数据。</p>
                    </div>
                  )}
                  <ChatHistory key={project.id} messages={project.messages || []} events={project.events || []} running={running} />
                  {question && (
                    <QuestionCard
                      key={project.id + ":" + question.id}
                      question={question}
                      disabled={busy || running || !boot}
                      onAnswer={(answer) => action("answer", { text: answer, question_id: question.id })}
                    />
                  )}
                  {task?.status === "waiting_plan" && task.plan && (
                    <div className="decision-card">
                      <div className="card-label">PLAN · 等待批准</div>
                      <Markdown text={task.plan.text} />
                      <h4>验收标准</h4>
                      <ul>
                        {task.plan.acceptance.map((a, i) => (
                          <li key={i}>{a}</li>
                        ))}
                      </ul>
                      <button
                        className="primary"
                        disabled={busy}
                        onClick={() =>
                          action("approve", { revision: task.plan!.revision })
                        }
                      >
                        <Check size={16} /> 批准计划，开始构建
                      </button>
                      <button
                        className="text-button"
                        disabled={busy}
                        onClick={() =>
                          setConfirm({
                            action: "end",
                            title: "取消当前计划？",
                            body: "已保存的对话和计划文档仍会保留。你可以提交新的需求。",
                          })
                        }
                      >
                        取消计划
                      </button>
                      <small>也可以在下方输入修改意见。</small>
                    </div>
                  )}
                  {unfinished && !running && (
                    <div className="recovery">
                      <b>{statusText[task!.status]}</b>
                      {project.runs.filter((r) => r.error).at(-1)?.error && (
                        <p className="failure-detail">
                          {project.runs.filter((r) => r.error).at(-1)?.error}
                        </p>
                      )}
                      <p>
                        已保存 {task!.checkpoints}{" "}
                        个检查点。继续会使用当前产物；从头重做会回到本任务起点，历史记录保留。
                      </p>
                      <div>
                        <button
                          className="primary"
                          disabled={busy}
                          onClick={() => action("continue")}
                        >
                          <Play size={14} /> 继续任务
                        </button>
                        <button
                          disabled={busy}
                          onClick={() =>
                            setConfirm({
                              action: "restart",
                              title: "从任务起点重新执行？",
                              body: "当前工作文件将回到本任务开始时的状态。旧运行和中间检查点会保留；新的执行会产生模型调用。",
                            })
                          }
                        >
                          从头重做
                        </button>
                        <button
                          disabled={busy}
                          onClick={() =>
                            setConfirm({
                              action: "end",
                              title: "结束当前任务？",
                              body: "已保存的文件保留，之后可以基于它们提交新需求。",
                            })
                          }
                        >
                          结束任务
                        </button>
                      </div>
                    </div>
                  )}
                  {project.runs?.length > 0 && (
                    <div className="usage">
                      累计 {project.runs.reduce((n, r) => n + r.calls, 0)} 次模型调用
                      · 已报告{" "}
                      {project.runs
                        .reduce((n, r) => n + r.usage.total, 0)
                        .toLocaleString()}{" "}
                      tokens
                      {project.runs.some((r) => !r.usage.known) &&
                        "（部分用量未知）"}
                    </div>
                  )}
                  <div ref={end} />
                </div>
                <div className={`chat-input${question && !running ? " question-pending" : ""}`}>
                  {streamState && <p className="connection">{streamState}</p>}
                  {(!question || running) && composer}
                  <div className="input-foot">
                    {running
                      ? "后台执行中，关闭网页不会停止任务"
                      : "生成内容与执行记录自动保存"}
                    <span>{task?.mode === "plan" ? "Plan" : "Build"}</span>
                  </div>
                </div>
              </section>
              <section className="workarea">
                <div className="work-toolbar">
                  <div className="tabs">
                    {[
                      ["preview", "预览", Eye],
                      ["files", "文件", Code2],
                      ["history", "版本", History],
                    ].map(([key, label, Icon]) => {
                      const I = Icon as typeof Eye;
                      return (
                        <button
                          key={key as string}
                          className={tab === key ? "active" : ""}
                          aria-pressed={tab === key}
                          onClick={() => setTab(key as string)}
                        >
                          <I size={16} />
                          {label as string}
                        </button>
                      );
                    })}
                  </div>
                  <div className="work-actions">
                    <select
                      aria-label="选择版本"
                      value={version}
                      onChange={(e) => setVersion(e.target.value)}
                    >
                      <option value="">当前草稿</option>
                      {(project.versions || [])
                        .slice()
                        .reverse()
                        .map((v) => (
                          <option key={v.id} value={v.id}>
                            v{v.number} · 已交付
                          </option>
                        ))}
                    </select>
                    <a
                      className="icon"
                      aria-label="下载所选产物"
                      title="下载所选产物 ZIP"
                      href={`/api/projects/${project.id}/export?version=${version}`}
                    >
                      <Download size={17} />
                    </a>
                  </div>
                </div>
                <div className="artifact-meta">
                  <span>
                    {selected
                      ? `v${selected.number} · ${selected.summary}`
                      : project.versions?.some(
                            (v) =>
                              JSON.stringify(v.manifest) ===
                              JSON.stringify(project.files),
                          )
                        ? "当前文件与已交付版本一致"
                        : "当前草稿 · 通过检查后形成交付版本"}
                  </span>
                  {tab === "preview" && path.endsWith(".html") && (
                    <div>
                      <button
                        className={"icon " + (!mobile ? "selected" : "")}
                        aria-label="桌面预览"
                        onClick={() => setMobile(false)}
                      >
                        <Monitor size={15} />
                      </button>
                      <button
                        className={"icon " + (mobile ? "selected" : "")}
                        aria-label="手机预览"
                        onClick={() => setMobile(true)}
                      >
                        <Smartphone size={15} />
                      </button>
                      <button
                        className="icon"
                        aria-label="刷新预览"
                        onClick={() => setRefresh(refresh + 1)}
                      >
                        <RotateCw size={15} />
                      </button>
                    </div>
                  )}
                </div>
                {!files.length ? (
                  <div className="empty-work">
                    <div className="empty-illustration"><Eye size={24} /></div>
                    <h2>暂无产物</h2>
                    <p>任务生成文件后，可在这里预览和下载。</p>
                  </div>
                ) : (
                  <div className="artifact-body">
                    <div className="file-strip">
                      <select
                        aria-label="选择文件"
                        value={path}
                        onChange={(e) => setPath(e.target.value)}
                      >
                        {files.map((f) => (
                          <option key={f} value={f}>
                            {manifest[f]?.read_only ? "🔒 " : ""}
                            {f}
                          </option>
                        ))}
                      </select>
                      <span>{manifest[path] ? `${manifest[path].size.toLocaleString()} B` : "已删除"}</span>
                      {manifest[path] && <a
                        href={`/api/projects/${project.id}/files?path=${encodeURIComponent(path)}&version=${version}&download=1`}
                        title="下载该文件"
                        aria-label="下载该文件"
                      >
                        <Download size={14} />
                      </a>}
                    </div>
                    {tab === "history" ? (
                      <div className="history-view">
                        <div className="version-info">
                          <h3>
                            {selected ? `v${selected.number}` : "当前草稿"} 与
                            {selected?.number === 1 ? "空项目" : "前一交付版本"}
                            的文件差异
                          </h3>
                          {selected && (
                            <button
                              disabled={running || busy}
                              onClick={() =>
                                setConfirm({
                                  action: "restore",
                                  title: `恢复到 v${selected.number}？`,
                                  body: "当前工作文件会切换到此版本；现有草稿与较新版本的历史记录保留。",
                                })
                              }
                            >
                              <History size={14} /> 恢复此版本
                            </button>
                          )}
                        </div>
                        <div className="file-changes">
                          {files.map((f) => (
                            <button
                              key={f}
                              className={f === path ? "active" : ""}
                              onClick={() => setPath(f)}
                            >
                              {f}
                            </button>
                          ))}
                        </div>
                        <pre className="diff">
                          {diffLines(oldContent, content).map((d, i) => (
                            <span
                              key={i}
                              className={
                                d.added ? "added" : d.removed ? "removed" : ""
                              }
                            >
                              {d.value}
                            </span>
                          ))}
                        </pre>
                      </div>
                    ) : tab === "files" ? (
                      <pre className="source">
                        <code>{content}</code>
                      </pre>
                    ) : path.endsWith(".html") && !(selected ? selected.preview_ready : project.preview_ready) ? (
                      <div className="empty-work"><h2>文件已保存，等待运行检查</h2><p>构建完成后将在这里显示交互预览。<br />现在可以切换到“文件”查看源码。</p></div>
                    ) : path.endsWith(".html") ? (
                      <div
                        className={"preview-wrap " + (mobile ? "mobile" : "")}
                      >
                        <iframe
                          title="生成产物预览"
                          sandbox="allow-scripts allow-forms"
                          key={`${version}-${refresh}-${files.map(name => name + manifest[name]?.hash).join("|")}`}
                          src={`/api/projects/${project.id}/preview?version=${version}`}
                        />
                      </div>
                    ) : path.endsWith(".md") ? (
                      <div className="document">
                        <Markdown text={content} />
                      </div>
                    ) : path.endsWith(".csv") ? (
                      <DataView text={content} />
                    ) : path.endsWith(".chart.json") ? (
                      <Chart text={content} />
                    ) : path.endsWith(".json") ? (
                      <JSONView text={content} />
                    ) : (
                      <pre className="source">
                        <code>{content}</code>
                      </pre>
                    )}
                  </div>
                )}
                {!!reports.length && (
                  <details className="reports">
                    <summary>
                      <span>
                        <Check size={15} /> 执行与检查报告
                      </span>
                      <b>
                        {reports.at(-1)?.status === "passed"
                          ? "最近检查通过"
                          : "最近检查失败"}
                      </b>
                    </summary>
                    {reports
                      .slice(-8)
                      .reverse()
                      .map((r) => (
                        <article key={r.id}>
                          <strong>
                            {(
                              {
                                static: "文件检查",
                                run: "实际运行",
                                browser: "浏览器测试",
                                analysis: "数据计算",
                              } as Record<string, string>
                            )[r.kind] || r.kind}{" "}
                            <span className={r.status}>
                              {r.status === "passed" ? "通过" : "失败"}
                            </span>
                          </strong>
                          {r.checks?.map((c, i) => (
                            <p key={i}>
                              <b>{c.name}</b> · {c.detail}
                            </p>
                          ))}
                        </article>
                      ))}
                  </details>
                )}
              </section>
            </div>
          </>
        )}
      </main>
      {rename && (
        <div className="modal-backdrop">
          <section className="modal compact" role="dialog" aria-modal="true" aria-label="重命名项目">
            <h2>重命名项目</h2>
            <form onSubmit={async (event) => {
              event.preventDefault();
              if (busy || !rename.title.trim()) return;
              setBusy(true);
              setRenameError("");
              try {
                const updated = await api("/projects/" + rename.id, { title: rename.title.trim() }, "PATCH");
                if (selection.current === rename.id) setProject(updated);
                await list();
                setRename(undefined);
              } catch (e) {
                setRenameError((e as Error).message);
              } finally {
                setBusy(false);
              }
            }}>
              <label>项目名称<input autoFocus value={rename.title} onChange={(e) => setRename({ ...rename, title: e.target.value })} /></label>
              {renameError && <p className="modal-error" role="alert">{renameError}</p>}
              <div className="modal-actions">
                <button type="button" disabled={busy} onClick={() => setRename(undefined)}>取消</button>
                <button className="primary" type="submit" disabled={busy || !rename.title.trim()}>保存名称</button>
              </div>
            </form>
          </section>
        </div>
      )}
      {inputOpen && (
        <div className="modal-backdrop">
          <section
            className="modal"
            role="dialog"
            aria-modal="true"
            aria-label="添加分析数据"
          >
            <button
              className="close icon"
              onClick={() => setInputOpen(false)}
              aria-label="关闭"
            >
              <X size={20} />
            </button>
            <Database size={27} />
            <h2>添加分析数据</h2>
            {error && <p className="modal-error" role="alert">{error}</p>}
            <p>
              粘贴 CSV 或由对象数组组成的 JSON，保存为只读输入。最多 2,000
              行、32 列、256 KiB。
            </p>
            <label>
              文件名
              <input
                value={inputName}
                onChange={(e) => setInputName(e.target.value)}
              />
            </label>
            <label>
              数据内容
              <textarea
                value={inputText}
                onChange={(e) => setInputText(e.target.value)}
                placeholder={"category,amount\n咖啡,28\n茶饮,18"}
              />
            </label>
            <button
              className="primary"
              disabled={busy || !inputText.trim()}
              onClick={saveInput}
            >
              保存数据
            </button>
          </section>
        </div>
      )}
      {confirm && (
        <div className="modal-backdrop">
          <section
            className="modal compact"
            role="dialog"
            aria-modal="true"
            aria-label={confirm.title}
          >
            <h2>{confirm.title}</h2>
            <p>{confirm.body}</p>
            <div className="modal-actions">
              <button onClick={() => setConfirm(undefined)}>取消</button>
              <button
                className="primary"
                onClick={() => {
                  action(
                    confirm.action,
                    confirm.action === "restore" ? { version } : {},
                  );
                  setConfirm(undefined);
                }}
              >
                确认
              </button>
            </div>
          </section>
        </div>
      )}
    </div>
  );
}
