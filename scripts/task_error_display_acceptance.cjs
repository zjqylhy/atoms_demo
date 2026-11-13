// UI regression fixtures: no production data, authentication or model calls.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const http = require('node:http');
const path = require('node:path');
const { chromium } = require('../runner/node_modules/playwright');

const root = path.resolve(__dirname, '..');
const dist = path.join(root, 'web/dist');
const out = path.join(root, 'artifacts', 'task-error-display-' + new Date().toISOString().replace(/[:.]/g, '-'));
const projectID = 'a'.repeat(32), currentTask = 'b'.repeat(32), otherTask = 'c'.repeat(32);
const configError = '模型服务尚未配置，请由部署方设置环境变量';
const results = [], errors = [];
let browser, server, page, fixture, modelReady = true, serial = 0;
function run(task, status, error = '', calls = 0) {
  serial++;
  return { id: serial.toString(16).padStart(32, '0'), task_id: task, status, error, calls, tools: 0, usage: { total: 0, known: true }, started: new Date(Date.UTC(2026, 10, 14, 0, 0, serial)).toISOString() };
}
function project(status, runs) {
  return {
    id: projectID, title: '模型配置提示回归', updated: new Date().toISOString(), current_task: currentTask,
    active_run: status === 'running' ? runs.at(-1).id : '',
    tasks: [{ id: currentTask, goal: '模型配置提示回归', status, mode: 'build', checkpoints: 14 }],
    runs, files: {}, versions: [], reports: [], messages: [], events: [],
  };
}
function pass(name) { results.push({ name, status: 'passed' }); console.log('PASS ' + name); }

(async () => {
  await fs.mkdir(out, { recursive: true });
  server = http.createServer(async (req, res) => {
    try {
      const pathname = new URL(req.url, 'http://localhost').pathname;
      const target = path.resolve(dist, pathname === '/' ? 'index.html' : '.' + pathname);
      if (!target.startsWith(dist + path.sep)) throw Error('invalid path');
      const body = await fs.readFile(target);
      const type = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css' }[path.extname(target)] || 'application/octet-stream';
      res.writeHead(200, { 'Content-Type': type, 'Cache-Control': 'no-store' }); res.end(body);
    } catch { res.writeHead(404); res.end(); }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const base = 'http://127.0.0.1:' + server.address().port;
  browser = await chromium.launch({ headless: true, ...(process.platform === 'win32' ? { executablePath: 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe' } : {}) });
  page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  page.setDefaultTimeout(10000);
  page.on('pageerror', error => errors.push(error.message));
  await page.route(base + '/api/**', async route => {
    const url = new URL(route.request().url());
    if (url.pathname === '/api/bootstrap') return route.fulfill({ json: { user: { id: 'test-account', username: 'test-account' }, csrf: 'test-csrf', model_ready: modelReady, runner_configured: true, notice: '' } });
    if (url.pathname === '/api/catalog') return route.fulfill({ json: [] });
    if (url.pathname === '/api/project-order') return route.fulfill({ json: { revision: 1, projects: [{ id: projectID, title: fixture.title }] } });
    if (url.pathname === '/api/projects/' + projectID) return route.fulfill({ json: fixture });
    if (url.pathname.endsWith('/events')) return route.fulfill({ contentType: 'text/event-stream', body: 'event: snapshot\ndata: {}\n\n' });
    if (url.pathname.endsWith('/actions') && route.request().postDataJSON().action === 'continue') {
      fixture = project('running', [...fixture.runs, run(currentTask, 'running')]);
      return route.fulfill({ json: fixture });
    }
    errors.push('Unexpected API request: ' + url.pathname);
    return route.fulfill({ status: 404, json: { error: 'unexpected fixture request' } });
  });
  async function load(status, runs, ready = true) {
    modelReady = ready; fixture = project(status, runs);
    await page.goto(base + '/?project=' + projectID);
    if (status === 'running') await page.getByRole('button', { name: '停止任务', exact: true }).waitFor();
    else if (status === 'completed') await page.getByText('已完成', { exact: true }).first().waitFor();
    else await page.getByRole('button', { name: '继续任务', exact: true }).waitFor();
  }
  await load('stopped', [run(currentTask, 'failed', configError), run(currentTask, 'stopped', '', 25)]);
  assert.equal(await page.locator('.recovery .failure-detail').count(), 0, 'A later stopped run must not show the earlier missing-key error');
  await page.getByRole('button', { name: '切换侧边栏', exact: true }).click();
  await page.getByText('模型服务已配置', { exact: true }).waitFor();
  await page.screenshot({ path: path.join(out, 'restored-key-stopped-task.png'), fullPage: true });
  pass('restored key and later stopped run do not inherit the old configuration error');

  await load('stopped', [run(otherTask, 'failed', '另一个任务的错误'), run(currentTask, 'stopped')]);
  assert.equal(await page.locator('.failure-detail').count(), 0);
  pass('another task error is never shown on the current task');

  await load('failed', [run(currentTask, 'failed', configError), run(currentTask, 'failed', '本次上游请求超时')]);
  assert.equal(await page.locator('.failure-detail').innerText(), '上次运行错误：本次上游请求超时');
  pass('the newest actual error remains visible and is identified as the last run');

  await load('failed', [run(currentTask, 'failed', '当前任务错误'), run(otherTask, 'failed', '其他任务较晚的错误')]);
  assert.equal(await page.locator('.failure-detail').innerText(), '上次运行错误：当前任务错误');
  pass('run selection belongs to the current task even if another task has a later run');

  await load('stopped', [run(otherTask, 'failed', configError)]);
  assert.equal(await page.locator('.failure-detail').count(), 0);
  pass('a task without runs does not borrow another task error');

  await load('failed', [run(currentTask, 'failed', configError)]);
  assert.equal(await page.locator('.failure-detail').innerText(), '上次运行错误：' + configError);
  pass('restoring configuration preserves the genuine last failure as historical execution information');
  await page.getByRole('button', { name: '继续任务', exact: true }).click();
  await page.getByRole('button', { name: '停止任务', exact: true }).waitFor();
  assert.equal(await page.locator('.recovery').count(), 0);
  pass('continuing starts a new simulated run and removes the recovery error without a page refresh');

  await load('failed', [run(currentTask, 'failed', configError)], false);
  assert.equal(await page.locator('.failure-detail').innerText(), '上次运行错误：' + configError);
  pass('a missing key still reports the actual latest configuration failure');

  await load('completed', [run(currentTask, 'failed', configError), run(currentTask, 'completed', '', 25)]);
  assert.equal(await page.locator('.recovery').count(), 0);
  pass('completed tasks do not display an old recovery error');
  assert.deepEqual(errors, []);
  await fs.writeFile(path.join(out, 'results.json'), JSON.stringify({ results, modelCalls: 0, notes: ['Built frontend with explicitly simulated API snapshots on a local test server. No production task was run.'] }, null, 2));
  console.log('ARTIFACTS ' + out);
})().catch(async error => {
  console.error(error);
  if (page) await page.screenshot({ path: path.join(out, 'failure.png'), fullPage: true }).catch(() => {});
  await fs.writeFile(path.join(out, 'failure.txt'), String(error.stack));
  process.exitCode = 1;
}).finally(async () => {
  if (browser) await browser.close();
  if (server) await new Promise(resolve => server.close(resolve));
});
