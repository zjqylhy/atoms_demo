// Trusted harness: generated scripts run as a separate UID, without credentials.
// Production isolation is provided by the executor's required gVisor runtime.
const { declarations, collectOutputs } = require("./outputs.cjs");
const { safeName } = require("./project-path.cjs");
const { buildHTML } = require("./web.cjs");
const fs = require("node:fs/promises");
const path = require("node:path");
const { spawn } = require("node:child_process");
const esbuild = require("esbuild");
const { checkBrowser } = require("./browser.cjs");
const ROOT = "/workspace/project";
const SCRATCH = "/workspace/scratch";
const checks = [];
const details = {};
let job;
let artifacts = [];
function check(name, detail, status = "passed", evidence = {}) {
  checks.push({
    name,
    status,
    detail: String(detail).slice(0, 4000),
    ...evidence,
  });
}
async function loadFiles() {
  const textFiles = Object.entries(job.files || {}).map(([name, content]) => {
    if (typeof content !== "string") throw Error("文本文件类型无效");
    return [name, Buffer.from(content, "utf8")];
  });
  const binaryFiles = Object.entries(job.binary_files || {}).map(
    ([name, content]) => {
      if (
        typeof content !== "string" ||
        Buffer.from(content, "base64").toString("base64") !== content ||
        Object.hasOwn(job.files || {}, name)
      )
        throw Error("二进制文件编码或路径无效");
      return [name, Buffer.from(content, "base64")];
    },
  );
  const entries = [...textFiles, ...binaryFiles];
  if (entries.length > 100) throw Error("文件数量超限");
  let total = 0;
  await fs.mkdir(ROOT, { recursive: true });
  for (const [name, content] of entries) {
    safeName(name);
    if (content.length > 262144) throw Error("文件格式或大小无效");
    total += Buffer.byteLength(content);
    if (total > 2097152) throw Error("项目大小超限");
    const target = path.join(ROOT, name);
    await fs.mkdir(path.dirname(target), { recursive: true });
    await fs.writeFile(target, content, { mode: 0o444 });
  }
}
async function prepareScratch() {
  await fs.mkdir(SCRATCH, { mode: 0o777 });
  await fs.chmod(SCRATCH, 0o777);
}

function runChild(command, args, { cwd = SCRATCH, env, seconds }) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd,
      uid: 1001,
      gid: 1001,
      detached: true,
      env: {
        PATH: "/usr/local/bin:/usr/bin:/bin",
        HOME: SCRATCH,
        LANG: "C.UTF-8",
        ...env,
      },
      stdio: ["ignore", "pipe", "pipe"],
    });
    child.stdout.setEncoding("utf8");
    child.stderr.setEncoding("utf8");
    let out = "",
      err = "",
      limit = false;
    const kill = () => {
      try {
        process.kill(-child.pid, "SIGKILL");
      } catch {}
    };
    const timer = setTimeout(() => {
      limit = true;
      kill();
    }, seconds * 1000);
    const collect = (which, b) => {
      if (which === "out") out += b;
      else err += b;
      if (Buffer.byteLength(out) + Buffer.byteLength(err) > 32768) {
        limit = true;
        kill();
      }
    };
    child.stdout.on("data", (b) => collect("out", b));
    child.stderr.on("data", (b) => collect("err", b));
    child.on("error", (e) => {
      clearTimeout(timer);
      reject(e);
    });
    child.on("close", (code, signal) => {
      clearTimeout(timer);
      kill();
      resolve({
        code,
        signal,
        out: out.slice(0, 32768),
        err: err.slice(0, 32768),
        limit,
      });
    });
  });
}

function checkExit(name, result, seconds, includeStdout = false) {
  const passed = !result.limit && result.code === 0;
  const output = [includeStdout ? result.out : "", result.err]
    .filter(Boolean)
    .join("\n")
    .trim();
  check(
    name,
    result.limit
      ? `超过 ${seconds} 秒或 32 KiB 输出限制`
      : `退出码 ${result.code ?? result.signal}${output ? "；" + output : ""}`,
    passed ? "passed" : "failed",
  );
  return passed;
}

async function script() {
  details.input_root = ROOT;
  details.working_directory = SCRATCH;
  details.output_location =
    "Write declared output paths relative to the working directory; project input is read-only.";
  const p = job.parameters;
  safeName(p.entry);
  const ext = path.extname(p.entry);
  if (
    !/^\.(?:[mc]?js|py|[mc]?ts|tsx)$/.test(ext) ||
    !Object.hasOwn(job.files, p.entry)
  )
    throw Error("脚本入口必须是项目内的 JavaScript、TypeScript 或 Python 文件");
  if (typeof p.expected_stdout !== "string")
    throw Error("必须提供脚本预期标准输出，不能只凭退出成功验收");
  if (
    p.args !== undefined &&
    (!Array.isArray(p.args) ||
      p.args.length > 20 ||
      p.args.some((x) => typeof x !== "string" || x.length > 1000))
  )
    throw Error("脚本参数无效");

  declarations(p.outputs);
  await prepareScratch();
  let entry = path.join(ROOT, p.entry);
  let command = process.execPath;
  let options = [];
  if (ext === ".py") {
    command = "/usr/bin/python3";
    options = ["-B", "-s"];
  } else if (/\.(?:[mc]?ts|tsx)$/.test(ext)) {
    const commonJS = ext === ".cts";
    const bundle = await esbuild.build({
      entryPoints: [entry],
      absWorkingDir: ROOT,
      bundle: true,
      write: false,
      platform: "node",
      format: commonJS ? "cjs" : "esm",
      target: "node24",
      nodePaths: ["/opt/atoms/node_modules"],
      logLevel: "silent",
    });
    if (bundle.outputFiles.length !== 1)
      throw Error("TypeScript 脚本必须能打包成单个入口");
    // Keeping the bundle beside the entry preserves relative data-file paths.
    entry = path.join(
      path.dirname(entry),
      commonJS ? ".atoms-entry.cjs" : ".atoms-entry.mjs",
    );
    await fs.writeFile(entry, bundle.outputFiles[0].contents, { mode: 0o444 });
    check("typescript_transpile", "esbuild 转译成功");
  }
  const result = await runChild(
    command,
    [...options, entry, ...(p.args || [])],
    {
      seconds: 15,
      env: { NODE_PATH: "/opt/atoms/node_modules" },
    },
  );
  details.stdout = result.out;
  details.stderr = result.err;
  details.exit_code = result.code;
  checkExit("script_exit", result, 15);
  check(
    "expected_stdout",
    `预期 ${JSON.stringify(p.expected_stdout.trim())}；实际 ${JSON.stringify(result.out.trim())}`,
    result.out.trim() === p.expected_stdout.trim() ? "passed" : "failed",
  );
  if (checks.every((c) => c.status === "passed")) {
    artifacts = await collectOutputs(SCRATCH, p.outputs || []);
    if (artifacts.length)
      check(
        "artifact_collection",
        `已回收 ${artifacts.length} 个声明文件并计算字节哈希`,
      );
  }
}

async function goProject() {
  if (job.parameters.entry !== "go.mod" || !Object.hasOwn(job.files, "go.mod"))
    throw Error("Go 项目需要根目录下的 go.mod");
  await prepareScratch();
  const go = "/usr/local/go/bin/go";
  const mod = Object.hasOwn(job.files, "vendor/modules.txt")
    ? "vendor"
    : "readonly";
  const env = {
    CGO_ENABLED: "0",
    GOMAXPROCS: "1",
    GOCACHE: "/tmp/atoms-go-cache",
    GOPATH: "/tmp/atoms-gopath",
    GOFLAGS: `-mod=${mod} -buildvcs=false -p=1`,
    GOENV: "off",
    GOPROXY: "off",
    GOSUMDB: "off",
    GOTOOLCHAIN: "local",
    GOWORK: "off",
  };
  // Seed a private cache from the image; build and test share it without cross-job writes.
  const cache = await runChild(
    "/bin/cp",
    ["-R", "/opt/atoms/go-cache", env.GOCACHE],
    {
      seconds: 10,
    },
  );
  if (cache.limit || cache.code !== 0)
    throw Error("Go 编译缓存初始化失败：" + cache.err);
  const build = await runChild(go, ["build", "-o", SCRATCH + "/", "./..."], {
    cwd: ROOT,
    env,
    seconds: 65,
  });
  details.go_build = {
    stdout: build.out,
    stderr: build.err,
    exit_code: build.code,
  };
  if (!checkExit("go_build", build, 65)) return;
  const test = await runChild(
    go,
    ["test", "-count=1", "-timeout=25s", "./..."],
    {
      cwd: ROOT,
      env,
      seconds: 45,
    },
  );
  details.go_test = {
    stdout: test.out,
    stderr: test.err,
    exit_code: test.code,
  };
  checkExit("go_test", test, 45, true);
}
(async () => {
  let data = "";
  process.stdin.setEncoding("utf8");
  for await (const chunk of process.stdin) {
    data += chunk;
    if (Buffer.byteLength(data) > 3 * 1024 * 1024) throw Error("请求超过限制");
  }
  job = JSON.parse(data);
  await loadFiles();
  const start = Date.now();
  if (job.action === "run_project" && job.parameters.kind === "go")
    await goProject();
  else if (job.action === "run_project" && job.parameters.kind === "script")
    await script();
  else if (
    (job.action === "run_project" && job.parameters.kind === "web") ||
    job.action === "test_preview"
  ) {
    const entry = job.parameters.entry || "index.html";
    const built = await buildHTML(job, entry, { root: ROOT });
    const html = built.html;
    details.dependencies = built.dependencies;
    details.preview_html = html;
    check(
      "build",
      `固定 esbuild 构建成功，${built.bundled} 个脚本，${Buffer.byteLength(html)} 字节`,
    );
    await checkBrowser(
      html,
      job.parameters,
      job.action === "test_preview",
      check,
      // Assertions can refer only to files in this registered execution input.
      { inputPaths: Object.keys(job.files), initialState: job.files['atoms.app.json'] ? JSON.parse(job.files['atoms.app.json']).initial_state : {} },
    );
  } else throw Error("不支持的执行动作");
  details.elapsed_ms = Date.now() - start;
  try {
    details.memory_peak_bytes = Number(
      (await fs.readFile("/sys/fs/cgroup/memory.peak", "utf8")).trim(),
    );
  } catch {}
  details.node = process.version;
  details.runtime = "isolated-node";
})()
  .catch((e) => check("execution", e.message, "failed"))
  .finally(() => {
    process.stdout.write(
      JSON.stringify({
        id: job?.id,
        digest: job?.digest,
        status:
          checks.length && checks.every((c) => c.status === "passed")
            ? "passed"
            : "failed",
        checks,
        details,
        artifacts,
      }),
    );
    process.exitCode = 0;
  });
