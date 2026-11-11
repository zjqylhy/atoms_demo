# Atoms Lab

自然语言驱动的多角色工作台，支持 Build / Plan、文档、Node 脚本、网页、CSV/JSON 分析、图表、持久检查点、版本比较恢复和 ZIP 导出。

- [产品调研](docs/product-research.md)
- [需求规格](docs/requirements.md)
- [技术设计与取舍](docs/technical-design.md)
- [实施和验收状态](docs/implementation-status.md)

## 本地启动（Windows）

开发时使用 Go 1.27.1、Node 24.21.0、Docker Desktop，依赖由 go.sum 和 package-lock.json 固定。

```powershell
npm ci --prefix web
npm run build --prefix web
go build -o bin/atoms.exe ./cmd/server
docker build -t atoms-runner:local runner
python scripts/dev_launch.py
```

启动器隐藏输入网关密钥，只校验免费 models 接口后放入子进程环境，不写项目文件。访问 http://127.0.0.1:8090 。模型配置为现有网关的 chat 别名和 JSON 动作适配。本机执行器显式采用 runc，仅用于本机调试，不能将此配置用于公开部署。

一般部署无需 Python 启动器：按 [.env.example](.env.example) 设置进程环境，分别运行 Go 服务与 `node runner/server.cjs`。应用不会自动读取 `.env`，没有 API Key 设置页面。密钥缺失时网页可打开，但任务会明确报错并保留请求。没有运行中的内置模型替身或固定答案回退。

## 验证

```powershell
go test ./...
npm run build --prefix web
npm test --prefix runner
```

最后一项执行真实 Docker/Chromium，不调用模型，验证真实输出、按钮交互、失败检查、只读文件系统、禁外网、无模型凭据、任务去重和死循环终止。测试报告写入忽略的 artifacts/runner-acceptance.json；报告中的 `failed` 是预期被检测出的反例，脚本会断言其状态。

Linux 生产环境必须安装 gVisor runsc；设置 `ATOMS_ACCEPTANCE_RUNTIME=runsc` 后执行同一验收脚本。不得仅凭本机 runc 测试声称生产隔离通过。

## 部署与回滚

已部署到 [atoms.linhengyi.xyz](https://atoms.linhengyi.xyz/)，发布和验证范围见实施记录。`deploy/` 提供独立 systemd 服务，应用使用普通系统账户，私有执行器绑定回环；仅可信执行器持有 Docker 控制权。生成作业在 gVisor 容器中运行，禁网络、禁宿主目录挂载、只读根目录、限制 CPU/内存/进程和时间。

Linux 二进制构建后由 `python scripts/package_release.py` 打包，生成 SHA256 构建清单。`deploy/install.py` 在已审计节点从标准输入接收密钥，写入仓库之外 `/etc/atoms-lab/app.env`（root 0600），systemd 注入进程环境；模型密钥不传给执行器。安装脚本不会配置公开 Caddy 入口，应在隔离和容量验收通过后添加 `deploy/atoms.caddy` 对应站点并校验、热加载。

数据位于 `/opt/atoms_lab/data/atoms.db`，包含源文件、对话、检查点和版本。备份执行 `python3 /opt/atoms_lab/current/deploy/backup.py`，使用 SQLite backup API 并做完整性检查。备份包含访客项目，按私有数据保护；只备份本应用管理的数据，不承诺恢复模型内部状态或外部服务。

回滚应用前停止 atoms-web 和 atoms-runner；根据 release-receipt.json 的 previous 切回已知版本，再启动这两个服务。恢复数据前先额外备份当前数据库；停止服务、将选定备份恢复到 data/atoms.db、处理对应旧 WAL/SHM、恢复 atoms-lab 所有权后启动。不要在服务运行时直接覆盖数据库。不要重启或清理已有 LLM 网关服务。

## 能力边界

固定 Node 运行环境，预装 esbuild、React、Playwright；不动态安装 npm 包，不提供任意宿主 Shell、联网搜索、外部数据库或生成应用独立发布。网页预览不带同源权限，不使用 localStorage/Cookie/网络；应用内存状态随刷新重置，工作台项目状态持久化。Python/Go 等其他语言可交付源码，但不能标为已运行。

匿名工作区由当前浏览器 Cookie 识别，清除 Cookie 后没有账户找回流程。当前是单节点演示系统，限制全局调用、并发和单任务资源；小样本验收不代表高并发生产承诺。检查记录只证明列出的用例；需求完整性仍需人工验收。
