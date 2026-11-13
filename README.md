# Atoms Lab

用自然语言生成文档、代码、网页和数据图表的工作台。Go 管理执行与状态，Eino 调用工具，SQLite 保存项目、账号和应用业务数据，React 提供预览界面；受限执行由独立 gVisor 服务负责。

[演示站](https://atoms.linhengyi.xyz/) · [源码](https://github.com/zjqylhy/atoms_demo) · [本次验收记录](docs/acceptance-completion-validation.md)

**2026-11-13：验收补齐已发布至演示站，版本 `20261113T125035Z`。** 已验证账号跨浏览器恢复、应用进程重启后保留数据、分享撤销及完整备份恢复。发布细节见 [实施记录](docs/implementation-status.md)。

## 直接启动，无需模型 Key

需要 Go 1.27.1、Node.js 24 或更新的兼容版本、npm。命令在仓库根目录执行；Windows 使用 PowerShell，若脚本策略阻止 npm，改用 `npm.cmd`。

```powershell
npm ci --prefix web
npm run build --prefix web
go run ./cmd/server
```

打开 <http://127.0.0.1:8090>。首页有任务看板、阅读清单、复利计算器三个可运行示例。点击创建自己的副本，修改记录后等待“已保存到服务端”，刷新仍可恢复；不需要模型或 Docker。无模型配置时提交 Build 需求会保留失败的原任务，并关联独立演示副本，明确显示“原需求尚未完成”。未批准的 Plan 不创建演示实现。

默认数据库是 `data/atoms.db`，重启复用该目录，不要使用每次启动清空的临时目录。应用不会自动读取 `.env`；配置项见 [.env.example](.env.example)，需由终端、进程管理器或部署环境注入。

## 使用方式

- **Build / Plan：** Build 直接实施；Plan 先产出计划，批准后实施。支持运行中补充要求、停止、继续、重做、文件差异和版本恢复。
- **账号：** 游客无需注册即可使用；用户名为 3～32 位字母、数字或下划线，密码 12～128 个字符。注册接收当前游客项目；登录已有账号时可选择是否接收。账号会话最长 30 天，闲置 7 天失效。退出只撤销当前设备会话，尚无邮件找回密码。
- **跨设备：** 登录同一账号即可读取原项目及已保存业务记录。游客 Cookie 丢失后无法自动恢复未绑定账号的工作区。
- **应用数据：** 当前交付版本使用正式数据；草稿、历史版本和分享页使用试用数据。并发保存发生冲突时重新加载后再修改。代码回退不会清空业务表；格式不兼容时拒绝写入，用户可导出原数据，确认备份后重置。
- **分享：** 固定分享所选交付版本，可撤销。只公开页面及初始示例数据，不提供业务表、聊天、输入原件和执行记录；页面本身写有的信息仍会公开。访客可复制成自己的项目。
- **导出：** ZIP 含源码和已构建 HTML，也可单独下载 HTML 离线打开。默认不带服务端业务记录；数据 JSON 另行导出。导入检查格式和修订，保留最近 3 次导入/重置前备份；备份下载中每项的 `state` 可保存成 JSON，再导入兼容版本。

离线 HTML 不同步服务器。浏览器可能缓存数据，但 `file://` 下缓存行为不统一；页面始终提供 JSON 导入/导出，缓存不可用时提示关闭页面即丢失。请使用 JSON 保存长期记录。

## 真实生成和受限执行

仅从本项目服务端环境变量读取模型配置，不将 Key 写入前端、源码或示例，不读取其他项目的模型配置。

| 变量 | 用途 |
| --- | --- |
| `ATOMS_LLM_BASE_URL / MODEL / API_KEY` | 模型地址、名称、Key；配置不全时示例与演示模式仍可用 |
| `ATOMS_LLM_PROTOCOL` | `chat_completions` 或 `responses` |
| `ATOMS_LLM_TOOL_MODE` | `native` 或 `json`；JSON 适配仅支持 Chat Completions |
| `ATOMS_MAX_OUTPUT_TOKENS / LLM_TPM` | 输出上限和每分钟预留额度；示例沿用现有网关的 81,920 / 1,000,000，需按实际模型能力与预算配置 |
| `ATOMS_BACKUP_LLM_BASE_URL / MODEL / API_KEY` | 可选单一备用，沿用主通道协议和额度 |
| `ATOMS_RUNNER_URL / RUNNER_TOKEN` | 独立执行器地址及共享随机凭据，至少 24 字符 |
| `ATOMS_DATA_DIR / WEB_DIR / ADDR` | 数据目录、前端构建目录、监听地址 |
| `ATOMS_PUBLIC_URL` | 公网 HTTPS 来源，生产必须配置，用于安全 Cookie 与来源校验 |
| `ATOMS_TRUST_PROXY` | 仅在回环 Caddy 等可信反向代理后设为 `1`，使用最后一跳 X-Forwarded-For 限流 |
| `ATOMS_TRUSTED_PROXY_IPS` | 容器代理的精确 IP，逗号分隔；只接受这些来源的最后一跳 X-Forwarded-For。代理 IP 变化时需同步配置，未匹配时按直连来源限流 |

普通 429、网络错误、502/503/504 最多重试 3 次；TPM 沿用最多 2 次等待，二者共享当前动作恢复计数。401/403 和预算不足不原样重试。备用、超时和结果未知的调用都占用原有额度，不将超时视为免费。HTTP 调用保留 300 秒上限，并受本轮剩余时间约束。

公开部署需 Linux、Docker 和已配置的 `runsc`。构建固定执行镜像后启动私有执行器：

```sh
npm ci --prefix runner
docker build -t atoms-runner:local runner
# 先向环境注入 ATOMS_RUNNER_TOKEN，与 Go 服务配置一致。
node runner/server.cjs
```

执行器默认监听 `127.0.0.1:8091`，生成代码不能获取 Docker socket、宿主路径或模型凭据。Windows 可运行工作台及无 Key 示例；完整云端执行使用 Linux gVisor。仅本机开发可显式设置 `ATOMS_RUNNER_LOCAL_DEV=1`、`ATOMS_SANDBOX_RUNTIME=runc`，不得公开部署。Runner 镜像必须包含本版 `app-host.cjs`，标签为 `org.atoms.preview-runtime=1`，旧镜像不能验证新版数据接口。

## 生成应用的数据约定

创建 `atoms.app.json`：

```json
{"api_version":1,"schema_version":1,"initial_state":{"items":[]}}
```

平台自动保存只读 `atoms-runtime.js`，构建时注入全局接口：

```js
const state = await Atoms.data.load();
const result = await Atoms.data.save(nextValue, {
  expectedRevision: state.revision,
  requestId: crypto.randomUUID()
});
// 成功后才能更新本地 revision、显示已保存；未知结果重试用原 requestId。
```

每项目最多 256 KiB JSON 对象，同账号当前应用数据共 8 MiB，最多 30 个项目。初始数据只写入一次。生成页面不接触 Cookie、Key 或数据库，只经隔离消息通道请求宿主保存；事务重新检查会话、项目、当前版本、格式及修订号。保留最近 7 天且最多 1,000 条保存回执。临时输入可放在内存，需要恢复的业务记录必须接入接口。旧应用需单独适配，升级不会恢复以前已丢失的内存内容。

## 验证与维护

```powershell
go test ./... -count=1 -timeout 120s
go vet ./...
npm run build --prefix web
npm ci --prefix runner
npm run test:unit --prefix runner
node scripts/build_catalog.cjs
go build -o tmp/acceptance-server.exe ./cmd/server
node scripts/acceptance_completion.cjs
```

Linux 把构建输出改为 `tmp/acceptance-server`。浏览器验证需 Playwright Chromium，Windows 脚本默认使用标准安装位置的 Chrome。模板脚本重新验证三个示例并更新打包证据，修改 SDK 后必须重建。真实模型验收需显式运行 `scripts/live_app_acceptance.py`，会消费额度；普通测试不调用真实模型。真实生成结果用 `node scripts/verify_live_apps.cjs <验收目录>` 验证。

一致性备份使用 SQLite backup API，包含全部新增表：

```sh
python deploy/backup.py --source data/atoms.db --output-dir backups
```

不要仅复制运行中数据库的主文件而漏掉 WAL。备份/回滚演练：`python scripts/verify_backup.py --source <已停止的验收库> --output-dir <新备份目录>`。发布沿用独立 `/opt/atoms_lab` 目录及数据卷，保留原 owner、文件、版本和调用账本。新账号或业务数据写入后，只能用支持新数据语义的兼容构建回滚；脚本会拒绝自动启动不兼容旧版，不通过旧备份覆盖新写入。

## AI 使用与边界

使用 AI 协助需求梳理、流程设计、代码修改、测试和排错。生成结果仍通过参数校验、运行权限、实际浏览器/执行器检查及版本证据约束；预置模板明确标记，不能冒充自由需求已完成。需求和方案先人工确认，代码与验证完成后再人工验收、发布。

本次验证包括账号换浏览器、隔离后端强制重启、提交后丢响应重试、并发冲突、分享撤销、离线 HTML、模板桌面/390px 检查，以及真实模型输出的持久化与局部迭代。新版生产迁移、公开 HTTPS 使用、应用正常重启恢复、云端 runsc 保存/刷新和生产备份在独立目录恢复启动也已通过，见[逐项证据](docs/acceptance-completion-validation.md)。真实手机国内流量、新版完整真实 Agent 调度链、并行容量和长期稳定性仍需相应验证，不能由这些小样本推断通过。

当前不支持组织权限、找回密码、任意 SQL、外部数据库/接口连接、在线安装依赖和多实例调度。后续优先补复杂长任务交付率、备份恢复体验与容量评估。

[需求补充](docs/acceptance-completion-requirements.md) · [技术方案与流程图](docs/acceptance-completion-design.md) · [原始技术设计](docs/technical-design.md) · [上下文压缩](docs/context-compaction-design.md)
