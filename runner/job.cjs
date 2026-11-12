// Trusted harness: generated scripts run as a separate UID, without credentials.
// Production isolation is provided by the executor's required gVisor runtime.
const fs = require("node:fs/promises");
const path = require("node:path");
const http = require("node:http");
const { spawn } = require("node:child_process");
const esbuild = require("esbuild");
const { chromium } = require("playwright");
const ROOT = "/workspace/project";
const SCRATCH = "/workspace/scratch";
const CSP =
  "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'; form-action 'none'; base-uri 'none'; sandbox allow-scripts allow-forms";
const checks = [];
const details = {};
let job;
function check(name, detail, status = "passed") {
  checks.push({ name, status, detail: String(detail).slice(0, 4000) });
}
function safeName(name) {
  if (
    typeof name !== "string" ||
    name.length > 180 ||
    name.includes("\\") ||
    name.includes(":") ||
    name.startsWith("/") ||
    name
      .split("/")
      .some((p) => !p || p.startsWith(".") || p === "node_modules") ||
    /[\x00-\x1f]/.test(name)
  )
    throw Error("无效项目文件路径");
  return name;
}
async function loadFiles() {
  const entries = Object.entries(job.files || {});
  if (entries.length > 100) throw Error("文件数量超限");
  let total = 0;
  await fs.mkdir(ROOT, { recursive: true });
  for (const [name, content] of entries) {
    safeName(name);
    if (typeof content !== "string" || Buffer.byteLength(content) > 262144)
      throw Error("文件格式或大小无效");
    total += Buffer.byteLength(content);
    if (total > 2097152) throw Error("项目大小超限");
    const target = path.join(ROOT, name);
    await fs.mkdir(path.dirname(target), { recursive: true });
    await fs.writeFile(target, content, { mode: 0o444 });
  }
}
function local(ref, base) {
  if (/^(data:|#)/i.test(ref)) return null;
  if (/^[a-z]+:|^\/\//i.test(ref)) throw Error("不支持外部资源：" + ref);
  const name = path.posix.normalize(
    path.posix.join(path.posix.dirname(base), ref.replace(/^\//, "")),
  );
  safeName(name);
  if (!(name in job.files)) throw Error("本地资源不存在：" + name);
  return name;
}
async function buildHTML(entry) {
  safeName(entry);
  if (!entry.endsWith(".html") || !job.files[entry])
    throw Error("网页入口不存在");
  const { parse, serialize } = await import("parse5");
  const doc = parse(job.files[entry]);
  let bundled = 0;
  const asset = (ref, base) => {
    const name = local(ref, base);
    if (!name) return ref;
    const ext = path.extname(name);
    const mime = {
      ".svg": "image/svg+xml",
      ".png": "image/png",
      ".jpg": "image/jpeg",
      ".woff2": "font/woff2",
    }[ext];
    if (!mime) throw Error("不支持的图片或字体格式：" + name);
    return `data:${mime};base64,${Buffer.from(job.files[name]).toString("base64")}`;
  };
  const css = (text, base) =>
    text.replace(
      /url\(\s*(['"]?)(.*?)\1\s*\)/gi,
      (_, q, ref) => `url("${asset(ref, base)}")`,
    );
  const bundle = async (name, text) => {
    const opts = {
      bundle: true,
      write: false,
      format: "iife",
      platform: "browser",
      target: "es2022",
      minify: true,
      jsx: "automatic",
      nodePaths: ["/opt/atoms/node_modules"],
      define: { "process.env.NODE_ENV": '"production"' },
      logLevel: "silent",
      loader: {
        ".svg": "dataurl",
        ".png": "dataurl",
        ".jpg": "dataurl",
        ".woff2": "dataurl",
      },
      absWorkingDir: ROOT,
    };
    const res = await esbuild.build(
      name
        ? { ...opts, entryPoints: [path.join(ROOT, name)] }
        : {
            ...opts,
            stdin: {
              contents: text,
              resolveDir: ROOT,
              sourcefile: "inline.js",
              loader: "js",
            },
          },
    );
    if (res.outputFiles.length !== 1) throw Error("请通过 HTML link 引用 CSS");
    bundled++;
    return res.outputFiles[0].text.replace(/<\/script/gi, "<\\/script");
  };
  async function visit(node) {
    const attrs = node.attrs || [];
    const attr = (n) => attrs.find((a) => a.name === n);
    const remove = (n) => {
      node.attrs = (node.attrs || []).filter((a) => a.name !== n);
    };
    const setText = (text) => {
      node.childNodes = [{ nodeName: "#text", value: text, parentNode: node }];
    };
    if (
      node.tagName === "base" ||
      node.tagName === "iframe" ||
      node.tagName === "object" ||
      node.tagName === "embed"
    )
      throw Error("不支持嵌入外部页面或修改基础地址");
    if (node.tagName === "meta" && attr("http-equiv"))
      throw Error("不允许 http-equiv 元信息");
    if (node.tagName === "script") {
      const type = attr("type")?.value;
      if (
        !type ||
        ["module", "text/javascript", "application/javascript"].includes(type)
      ) {
        const src = attr("src");
        const name = src ? local(src.value, entry) : null;
        const code = name
          ? await bundle(name)
          : await bundle(
              null,
              (node.childNodes || []).map((n) => n.value || "").join(""),
            );
        remove("src");
        remove("type");
        remove("integrity");
        setText(code);
      }
    }
    if (node.tagName === "link") {
      if (attr("rel")?.value === "stylesheet") {
        const name = local(attr("href")?.value || "", entry);
        if (!name) throw Error("样式引用无效");
        node.tagName = "style";
        node.nodeName = "style";
        node.attrs = [];
        setText(css(job.files[name], name));
      } else if (attr("href"))
        attr("href").value = asset(attr("href").value, entry);
    }
    if (node.tagName === "style") {
      const text = (node.childNodes || []).map((n) => n.value || "").join("");
      if (/@import/i.test(text)) throw Error("不允许 CSS @import");
      setText(css(text, entry));
    }
    if (attr("style")) attr("style").value = css(attr("style").value, entry);
    if (node.tagName === "img" && attr("src"))
      attr("src").value = asset(attr("src").value, entry);
    if (attr("srcset")) throw Error("首版请使用单一 src 图片资源");
    if (
      node.tagName === "a" &&
      attr("href") &&
      !attr("href").value.startsWith("#")
    )
      throw Error("首版网页仅支持页内链接");
    for (const child of node.childNodes || []) await visit(child);
  }
  await visit(doc);
  const html = serialize(doc);
  if (Buffer.byteLength(html) > 1048576) throw Error("构建后网页超过 1 MiB");
  check(
    "build",
    `固定 esbuild 构建成功，${bundled} 个脚本，${Buffer.byteLength(html)} 字节`,
  );
  details.preview_html = html;
  return html;
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
  const output = [includeStdout ? result.out : "", result.err].filter(Boolean).join("\n").trim();
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
  const p = job.parameters;
  safeName(p.entry);
  const ext = path.extname(p.entry);
  if (!/^\.(?:[mc]?js|py|[mc]?ts|tsx)$/.test(ext) || !Object.hasOwn(job.files, p.entry))
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
    if (bundle.outputFiles.length !== 1) throw Error("TypeScript 脚本必须能打包成单个入口");
    // Keeping the bundle beside the entry preserves relative data-file paths.
    entry = path.join(path.dirname(entry), commonJS ? ".atoms-entry.cjs" : ".atoms-entry.mjs");
    await fs.writeFile(entry, bundle.outputFiles[0].contents, { mode: 0o444 });
    check("typescript_transpile", "esbuild 转译成功");
  }
  const result = await runChild(command, [...options, entry, ...(p.args || [])], {
    seconds: 15,
    env: { NODE_PATH: "/opt/atoms/node_modules" },
  });
  details.stdout = result.out;
  details.stderr = result.err;
  details.exit_code = result.code;
  checkExit("script_exit", result, 15);
  check(
    "expected_stdout",
    `预期 ${JSON.stringify(p.expected_stdout.trim())}；实际 ${JSON.stringify(result.out.trim())}`,
    result.out.trim() === p.expected_stdout.trim() ? "passed" : "failed",
  );
}

async function goProject() {
  if (job.parameters.entry !== "go.mod" || !Object.hasOwn(job.files, "go.mod"))
    throw Error("Go 项目需要根目录下的 go.mod");
  await prepareScratch();
  const go = "/usr/local/go/bin/go";
  const mod = Object.hasOwn(job.files, "vendor/modules.txt") ? "vendor" : "readonly";
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
  const cache = await runChild("/bin/cp", ["-R", "/opt/atoms/go-cache", env.GOCACHE], {
    seconds: 10,
  });
  if (cache.limit || cache.code !== 0) throw Error("Go 编译缓存初始化失败：" + cache.err);
  const build = await runChild(go, ["build", "-o", SCRATCH + "/", "./..."], {
    cwd: ROOT, env, seconds: 65,
  });
  details.go_build = { stdout: build.out, stderr: build.err, exit_code: build.code };
  if (!checkExit("go_build", build, 65)) return;
  const test = await runChild(go, ["test", "-count=1", "-timeout=25s", "./..."], {
    cwd: ROOT, env, seconds: 45,
  });
  details.go_test = { stdout: test.out, stderr: test.err, exit_code: test.code };
  checkExit("go_test", test, 45, true);
}
async function browser(html, test) {
  const p = job.parameters;
  const steps = p.steps || [];
  if (test) {
    if (
      !Array.isArray(steps) ||
      steps.length > 30 ||
      !steps.some(
        (s) => typeof s.action === "string" && s.action.startsWith("expect_"),
      )
    )
      throw Error("交互检查需要至少一个断言，最多 30 步");
    for (const s of steps) {
      if (
        typeof s.requirement !== "string" ||
        !s.requirement.trim() ||
        s.requirement.length > 500 ||
        typeof s.selector !== "string" ||
        s.selector.length > 300
      )
        throw Error("测试步骤必须有有效选择器和验收目标");
      if (
        ![
          "click",
          "fill",
          "select",
          "check",
          "expect_text",
          "expect_count",
          "expect_visible",
        ].includes(s.action)
      )
        throw Error("不支持的测试动作");
      if (
        ["fill", "select", "expect_text"].includes(s.action) &&
        typeof s.value !== "string"
      )
        throw Error("步骤缺少字符串 value");
      if (
        s.action === "expect_count" &&
        (!Number.isInteger(s.count) || s.count < 0 || s.count > 10000)
      )
        throw Error("数量断言无效");
    }
  }
  const server = http.createServer((req, res) => {
    res.writeHead(200, {
      "Content-Type": "text/html; charset=utf-8",
      "Content-Security-Policy": CSP,
    });
    res.end(html);
  });
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  const url = `http://127.0.0.1:${server.address().port}`;
  let b;
  try {
    b = await chromium.launch({
      headless: true,
      args: [
        "--no-sandbox",
        "--disable-dev-shm-usage",
        "--disable-background-networking",
      ],
    });
    for (const width of test ? [1440, 390] : [1440]) {
      const context = await b.newContext({
        viewport: { width, height: 900 },
        serviceWorkers: "block",
        acceptDownloads: false,
      });
      await context.route("**/*", (route) =>
        route.request().url() === url + "/" ? route.continue() : route.abort(),
      );
      const page = await context.newPage();
      page.setDefaultTimeout(3500);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("console", (m) => {
        if (m.type() === "error") errors.push(m.text());
      });
      await page.goto(url, { waitUntil: "load", timeout: 10000 });
      await page.waitForTimeout(100);
      check(`startup_${width}`, "Chromium 实际加载完成");
      for (let i = 0; i < steps.length; i++) {
        const s = steps[i];
        const loc = page.locator(s.selector);
        try {
          switch (s.action) {
            case "click":
              await loc.click();
              break;
            case "fill":
              await loc.fill(s.value);
              break;
            case "select":
              await loc.selectOption(s.value);
              break;
            case "check":
              await loc.check();
              break;
            case "expect_text":
              await loc
                .filter({ hasText: s.value })
                .first()
                .waitFor({ state: "visible" });
              break;
            case "expect_count": {
              let n = 0;
              for (let j = 0; j < 15; j++) {
                n = await loc.count();
                if (n === s.count) break;
                await page.waitForTimeout(100);
              }
              if (n !== s.count) throw Error(`期望 ${s.count}，实际 ${n}`);
              break;
            }
            case "expect_visible":
              await loc.waitFor({ state: "visible" });
              break;
          }
          check(
            `interaction_${width}_${i + 1}`,
            `${s.requirement}：${s.action} ${s.selector}`,
          );
        } catch (e) {
          check(
            `interaction_${width}_${i + 1}`,
            `${s.requirement}：${e.message}`,
            "failed",
          );
          break;
        }
      }
      await page.waitForTimeout(150);
      check(
        `console_${width}`,
        errors.length ? errors.slice(0, 5).join("\n") : "无页面脚本错误",
        errors.length ? "failed" : "passed",
      );
      const overflow = await page.evaluate(
        () => document.documentElement.scrollWidth > innerWidth + 2,
      );
      check(
        `layout_${width}`,
        overflow ? "页面存在水平溢出" : "无页面水平溢出",
        overflow ? "failed" : "passed",
      );
      await context.close();
    }
  } finally {
    if (b) await b.close();
    await new Promise((r) => server.close(r));
  }
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
  else if ((job.action === "run_project" && job.parameters.kind === "web") || job.action === "test_preview") {
    const entry = job.parameters.entry || "index.html";
    const html = await buildHTML(entry);
    await browser(html, job.action === "test_preview");
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
      }),
    );
    process.exitCode = 0;
  });
