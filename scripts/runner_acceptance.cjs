// Integration tests exercise real containers. They do not invoke any model.
const { spawn, execFileSync } = require("node:child_process");
const crypto = require("node:crypto");
const fs = require("node:fs/promises");
const assert = require("node:assert/strict");
const http = require("node:http");
const path = require("node:path");
process.chdir(path.resolve(__dirname, ".."));
const runtime = process.env.ATOMS_ACCEPTANCE_RUNTIME || "runc";
const token = crypto.randomBytes(32).toString("hex");
const env = {
  ...process.env,
  ATOMS_RUNNER_PORT: "8092",
  ATOMS_RUNNER_TOKEN: token,
  ATOMS_RUNNER_LOCAL_DEV: runtime === "runc" ? "1" : "0",
  ATOMS_SANDBOX_RUNTIME: runtime,
  ATOMS_RUNNER_DATA: "data/runner-acceptance",
};
const child = spawn("node", ["runner/server.cjs"], {
  env,
  stdio: ["ignore", "pipe", "pipe"],
  windowsHide: true,
});
let log = "";
child.stdout.on("data", (b) => {
  log += b;
});
child.stderr.on("data", (b) => {
  log += b;
});
const results = [];
const base = "http://127.0.0.1:8092";
const digest = (x) =>
  crypto.createHash("sha256").update(JSON.stringify(x)).digest("hex");
const job = (files, action, parameters) => ({
  id: crypto.randomBytes(32).toString("hex"),
  digest: digest(files),
  files,
  action,
  parameters,
});
async function post(body, auth = token) {
  const r = await fetch(base + "/jobs", {
    method: "POST",
    headers: {
      Authorization: "Bearer " + auth,
      "Content-Type": "application/json",
    },
    body: JSON.stringify(body),
  });
  return { status: r.status, body: await r.json() };
}
async function run(name, j, want) {
  const start = Date.now();
  const r = await post(j);
  if (name === "infinite-loop-terminated") assert.ok(Date.now() - start < 35000, "script deadline was not enforced");
  assert.equal(r.status, 200, JSON.stringify(r));
  assert.equal(r.body.status, want, JSON.stringify({...r.body, details:{...r.body.details, preview_html:undefined}}));
  results.push({
    name,
    status: r.body.status,
    seconds: (Date.now() - start) / 1000,
    checks: r.body.checks,
    resources: { ...r.body.details, preview_html: undefined },
  });
  console.log(name, r.body.status, r.body.details?.memory_peak_bytes || "n/a");
  return r;
}
(async () => {
  try {
    for (let i = 0; i < 60; i++) {
      if (child.exitCode !== null) throw Error(log);
      try {
        if ((await fetch(base + "/healthz")).ok) break;
      } catch {}
      await new Promise((r) => setTimeout(r, 500));
    }
    if (process.env.ATOMS_ACCEPTANCE_FIXTURE) {
      const fixture = JSON.parse(await fs.readFile(process.env.ATOMS_ACCEPTANCE_FIXTURE, "utf8"));
      await run("generated-page-build", job(fixture.files, "run_project", {kind:"web",entry:"index.html"}), "passed");
      await run("generated-page-interactions", job(fixture.files, "test_preview", {steps:fixture.steps}), "passed");
      await fs.mkdir("artifacts", {recursive:true});
      await fs.writeFile("artifacts/runner-fixture.json", JSON.stringify({at:new Date().toISOString(),runtime,source_digest:digest(fixture.files),results},null,2));
      console.log("Generated-page acceptance passed.");
      return;
    }
    const js = job(
      { "sum.js": "console.log([2,3,5].reduce((a,b)=>a+b,0))" },
      "run_project",
      { kind: "script", entry: "sum.js", expected_stdout: "10" },
    );
    const first = await run("node-output", js, "passed");
    const duplicate = await post(js);
    assert.deepEqual(duplicate.body, first.body);
    results.push({ name: "same-job-id-not-reexecuted", status: "passed" });
    const clash = await post({
      ...js,
      parameters: { ...js.parameters, expected_stdout: "12" },
    });
    assert.equal(clash.status, 409);
    await run(
      "wrong-expectation",
      job({ "sum.js": "console.log(10)" }, "run_project", {
        kind: "script",
        entry: "sum.js",
        expected_stdout: "11",
      }),
      "failed",
    );
    const page = {
      "index.html":
        '<!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1"></head><body><h1>Counter</h1><form id="counter-form"><button id="plus">Add</button></form><output id="count">0</output><script src="app.js"></script></body></html>',
      "app.js":
        "let n=0;document.querySelector('#counter-form').addEventListener('submit',e=>{e.preventDefault();document.querySelector('#count').textContent=String(++n)})",
    };
    await run(
      "web-build-start",
      job(page, "run_project", { kind: "web", entry: "index.html" }),
      "passed",
    );
    await run(
      "desktop-mobile-interaction",
      job(page, "test_preview", {
        steps: [
          { action: "click", selector: "#plus", requirement: "点击增加计数" },
          {
            action: "expect_text",
            selector: "#count",
            value: "1",
            requirement: "计数从 0 变为 1",
          },
        ],
      }),
      "passed",
    );
    await run(
      "broken-button-detected",
      job(
        {
          ...page,
          "app.js":
            "document.querySelector('#plus').addEventListener('click',()=>{})",
        },
        "test_preview",
        {
          steps: [
            { action: "click", selector: "#plus", requirement: "点击增加计数" },
            {
              action: "expect_text",
              selector: "#count",
              value: "1",
              requirement: "计数增加",
            },
          ],
        },
      ),
      "failed",
    );
    await run(
      "no-credential-no-network-readonly",
      job(
        {
          "guard.js": `const fs=require('fs');const http=require('http');let blocked=false;try{fs.writeFileSync('/opt/atoms/escape','x')}catch{blocked=true};if(!blocked)throw Error('root fs writable');if(Object.keys(process.env).some(k=>k.startsWith('ATOMS_')))throw Error('credentials inherited');const r=http.get('http://1.1.1.1',()=>{throw Error('network escaped')});r.setTimeout(1000,()=>r.destroy());r.on('error',()=>console.log('isolated'));`,
        },
        "run_project",
        { kind: "script", entry: "guard.js", expected_stdout: "isolated" },
      ),
      "passed",
    );
    await run(
      "infinite-loop-terminated",
      job({ "loop.js": "while(true){}" }, "run_project", {
        kind: "script",
        entry: "loop.js",
        expected_stdout: "",
      }),
      "failed",
    );
    assert.equal(
      (await post(job({ "../escape.js": "x" }, "run_project", {}))).status,
      400,
    );
    assert.equal((await post(js, "invalid")).status, 401);
    // Deliberately split a Chinese UTF-8 codepoint across HTTP chunks.
    const chinese = job({"unicode.js": "console.log('中文保持完整')"}, "run_project", {kind:"script", entry:"unicode.js", expected_stdout:"中文保持完整"});
    const bytes = Buffer.from(JSON.stringify(chinese));
    const cut = bytes.indexOf(Buffer.from("中")) + 1;
    const unicodeResult = await new Promise((resolve, reject) => {
      const request = http.request(base + "/jobs", {method:"POST", headers:{Authorization:"Bearer " + token, "Content-Type":"application/json"}}, response => {
        response.setEncoding("utf8");
        let body = "";
        response.on("data", chunk => body += chunk);
        response.on("end", () => { try { resolve(JSON.parse(body)); } catch (e) { reject(e); } });
      });
      request.on("error", reject);
      request.write(bytes.subarray(0, cut));
      setTimeout(() => request.end(bytes.subarray(cut)), 20);
    });
    assert.equal(unicodeResult.status, "passed", JSON.stringify(unicodeResult));
    results.push({name:"utf8-chunk-boundary", status:"passed"});
    results.push({ name: "path-and-auth-rejected", status: "passed" });
    await fs.mkdir("artifacts", { recursive: true });
    await fs.writeFile(
      "artifacts/runner-acceptance.json",
      JSON.stringify(
        { at: new Date().toISOString(), runtime, results },
        null,
        2,
      ),
    );
    console.log("All executor acceptance checks passed.");
  } finally {
    child.kill();
    await new Promise((r) => child.once("close", r));
  }
})().catch((e) => {
  console.error(e);
  process.exitCode = 1;
});
