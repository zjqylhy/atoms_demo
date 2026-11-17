// Serve the built workbench with controlled project snapshots and a real event stream.
const assert = require('node:assert/strict');
const { createHash } = require('node:crypto');
const fs = require('node:fs/promises');
const http = require('node:http');
const path = require('node:path');
const { chromium } = require('../runner/node_modules/playwright');

const dist = path.resolve(__dirname, '../web/dist');
const out = path.resolve(__dirname, '../artifacts', 'preview-entry-' + new Date().toISOString().replace(/[:.]/g, '-'));
const files = {
  'app.js': 'const previousScript = true;',
  'index.html': '<title>首页源码</title>',
  'pages/dashboard.html': '<title>看板源码</title>',
  'README.md': '# 分析报告',
  'sales.csv': '产品,金额\n咖啡,42',
  'sales.chart.json': JSON.stringify({ title: '销售走势', points: [{ label: '咖啡', value: 42 }] }),
  'slides.pptx': Buffer.from([0x50, 0x4b, 0, 0]),
};
function manifest(...names) {
  return Object.fromEntries(names.map(name => {
    const content = files[name];
    return [name, { hash: createHash('sha256').update(content).digest('hex'), size: Buffer.byteLength(content), binary: Buffer.isBuffer(content) }];
  }));
}
const home = manifest('index.html');
const multiplePages = manifest('index.html', 'pages/dashboard.html');
const builds = new Map([
  ['first', '首次构建'],
  ['rebuilt', '重新构建'],
  ['saved-home', '旧版首页'],
  ['saved-dashboard', '已交付看板'],
  ['legacy', '旧记录的交互页面'],
]);
function preview(report, entry) {
  return { preview_ready: true, preview_entry: entry, preview_report_id: report };
}
const versionOne = { id: 'v1', number: 1, manifest: manifest('app.js', 'index.html'), reports: [], ...preview('saved-home', 'index.html') };
const versionTwo = { id: 'v2', number: 2, manifest: multiplePages, reports: [], ...preview('saved-dashboard', 'pages/dashboard.html') };
const states = new Map();
const streams = new Map();
let revision = 0;
function save(id, value) {
  const updated = new Date(Date.UTC(2026, 10, 17) + revision++).toISOString();
  states.set(id, { id, messages: [], tasks: [], events: [], runs: [], versions: [], reports: [], ...states.get(id), ...value, updated });
  for (const stream of streams.get(id) || []) stream.write('event: snapshot\ndata: {}\n\n');
}
const [live, saved, legacy, documents, empty] = ['1', '2', '3', '4', '5'].map(char => char.repeat(32));
save(live, { title: '正在生成的新项目', files: manifest('app.js') });
save(saved, { title: '历史网页项目', files: multiplePages, versions: [versionOne, versionTwo], head: 'v2', ...preview('saved-dashboard', 'pages/dashboard.html') });
save(legacy, { title: '旧报告项目', files: multiplePages, ...preview('legacy') });
save(documents, { title: '分析产物项目', files: manifest('README.md', 'sales.csv', 'sales.chart.json', 'slides.pptx') });
save(empty, { title: '空项目', files: {} });

const results = [], errors = [];
let browser, server, page;
function pass(name) {
  results.push({ name, status: 'passed' });
  console.log('PASS ' + name);
}
function reply(res, type, content) {
  res.writeHead(200, { 'Content-Type': type, 'Cache-Control': 'no-store' });
  res.end(content);
}
const json = (res, data) => reply(res, 'application/json', JSON.stringify(data));

async function serve(req, res) {
  const { pathname, searchParams } = new URL(req.url, 'http://127.0.0.1');
  if (pathname === '/api/bootstrap') {
    return json(res, { user: { id: 'preview-fixture', username: 'preview_fixture' }, csrf: 'fixture', model_ready: true });
  }
  if (pathname === '/api/catalog') return json(res, []);
  if (pathname === '/api/project-order') {
    return json(res, { revision: 1, projects: [...states.values()].map(({ id, title }) => ({ id, title })) });
  }
  if (pathname.startsWith('/api/')) {
    const [, , , id, action] = pathname.split('/');
    const project = states.get(id);
    if (!project) throw Error('Unexpected API request: ' + pathname);
    if (!action) return json(res, project);
    if (action === 'events') {
      const connections = streams.get(id) || new Set();
      streams.set(id, connections);
      connections.add(res);
      res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-store' });
      res.write(': connected\n\n');
      res.on('close', () => connections.delete(res));
      return;
    }
    if (action === 'preview-access') return json(res, { mode: 'legacy' });
    const version = searchParams.get('version');
    const view = version ? project.versions.find(item => item.id === version) : project;
    if (!view) throw Error('Unexpected version: ' + version);
    if (action === 'preview') {
      const label = builds.get(view.preview_report_id);
      if (!label) throw Error('Page requested before build completed');
      return reply(res, 'text/html; charset=utf-8', '<!doctype html><button onclick="this.textContent=\'交互成功\'">' + label + '</button>');
    }
    if (action === 'files') {
      const name = searchParams.get('path');
      if (!Object.hasOwn(view.manifest || view.files, name)) throw Error('File outside the selected version: ' + name);
      const content = files[name];
      if (searchParams.get('download') === '1') return reply(res, 'application/octet-stream', content);
      return json(res, { content: Buffer.isBuffer(content) ? '二进制产物，请下载查看。' : content });
    }
    throw Error('Unexpected API request: ' + pathname);
  }
  const asset = path.resolve(dist, '.' + (pathname === '/' ? '/index.html' : pathname));
  if (!asset.startsWith(dist + path.sep)) throw Error('Unexpected asset: ' + pathname);
  const mime = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css' }[path.extname(asset)];
  if (!mime) {
    res.writeHead(404).end();
    return;
  }
  reply(res, mime + '; charset=utf-8', await fs.readFile(asset));
}

(async () => {
  await fs.mkdir(out, { recursive: true });
  server = http.createServer((req, res) => {
    serve(req, res).catch(error => {
      errors.push(error.message);
      res.writeHead(404, { 'Content-Type': 'application/json' }).end(JSON.stringify({ error: error.message }));
    });
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  browser = await chromium.launch({ headless: true, ...(process.platform === 'win32' ? { executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe' } : {}) });
  page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, acceptDownloads: true });
  page.setDefaultTimeout(8000);
  page.on('pageerror', error => errors.push(error.message));
  const button = name => page.getByRole('button', { name, exact: true });
  const fileSelect = page.getByLabel('选择文件', { exact: true });
  const versionSelect = page.getByLabel('选择版本', { exact: true });
  const frame = page.frameLocator('iframe[title="生成产物预览"]');
  const open = async id => {
    await button(states.get(id).title).click();
    await page.waitForURL(url => url.searchParams.get('project') === id);
  };
  await page.goto('http://127.0.0.1:' + server.address().port + '/?project=' + live);
  await page.locator('.source').getByText(files['app.js'], { exact: true }).waitFor();
  assert.equal(await fileSelect.inputValue(), 'app.js');
  const newFiles = manifest('app.js', 'index.html');
  save(live, { files: newFiles });
  await page.getByRole('heading', { name: '文件已保存，等待运行检查' }).waitFor();
  assert.equal(await fileSelect.inputValue(), 'index.html');
  save(live, preview('first', 'index.html'));
  await frame.getByRole('button', { name: '首次构建', exact: true }).click();
  await frame.getByRole('button', { name: '交互成功', exact: true }).waitFor();
  pass('new HTML replaces the automatic script preview when its build becomes ready');

  save(live, preview('rebuilt', 'index.html'));
  await frame.getByRole('button', { name: '重新构建', exact: true }).waitFor();
  pass('a new build reloads the same entry even when the manifest is unchanged');

  await fileSelect.selectOption('app.js');
  save(live, { title: '构建状态已更新', ...preview('first', 'index.html') });
  await page.locator('.project-title').filter({ hasText: '构建状态已更新' }).waitFor();
  assert.equal(await fileSelect.inputValue(), 'app.js');
  await page.locator('.source').getByText(files['app.js'], { exact: true }).waitFor();
  save(live, { files: home });
  await frame.getByRole('button', { name: '首次构建', exact: true }).waitFor();
  save(live, { files: newFiles });
  await fileSelect.locator('option[value="app.js"]').waitFor({ state: 'attached' });
  assert.equal(await fileSelect.inputValue(), 'index.html');
  pass('background updates respect a manual choice and clear it when the file disappears');

  await button('版本').click();
  await open(saved);
  await frame.getByRole('button', { name: '已交付看板', exact: true }).waitFor();
  assert.equal(await fileSelect.inputValue(), 'pages/dashboard.html');
  await page.screenshot({ path: path.join(out, 'desktop-preview.png') });
  await versionSelect.selectOption('v1');
  await frame.getByRole('button', { name: '旧版首页', exact: true }).waitFor();
  assert.equal(await fileSelect.inputValue(), 'index.html');
  await button('版本').click();
  await versionSelect.selectOption('v2');
  await fileSelect.selectOption('app.js');
  await page.getByText('已删除', { exact: true }).waitFor();
  await page.locator('.diff .removed').filter({ hasText: files['app.js'] }).waitFor();
  await button('预览').click();
  await frame.getByRole('button', { name: '已交付看板', exact: true }).waitFor();
  assert.equal(await fileSelect.inputValue(), 'pages/dashboard.html');
  await fileSelect.selectOption('index.html');
  await page.locator('.source').getByText(files['index.html'], { exact: true }).waitFor();
  assert.equal(await page.locator('iframe[title="生成产物预览"]').count(), 0);
  pass('opening projects, switching versions and returning from deleted-file diffs choose the actual built entry');

  await open(legacy);
  await frame.getByRole('button', { name: '旧记录的交互页面', exact: true }).waitFor();
  assert.equal(await fileSelect.inputValue(), '');
  assert.equal(await fileSelect.locator('option:checked').textContent(), '网页预览');
  await page.reload();
  await frame.getByRole('button', { name: '旧记录的交互页面', exact: true }).waitFor();
  pass('an old build with multiple HTML files opens under the generic preview label');

  await open(documents);
  await page.getByRole('heading', { name: '分析报告', exact: true }).waitFor();
  await fileSelect.selectOption('sales.csv');
  await page.getByRole('cell', { name: '42', exact: true }).waitFor();
  await fileSelect.selectOption('sales.chart.json');
  await page.getByRole('img', { name: '销售走势，柱状图' }).waitFor();
  await fileSelect.selectOption('slides.pptx');
  await page.getByText('二进制产物，请下载查看。', { exact: true }).waitFor();
  const [download] = await Promise.all([page.waitForEvent('download'), button('下载该文件').click()]);
  assert.deepEqual(await fs.readFile(await download.path()), files['slides.pptx']);
  await open(empty);
  await page.getByRole('heading', { name: '暂无产物', exact: true }).waitFor();
  pass('non-web documents, tables, charts, downloads and empty projects remain usable');

  assert.deepEqual(errors, []);
  await fs.writeFile(path.join(out, 'results.json'), JSON.stringify({ results, fixture: 'local API and event stream, no model calls' }, null, 2));
  console.log('ARTIFACTS ' + out);
})().catch(async error => {
  console.error(error);
  if (page) await page.screenshot({ path: path.join(out, 'failure.png') }).catch(() => {});
  process.exitCode = 1;
}).finally(async () => {
  if (browser) await browser.close();
  if (server) await new Promise(resolve => server.close(resolve));
});
