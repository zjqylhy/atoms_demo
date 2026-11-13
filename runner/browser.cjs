const http = require("node:http");
const { chromium } = require("playwright");
const CSP =
  "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'; form-action 'none'; base-uri 'none'; sandbox allow-scripts allow-forms";
async function checkBrowser(html, p, test, check, launchOptions = {}) {
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
        s.paths !== undefined &&
        (!Array.isArray(s.paths) ||
          !s.paths.length ||
          s.paths.some(
            (name) =>
              typeof name !== "string" || !Object.hasOwn(job.files, name),
          ))
      )
        throw Error("浏览器断言的 paths 必须指向实际输入文件");
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
      ...launchOptions,
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
            "passed",
            s.action.startsWith("expect_")
              ? {
                  requirement: s.requirement,
                  paths: s.paths || [p.entry || "index.html"],
                }
              : {},
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
module.exports = { checkBrowser };
