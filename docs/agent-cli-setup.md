# Agent CLI 配置

Niuma 直接启动本机 Agent CLI，prompt 通过 stdin 传入。当前内置命令定义在 `go/config.go`，执行、会话恢复、超时和产物归档在 `go/agent.go`。

## 支持范围

| Agent | 需求澄清/开发/Review | Bug 修复/Review | 会话续接 |
| --- | --- | --- | --- |
| Claude Code | 支持 | 支持 | `--resume` |
| Codex | 支持 | 支持 | `codex exec resume` |
| Cursor Agent | 支持 | 支持 | 当前按单次调用 |
| Gemini CLI | 支持 | 不用于 Bug 独立修复对 | 当前按单次调用 |

Bug 修复与 Review 必须由两个不同的 Agent 完成。默认是 Claude 修复、Codex Review，可在 `.env` 或管理控制台修改。

## 内置无头命令

```text
claude: claude -p --output-format text
codex:  codex exec -
cursor: cursor-agent --print --force --trust --model auto --output-format text
gemini: gemini --skip-trust --approval-mode yolo -p " "
```

Claude 在代码写入阶段会额外使用 `--permission-mode acceptEdits`。这只允许 Claude 编辑文件，并不代表所有 Bash、构建或测试命令都会自动获批；真正的验收仍由 Niuma 的 `test_cmd` 独立执行。

## 安装后先手工验证

至少安装一个计划使用的 CLI，并完成官方登录或 API 凭据配置。请在目标仓库目录中逐个验证：

```bash
printf '只回复 ok\n' | claude -p --output-format text
printf '只回复 ok\n' | codex exec -
printf '只回复 ok\n' | cursor-agent --print --force --trust --model auto --output-format text
gemini --skip-trust --approval-mode yolo -p '只回复 ok'
```

不要只检查 `--version`。需要确认无头调用能返回内容、不会等待交互输入，并且服务用户能够访问目标仓库。

## PATH 与常驻服务

交互终端和 launchd/systemd/NSSM 的环境通常不同。先找到真实可执行文件：

```bash
command -v claude
command -v codex
command -v cursor-agent
command -v gemini
```

再把对应目录加入常驻服务的 `PATH`。如果使用工作区级 Java/Maven 配置，Niuma 还会为 Agent、BugGraph 和测试命令注入：

```text
JAVA_HOME
MAVEN_HOME
M2_HOME
PATH=<java_home>/bin:<maven_home>/bin:...
```

管理页保存工作区时会检查 `bin/java` 和 `bin/mvn` 是否存在且可执行。

## 会话与上下文

Niuma 会记录 Claude/Codex 返回的 session ID，并在同一任务的返修轮次中恢复会话。该机制用于保持同一 Bug 的上下文，不应跨不相关任务共享。

每次调用的元数据和输出保存在 `state/agent-runs/`。排查质量问题时应按同一个任务查看完整调用链，而不是只看最后一次回答。

## 环境变量清理

启动 Agent 前，Niuma 会清理可能污染 Claude 认证或嵌套运行的部分变量，例如 `ANTHROPIC_BASE_URL`、`ANTHROPIC_AUTH_TOKEN`、`CLAUDECODE` 以及相关前缀。若你有意使用自定义网关，需要先确认当前代码的清理策略是否符合部署方式，不要把私密 Token 写进仓库配置。

## 超时和看门狗

常用配置：

```text
PIPELINE_TIMEOUT_CLARIFY=600
PIPELINE_TIMEOUT_CODE=1800
PIPELINE_TIMEOUT_REVIEW=900
PIPELINE_TIMEOUT_BUG=10800
PIPELINE_INACTIVITY_TIMEOUT=120
PIPELINE_AGENT_RETRIES=2
```

无输出超时只能说明进程在一段时间内没有可观察输出，并不能证明 Agent 真的死锁。调整前先查看 `state/agent-runs/` 和服务日志，确认终止的是完整子进程树。

## 增加新 Agent

当前版本没有 `agents.json` 动态命令覆盖。增加新 Agent 需要修改：

1. `go/config.go` 中的命令与别名；
2. `go/agent.go` 中的输出解析、会话恢复和错误分类；
3. 对应单元测试；
4. 管理页/任务表单的可选项（如果应暴露给用户）。

新增后至少验证：正常输出、空输出、认证失败、超时、进程终止、会话续接、写权限和产物归档。
