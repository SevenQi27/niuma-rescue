# 快速开始

这份文档以当前 Go 控制面为准。旧版 `src/listener.py` / `src/dispatcher.py` 已不再是运行入口。

## 1. 前置条件

- Go 1.25.5+
- Git
- 至少一个已登录、可无头运行的 Agent CLI
- Python 3.10+（只有 Bug 调查/修复流程需要）
- 一个本地代码仓库；Git 工作区通常应能访问 `origin/main`

飞书不是启动条件。只使用网页和本地 SQLite 时，不需要任何飞书配置。

## 2. 构建 Go 控制面

```bash
git clone <your-repo-url> agent-pipeline
cd agent-pipeline/go
GOPROXY=https://goproxy.cn,direct go build -o niuma .
```

## 3. 配置最小本地模式

```bash
cd ..
cp .env.example .env
```

编辑 `.env`，最少设置一个目标仓库：

```text
PIPELINE_REPO_PATH=/absolute/path/to/your/repo
NIUMA_FEISHU_ENABLED=0
```

如果需要多个仓库或共享开发会话，复制并修改工作区配置：

```bash
cp workspaces.example.json workspaces.json
```

`workspaces.example.json` 中的路径都是占位符，必须换成真实绝对路径。配置字段见 [config-reference.md](config-reference.md)。

## 4. 安装 BugGraph（需要修 Bug 时）

```bash
cd buggraph
python3 -m venv .venv
.venv/bin/python -m pip install -e .
.venv/bin/python -m unittest discover -s tests -v
cd ../go
```

普通需求流程不依赖 Python；未安装 BugGraph 时不要启动 Bug 修复任务。

## 5. 启动并验证

```bash
cd go
./niuma
```

日志应包含：

```text
Web 控制台启动 · http://...:8787
niuma 启动 · 数据源=本地
扫描 0 条记录，待处理 0 条
```

然后打开：

```text
http://localhost:8787
```

验证顺序：

1. 在 Bug 或需求页签保存一条任务，但先不要启动 Agent；
2. 打开管理控制台，确认任务总数、工作区和默认 Agent 正确；
3. 在目标仓库里手动验证准备使用的 Agent CLI；
4. 给工作区配置真实 `test_cmd`；
5. 再启动一条低风险演示任务，检查进度、Agent 产物和人工卡点。

## 6. 可选启用飞书

在 `.env` 填齐以下四项，或者在管理控制台中保存：

```text
FEISHU_APP_ID
FEISHU_APP_SECRET
PIPELINE_BASE_TOKEN
PIPELINE_TABLE_ID
NIUMA_FEISHU_ENABLED=1
```

飞书权限、字段和状态选项见 [feishu-app-setup.md](feishu-app-setup.md)。启用前可在管理控制台测试连接；飞书异常不会删除本地任务，也不会让本地页面失效。

## 7. 常见问题

| 症状 | 检查 |
| --- | --- |
| 启动时报缺少 `PIPELINE_REPO_PATH` | 设置默认仓库，或提供至少一个有效 `workspaces.json` 工作区 |
| 页面打不开 | 检查 `NIUMA_WEB_ENABLED`、`NIUMA_WEB_ADDR` 和端口占用 |
| Agent 在终端可用、服务里找不到 | 常驻服务的 `PATH` 未包含 Agent CLI；见 [agent-cli-setup.md](agent-cli-setup.md) |
| Bug 到测试阶段失败 | 检查工作区 `test_cmd`、JDK/Maven 等工具链路径和真实测试输出 |
| Bug 停在“等待代码区域” | 另一个运行中 Bug 已登记重叠文件；等待前置 Review 或在管理页处理前置任务 |
| 共享会话集成冲突 | 只会暂停当前任务；先解决该任务分支冲突，不要直接修改会话 worktree |
| 页面提示无鉴权 | 这是当前边界，只能运行在可信局域网，不能直接暴露公网 |

常驻部署和恢复操作见 [operations.md](operations.md)，Windows 见 [windows-install.md](windows-install.md)。
