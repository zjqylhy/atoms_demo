// Only this private service may access Docker. Neither the app nor generated code
// receives a Docker socket, host path mount, or model credential.
const http = require("node:http");
const fs = require("node:fs/promises");
const path = require("node:path");
const crypto = require("node:crypto");
const { spawn, execFile } = require("node:child_process");
const { promisify } = require("node:util");
const exec = promisify(execFile);
const token = process.env.ATOMS_RUNNER_TOKEN;
const host = process.env.ATOMS_RUNNER_HOST || "127.0.0.1";
const port = Number(process.env.ATOMS_RUNNER_PORT || 8091);
const runtime = process.env.ATOMS_SANDBOX_RUNTIME || "runsc";
const localDev = process.env.ATOMS_RUNNER_LOCAL_DEV === "1";
const image = process.env.ATOMS_RUNNER_IMAGE || "atoms-runner:local";
const data = path.resolve(process.env.ATOMS_RUNNER_DATA || "data/runner");
const memory = process.env.ATOMS_SANDBOX_MEMORY || "768m";
const cpus = process.env.ATOMS_SANDBOX_CPUS || "1";
let active;
let closing = false;
if (!token || token.length < 24)
  throw Error("ATOMS_RUNNER_TOKEN 必须至少 24 字符");
if (
  runtime !== "runsc" &&
  !(localDev && host === "127.0.0.1" && runtime === "runc")
)
  throw Error("公开部署必须使用 runsc；runc 仅允许明确开启的本机开发");
const hash = (x) => crypto.createHash("sha256").update(x).digest("hex");
const send = (r, status, v) => {
  if (r.destroyed) return;
  r.writeHead(status, {
    "Content-Type": "application/json",
    "Cache-Control": "no-store",
  });
  r.end(JSON.stringify(v));
};
async function save(id, value) {
  const target = path.join(data, id + ".json");
  const tmp = target + "." + crypto.randomUUID() + ".tmp";
  const f = await fs.open(tmp, "wx", 0o600);
  try {
    await f.writeFile(JSON.stringify(value));
    await f.sync();
  } finally {
    await f.close();
  }
  await fs.rename(tmp, target);
}
function validate(j) {
  if (
    !j ||
    !/^[a-f0-9]{64}$/.test(j.id) ||
    !/^[a-f0-9]{64}$/.test(j.digest) ||
    !["run_project", "test_preview"].includes(j.action) ||
    !j.parameters ||
    typeof j.parameters !== "object" ||
    !j.files ||
    typeof j.files !== "object" ||
    Array.isArray(j.files)
  )
    throw Error("执行请求格式无效");
  const entries = Object.entries(j.files);
  if (entries.length > 100) throw Error("文件数量超限");
  let size = 0;
  for (const [n, v] of entries) {
    if (
      typeof v !== "string" ||
      Buffer.byteLength(v) > 262144 ||
      n.length > 180 ||
      n.startsWith("/") ||
      /[\\:\x00-\x1f]/.test(n) ||
      n.split("/").some((p) => !p || p.startsWith(".") || p === "node_modules")
    )
      throw Error("无效文件");
    size += Buffer.byteLength(v);
  }
  if (size > 2097152) throw Error("项目大小超限");
}
async function remove(name) {
  try {
    await exec("docker", ["rm", "-f", name], {
      timeout: 10000,
      maxBuffer: 2000,
      windowsHide: true,
    });
  } catch {}
}
async function run(job, name) {
  return await new Promise((resolve) => {
    const args = [
      "run",
      "--name",
      name,
      "--label",
      "atoms.executor=1",
      "--runtime",
      runtime,
      "--network",
      "none",
      "--cap-drop",
      "ALL",
      "--cap-add",
      "SETUID",
      "--cap-add",
      "SETGID",
      "--cap-add",
      "KILL",
      "--security-opt",
      "no-new-privileges",
      "--read-only",
      "--pids-limit",
      "192",
      "--memory",
      memory,
      "--memory-swap",
      memory,
      "--cpus",
      cpus,
      "--tmpfs",
      "/workspace:rw,nosuid,nodev,size=96m,mode=0755",
      "--tmpfs",
      job.action === "run_project" && job.parameters.kind === "go"
        ? "/tmp:rw,exec,nosuid,nodev,size=384m,mode=1777"
        : "/tmp:rw,nosuid,nodev,size=128m,mode=1777",
      "--shm-size",
      "64m",
      "--init",
      "-i",
      image,
    ];
    const child = spawn("docker", args, {
      stdio: ["pipe", "pipe", "pipe"],
      windowsHide: true,
    });
    child.stdout.setEncoding("utf8");
    child.stderr.setEncoding("utf8");
    active = { name, child };
    let out = "",
      err = "",
      timedOut = false;
    const cancel = () => {
      timedOut = true;
      remove(name).then(() => child.kill());
    };
    active.cancel = cancel;
    const timer = setTimeout(cancel, 120000);
    child.stdout.on("data", (b) => {
      out += b;
      if (Buffer.byteLength(out) > 2097152) cancel();
    });
    child.stderr.on("data", (b) => {
      err = (err + b).slice(-10000);
    });
    child.on("error", (e) => {
      err = e.message;
    });
    child.stdin.on("error", () => {});
    child.stdin.end(JSON.stringify(job));
    child.on("close", async (code) => {
      clearTimeout(timer);
      let result;
      try {
        result = JSON.parse(out);
        if (
          code !== 0 ||
          timedOut ||
          result.id !== job.id ||
          result.digest !== job.digest ||
          !["passed", "failed"].includes(result.status) ||
          !Array.isArray(result.checks) ||
          !result.checks.length
        )
          throw Error("报告无效");
      } catch {
        result = {
          id: job.id,
          digest: job.digest,
          status: "failed",
          checks: [
            {
              name: "container",
              status: "failed",
              detail: timedOut
                ? "执行超过限制或被停止"
                : `容器未正常返回报告 (${code})：${err.slice(-3000)}`,
            },
          ],
          details: {},
        };
      }
      result.details = {
        ...result.details,
        isolation: runtime,
        memory_limit: memory,
        cpu_limit: cpus,
      };
      await remove(name);
      resolve(result);
    });
  });
}
async function main() {
  await fs.mkdir(data, { recursive: true });
  const { stdout } = await exec(
    "docker",
    ["info", "--format", "{{json .Runtimes}}"],
    { timeout: 15000, windowsHide: true },
  );
  if (!Object.hasOwn(JSON.parse(stdout), runtime))
    throw Error("所需隔离运行时不可用：" + runtime);
  await exec("docker", ["image", "inspect", image], {
    timeout: 10000,
    maxBuffer: 1000000,
    windowsHide: true,
  });
  // A service restart never silently repeats an in-flight job. Its container is
  // removed, and the original job ID returns an interrupted report.
  for (const name of await fs.readdir(data)) {
    if (!/^[a-f0-9]{64}\.json$/.test(name)) continue;
    const record = JSON.parse(await fs.readFile(path.join(data, name), "utf8"));
    if (record.state === "running") {
      if (/^atoms-job-[a-f0-9]{16}-[a-f0-9]{8}$/.test(record.container))
        await remove(record.container);
      record.state = "complete";
      record.result = {
        id: record.id,
        digest: record.digest,
        status: "failed",
        checks: [
          {
            name: "interrupted",
            status: "failed",
            detail: "执行服务中断，结果未确认；保留任务记录，没有自动重复执行",
          },
        ],
        details: {},
      };
      await save(record.id, record);
    }
  }
  const server = http.createServer(async (req, res) => {
    let ownedName;
    try {
      if (req.url === "/healthz" && req.method === "GET") {
        send(res, 200, { ok: true, busy: !!active, isolation: runtime });
        return;
      }
      const auth = Buffer.from(req.headers.authorization || "");
      const expected = Buffer.from("Bearer " + token);
      if (
        auth.length !== expected.length ||
        !crypto.timingSafeEqual(auth, expected)
      ) {
        send(res, 401, { error: "unauthorized" });
        return;
      }
      if (req.method !== "POST" || req.url !== "/jobs") {
        send(res, 404, { error: "not found" });
        return;
      }
      let body = "";
      req.setEncoding("utf8");
      for await (const c of req) {
        body += c;
        if (Buffer.byteLength(body) > 3 * 1024 * 1024) {
          send(res, 413, { error: "请求过大" });
          return;
        }
      }
      let job;
      try {
        job = JSON.parse(body);
        validate(job);
      } catch (e) {
        send(res, 400, { error: e.message });
        return;
      }
      const fingerprint = hash(JSON.stringify(job));
      let existing;
      try {
        existing = JSON.parse(
          await fs.readFile(path.join(data, job.id + ".json"), "utf8"),
        );
      } catch (e) {
        if (e.code !== "ENOENT") throw e;
      }
      if (existing) {
        if (existing.fingerprint !== fingerprint) {
          send(res, 409, { error: "相同任务标识的内容发生变化" });
          return;
        }
        if (existing.state === "complete") {
          send(res, 200, existing.result);
          return;
        }
        send(res, 409, { error: "该任务尚在运行，尚未确认结果" });
        return;
      }
      if (active || closing) {
        send(res, 429, { error: "执行节点忙，请稍后继续" });
        return;
      }
      const name =
        "atoms-job-" +
        job.id.slice(0, 16) +
        "-" +
        crypto.randomBytes(4).toString("hex");
      active = { name };
      ownedName = name;
      const record = {
        id: job.id,
        digest: job.digest,
        fingerprint,
        state: "running",
        container: name,
      };
      await save(job.id, record);
      const resultPromise = run(job, name);
      const onClose = () => {
        if (!res.writableEnded && active?.name === name) active?.cancel?.();
      };
      res.once("close", onClose);
      const result = await resultPromise;
      res.removeListener("close", onClose);
      record.state = "complete";
      record.result = result;
      await save(job.id, record);
      active = undefined;
      send(res, 200, result);
    } catch (e) {
      // A failed unrelated request must not release another job's admission slot.
      if (ownedName && active?.name === ownedName) {
        active.cancel?.();
        await remove(ownedName);
        active = undefined;
      }
      send(res, 500, { error: "执行服务内部错误" });
      console.error(e.message);
    }
  });
  server.requestTimeout = 150000;
  server.listen(port, host, () =>
    console.log(
      `Atoms runner http://${host}:${port} (${runtime}${localDev ? ", local development" : ""})`,
    ),
  );
  for (const sig of ["SIGINT", "SIGTERM"])
    process.on(sig, () => {
      closing = true;
      active?.cancel?.();
      server.close();
      setTimeout(() => process.exit(0), 12000).unref();
    });
}
main().catch((e) => {
  console.error(e.message);
  process.exitCode = 1;
});
