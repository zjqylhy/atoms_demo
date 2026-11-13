const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const { buildHTML } = require("./web.cjs");
const { checkBrowser } = require("./browser.cjs");

test("real build and browser track transitive inputs and resolution changes", async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "atoms-web-test-"));
  t.after(() => {
    assert.ok(
      path
        .resolve(root)
        .startsWith(path.resolve(os.tmpdir()) + path.sep + "atoms-web-test-"),
    );
    return fs.rm(root, { recursive: true, force: true });
  });
  const files = {
    "pages/view.html":
      '<link rel="stylesheet" href="style.css"><h1 id="message"></h1><button id="next">Next</button><script src="app.ts"></script>',
    "pages/app.ts":
      'import { title } from "./content"; document.querySelector("#message").textContent=title; document.querySelector("#next").onclick=()=>document.querySelector("#message").textContent="Second";',
    "pages/content.js": 'export const title="Original";',
    "pages/style.css": 'body{background-image:url("image.svg")}',
    "unrelated.pptx": "saved independent artifact",
    "package.json": '{"name":"fixture","private":true}',
  };
  const binary_files = {
    "pages/image.svg": Buffer.from(
      '<svg xmlns="http://www.w3.org/2000/svg"/>',
    ).toString("base64"),
  };
  const write = async () => {
    for (const [name, data] of Object.entries(files)) {
      await fs.mkdir(path.dirname(path.join(root, name)), { recursive: true });
      await fs.writeFile(path.join(root, name), data);
    }
    for (const [name, data] of Object.entries(binary_files))
      await fs.writeFile(path.join(root, name), Buffer.from(data, "base64"));
  };
  const build = () =>
    buildHTML({ files, binary_files }, "pages/view.html", {
      root,
      nodePaths: [path.join(__dirname, "node_modules")],
    });
  await write();
  const original = await build();
  assert.deepEqual(original.dependencies.paths, [
    "package.json",
    "pages/app.ts",
    "pages/content.js",
    "pages/image.svg",
    "pages/style.css",
    "pages/view.html",
  ]);
  const checks = [];
  await checkBrowser(
    original.html,
    {
      entry: "pages/view.html",
      steps: [
        {
          action: "expect_text",
          selector: "#message",
          value: "Original",
          requirement: "原始标题",
        },
        { action: "click", selector: "#next", requirement: "可翻页" },
        {
          action: "expect_text",
          selector: "#message",
          value: "Second",
          requirement: "可翻页",
          paths: ["pages/view.html", "pages/app.ts", "pages/style.css"],
        },
      ],
    },
    true,
    (name, detail, status = "passed", evidence = {}) =>
      checks.push({ name, detail, status, ...evidence }),
    {
      inputPaths: Object.keys(files),
      launchOptions: process.env.ATOMS_TEST_BROWSER_CHANNEL
        ? { channel: process.env.ATOMS_TEST_BROWSER_CHANNEL }
        : process.platform === "win32"
          ? { channel: "chrome" }
          : {},
    },
  );
  assert.ok(checks.length >= 12);
  assert.ok(
    checks.every((c) => c.status === "passed"),
    JSON.stringify(checks),
  );
  assert.ok(
    checks
      .filter((c) => c.requirement)
      .every((c) => c.paths[0] === "pages/view.html"),
  );
  files["unrelated.pptx"] = "changed independent artifact";
  await write();
  const unrelated = await build();
  assert.equal(unrelated.html, original.html);
  assert.deepEqual(unrelated.dependencies, original.dependencies);
  files["pages/content.ts"] = 'export const title="Shadowed";';
  await write();
  const shadowed = await build();
  assert.notDeepEqual(
    shadowed.dependencies.namespace,
    original.dependencies.namespace,
  );
  assert.ok(shadowed.dependencies.paths.includes("pages/content.ts"));
  assert.match(shadowed.html, /Shadowed/);
  delete files["pages/content.js"];
  delete files["pages/content.ts"];
  await fs.unlink(path.join(root, "pages/content.js"));
  await fs.unlink(path.join(root, "pages/content.ts"));
  await assert.rejects(build(), /resolve/);
});

test("browser rejects assertion paths outside the registered input", async () => {
  await assert.rejects(
    checkBrowser(
      "<h1>actual</h1>",
      {
        steps: [
          {
            action: "expect_text",
            selector: "h1",
            value: "actual",
            requirement: "title",
            paths: ["missing.html"],
          },
        ],
      },
      true,
      () => {},
      { inputPaths: ["index.html"] },
    ),
    /paths 必须指向实际输入文件/,
  );
});

test("inline modules and missing resources do not produce incomplete exact evidence", async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "atoms-web-test-"));
  t.after(() => {
    assert.ok(
      path
        .resolve(root)
        .startsWith(path.resolve(os.tmpdir()) + path.sep + "atoms-web-test-"),
    );
    return fs.rm(root, { recursive: true, force: true });
  });
  const files = {
    "index.html": '<script>document.body.textContent="OK";</script>',
  };
  const built = await buildHTML({ files }, "index.html", { root });
  assert.deepEqual(built.dependencies.paths, ["index.html"]);
  files["index.html"] = '<img src="missing.png">';
  await assert.rejects(buildHTML({ files }, "index.html", { root }), /不存在/);
});
