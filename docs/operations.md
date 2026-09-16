# 运维与恢复

当前版本只有一个 Go 常驻进程：它同时提供 Web、任务调度、飞书长连接、连接器同步和 Agent 子进程管理。Bug 运行时会按需启动本地 Python LangGraph 子进程。

## 进程与入口

前台启动：

```bash
cd go
./niuma
```

默认页面：

```text
http://localhost:8787
```

独立流程问询页：

```text
http://localhost:8787/inquiry
```

问询只使用 Codex，并在后端强制要求唯一的纯数字
`procId` 查询参数。它以只读、临时会话模式异步执行；未通过参数校验的请求不会启动 Codex。
问询记录持久化在 `STATE_DIR/inquiries.json`，页面展示最近 200 条。刷新页面不会丢失记录；
服务重启时仍未完成的问询会保留下来并明确标记为失败，方便重新提交。

问询中的 `mysql-prod` 会被替换为 Niuma 内置的生产只读 MCP，并只对这个受限包装器
预先授权。它只接受单条 `SELECT`、`SHOW`、`DESCRIBE`、`EXPLAIN` 或只读 `WITH`，
拒绝 DML、DDL、锁、文件访问和多语句，并在 MySQL `READ ONLY` 事务中执行。
不要把全局 `mysql-prod` 的通用 `execute_sql` 改成自动批准；原 MCP 支持并自动提交写 SQL。
只读包装器会先使用 `PATH` 中的 `mysql`，再探测 Homebrew Intel/Apple Silicon 和常见系统路径；
特殊安装位置可通过 `MYSQL_CLIENT_PATH` 指定。

管理控制台可以完成：

- 查看任务总数、状态、当前运行和服务运行时间；
- 停止、重试、重新澄清、确认完成或归档任务；
- 配置 Bug 修复/Review Agent 和超时；
- 管理工作区及 JDK/Maven 工具链；
- 查看和冻结共享开发会话；
- 启停飞书、测试连接和立即同步；
- 管理禅道、Jira、Slack 连接器并查看最近事件。

当前页面没有身份认证，只允许部署在可信局域网。
`procId` 只限定问询对象，并不替代身份认证。

## 日志

前台运行时日志写到 stdout/stderr。常驻服务应把两者重定向到：

```text
logs/niuma.log
```

常用查看方式：

```bash
tail -f logs/niuma.log
```

每次 Agent 调用的 prompt、标准输出、错误输出和元数据保存在：

```text
state/agent-runs/
```

只看页面摘要不能替代原始产物；排查“Agent 理解错了”时应同时核对原始 Bug、调查结论、实际 diff、测试输出和 Review 输出。

## 运行时数据

默认 `PIPELINE_STATE_DIR=state`，主要内容包括：

- `niuma.sqlite3`：本地任务、执行锁、连接器事件和共享开发会话；
- `buggraph.sqlite3`：Bug LangGraph checkpoint；
- `integration.json`：飞书运行时设置；
- `integrations.json`：禅道/Jira/Slack 设置，权限为 `0600`；
- `pipeline-settings.json`：管理页保存的 Bug Agent 与超时覆盖；
- `agent-runs/`：Agent 调用产物；
- Bug 附件和进度文件。

这些都是运行时私有数据，不应提交到 Git。

备份时先停止 Niuma，再复制整个 `state/` 目录；恢复时也应在进程停止后替换，避免复制到不一致的 SQLite/WAL 状态。

## 任务恢复

优先在任务页面或管理控制台操作：

- **停止**：终止当前任务并清理执行状态；
- **重试**：清理失败/阻塞状态并重新调度；
- **重新澄清**：让需求回到澄清阶段；
- **确认完成**：只在代码已人工交付后使用；Bug 会检查目标分支是否包含修复提交或等价补丁；
- **归档**：只用于不再处理的非运行任务。

不要因为一次重试重新跑起来，就把原问题当成已经修复。任务完成仍需业务验收证据。

## Bug 协调状态

Bug 调查后会登记预计修改文件。若与运行中任务重叠：

1. 后来的任务进入“等待代码区域”；
2. 它保留调查结果，但不写代码；
3. 前置任务 Review 通过后，后续任务基于前置分支继续；
4. 若前置任务失败或被终止，人工检查后再决定重试或重新调查。

页面中的相似度只是提示；文件占用、前置分支和实际 Git 状态才是调度依据。

## 共享开发会话

共享会话包含一个集成 worktree 和多个任务 worktree：

- Agent 可以在不同任务 worktree 中并行工作；
- Review 通过后，Git 集成动作串行执行；
- 会话 HEAD 已前进时，当前任务先 rebase 到最新会话分支；
- 冲突只标记当前任务为 `integration_conflict`，并自动 abort rebase；
- 会话 worktree 必须保持干净；
- 所有任务都已集成后，才能在管理页冻结会话。

冻结不会合并到 `main`、`test` 或其他目标分支。最终目标由用户决定。

## 飞书命令

启用飞书后可在机器人私聊使用：

```text
指令 / 帮助
看板 / 状态 / 需求池
开始澄清 / 开始开发
配置 / 健康 / 统计 / 周报 / 诊断
重试 / 清锁 / 重新澄清 / 解除阻塞 / 完成
切换Agent <agent>
切换工作区 <workspace>
```

飞书关闭或断线时，使用 Web 管理控制台继续处理；不要依赖连接器作为唯一任务主库。

## 服务管理

macOS launchd：

```bash
launchctl kickstart -k gui/$(id -u)/com.niuma.rescue
launchctl print gui/$(id -u)/com.niuma.rescue
```

Linux systemd user service：

```bash
systemctl --user restart niuma
systemctl --user status niuma
journalctl --user -u niuma -f
```

Windows NSSM：

```cmd
nssm restart niuma
nssm status niuma
```

重建二进制后必须重启常驻服务，否则页面和运行代码仍然是旧版本。

## 故障定位

### 页面可以打开，但任务不动

- 查看管理控制台的“当前运行”和任务状态；
- 查看 `logs/niuma.log`；
- 检查 Agent CLI 是否能在相同服务用户和 `PATH` 下运行；
- 检查执行锁是否仍有心跳；
- 检查任务是否停在人工作业门，而不是误判成卡死。

### Agent 无输出后被阻塞

- 查看 Agent 原始 stderr 和最后输出时间；
- 对照 `PIPELINE_INACTIVITY_TIMEOUT`；
- 确认被终止的是完整进程树，而不只是直接父进程；
- 除非有业务结果证据，否则一次成功重试不等于修复看门狗问题。

### 测试显示跳过

工作区 `test_cmd` 为空时不会执行真实验收。为必须验证的代码配置可重复运行的命令，并确认所需 JDK/Maven/Node/Python 环境已注入。

### 飞书没有同步

- 在管理页确认连接器已启用；
- 使用“测试连接”和“立即同步”；
- 核对 Base 字段和状态选项；
- 确认同一个飞书应用没有第二个长连接消费者；
- 即使同步失败，也先以本地 SQLite 任务状态为准。
