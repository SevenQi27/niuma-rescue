# 配置参考

当前版本读取根目录 `.env`、可选的 `workspaces.json`，以及管理控制台写入 `state/` 的运行时配置。不存在旧版 `fields.json`、`agents.json` 或 Python 禅道同步脚本配置。

## 配置加载顺序

Niuma 按顺序查找：

1. `NIUMA_ENV` 指定的文件；
2. 当前目录 `.env`；
3. `../.env`；
4. `../../.env`。

已经存在的进程环境变量优先于 `.env`。找到 `.env` 后，其所在目录会成为默认的 `state/`、`worktrees/` 和 `workspaces.json` 根目录。

## 最小配置

只使用一个仓库：

```text
PIPELINE_REPO_PATH=/absolute/path/to/repo
NIUMA_FEISHU_ENABLED=0
```

使用多个工作区时，可以不设置 `PIPELINE_REPO_PATH`，但 `PIPELINE_WORKSPACES_FILE` 指向的 JSON 至少要包含一个有效工作区。

## Web 与本地状态

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `NIUMA_WEB_ENABLED` | `1` | 是否启用任务中心和管理控制台 |
| `NIUMA_WEB_ADDR` | `:8787` | HTTP 监听地址 |
| `PIPELINE_STATE_DIR` | `<root>/state` | SQLite、连接器设置、附件和 Agent 产物目录 |
| `PIPELINE_WORKTREE_BASE` | `<root>/worktrees` | 未被工作区覆盖时的 worktree 根目录 |
| `PIPELINE_WORKSPACES_FILE` | `<root>/workspaces.json` | 多工作区配置 |
| `PIPELINE_AGENT_RUNS_KEEP` | `200` | 保留最近 Agent 调用产物数量 |

Web 当前无鉴权，不能直接暴露公网。

## Agent 与超时

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `PIPELINE_ENGINE_CLARIFY` | `cursor` | 需求澄清 Agent |
| `PIPELINE_ENGINE_CODE` | `cursor` | 需求开发 Agent |
| `PIPELINE_ENGINE_REVIEW` | `gemini` | 需求 Review Agent |
| `PIPELINE_ENGINE_BUG_FIX` | `claude` | Bug 修复 Agent |
| `PIPELINE_ENGINE_BUG_REVIEW` | `codex` | Bug 独立 Review Agent |
| `PIPELINE_TIMEOUT_CLARIFY` | `600` | 澄清超时秒数 |
| `PIPELINE_TIMEOUT_CODE` | `1800` | 开发/测试命令超时秒数 |
| `PIPELINE_TIMEOUT_REVIEW` | `900` | Review 超时秒数 |
| `PIPELINE_TIMEOUT_BUG` | `10800` | 完整 BugGraph 超时秒数 |
| `PIPELINE_BUG_REPAIR_LIMIT` | `2` | 测试或 Review 失败后的最大返修轮数 |
| `PIPELINE_AGENT_RETRIES` | `2` | 单次 Agent 调用重试次数 |
| `PIPELINE_INACTIVITY_TIMEOUT` | `120` | 无输出看门狗；`0` 关闭 |

Bug Agent 仅支持 Claude、Codex、Cursor，且修复与 Review 不能相同。管理控制台保存的覆盖值在重启后继续生效。

## 调度与重试

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `PIPELINE_MAX_CONCURRENCY` | `2` | 全局并发 Agent 上限 |
| `PIPELINE_POLL_INTERVAL` | `900` | 事件驱动之外的兜底扫描间隔 |
| `PIPELINE_EXECUTION_STALE_AFTER` | `600` | 执行锁多久无心跳后可被接管 |
| `PIPELINE_RETRY_BASE_DELAY` | `60` | 指数退避基数秒数 |
| `PIPELINE_FAILURE_LIMIT` | `2` | 达到后进入“已阻塞” |
| `PIPELINE_SETUP_GATE` | `1` | 新任务是否先停在“待选择” |
| `PIPELINE_BATCH_CLARIFY` | `1` | 是否并行澄清多条需求 |
| `PIPELINE_BATCH_DEVELOP` | `1` | inline 模式是否合批开发 |
| `PIPELINE_INLINE_SKIP_GATE` | `1` | inline 开发是否跳过自动测试门 |

## BugGraph

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `PIPELINE_BUG_GRAPH_PYTHON` | 自动发现 `buggraph/.venv` | Python 解释器 |
| `PIPELINE_BUG_GRAPH_DIR` | `<root>/buggraph` | Python 包目录 |
| `PIPELINE_CODE_EXTS` | 内置代码扩展名列表 | 判断是否为产品代码改动 |

BugGraph 会读取工作区 `test_cmd`。当前实现中，空命令会记录 `SKIPPED` 并继续流程；这不等于真实验收通过。生产代码工作区应配置可重复执行的测试命令。

## 飞书（可选）

```text
FEISHU_APP_ID=cli_xxx
FEISHU_APP_SECRET=xxx
PIPELINE_BASE_TOKEN=app_xxx
PIPELINE_TABLE_ID=tblxxx
NIUMA_FEISHU_ENABLED=1
```

四项齐全且未显式设置 `NIUMA_FEISHU_ENABLED` 时，Niuma 会自动启用飞书；否则保持纯本地模式。也可以在管理控制台保存、测试、启停和立即同步。

飞书字段与状态选项见 [feishu-app-setup.md](feishu-app-setup.md)。

## 工作区配置

从样例开始：

```bash
cp workspaces.example.json workspaces.json
```

基础字段：

| 字段 | 值 | 说明 |
| --- | --- | --- |
| `path` | 绝对路径 | 仓库或 SVN 工作副本 |
| `scm` | `git` / `svn` | 版本控制类型 |
| `work_mode` | `inline` / `worktree` | 当前目录或隔离目录 |
| `base` | 如 `origin/main` | 新任务和会话的配置基线 |
| `target_branch` | 如 `main` | `delivery_target=fixed` 时的目标 |
| `worktree_base` | 绝对路径 | 工作区专属 worktree 根目录 |
| `rules_source` | 绝对路径 | 链接 `AGENTS.md` / `.claude/rules` 的来源 checkout |
| `test_cmd` | shell 命令 | 验收门；空值表示跳过 |
| `java_home` | JDK 根目录 | 注入 `JAVA_HOME` 和 `PATH` |
| `maven_home` | Maven 根目录 | 注入 `MAVEN_HOME`、`M2_HOME` 和 `PATH` |
| `push_enabled` | boolean | 是否允许自动 push |
| `pr_enabled` | boolean | 是否允许自动创建 PR/MR |
| `pr_provider` | `none` / `github` / `gitlab` | PR 提供方 |
| `gh_repo` | `owner/repo` | GitHub 目标仓库 |

### Inline

```json
{
  "path": "/absolute/path/to/repo",
  "scm": "git",
  "work_mode": "inline",
  "base": "origin/main",
  "push_enabled": false,
  "pr_enabled": false,
  "test_cmd": ""
}
```

直接修改当前 checkout，不创建分支、不提交、不 push。同一工作区代码阶段串行。原工作区已有脏文件会进入 diff 范围，因此开始前应保持干净。

### 每任务独立 worktree

```json
{
  "path": "/absolute/path/to/repo",
  "scm": "git",
  "work_mode": "worktree",
  "workspace_scope": "task",
  "queue_mode": "parallel",
  "worktree_base": "/absolute/path/to/worktrees",
  "base": "origin/main",
  "target_branch": "main",
  "push_enabled": false,
  "pr_enabled": false,
  "test_cmd": "mvn test"
}
```

每条任务从配置基线创建无 upstream 的独立分支。任务之间可以并行，最终交付各自处理。

### 共享开发会话

```json
{
  "path": "/absolute/path/to/repo",
  "scm": "git",
  "work_mode": "worktree",
  "workspace_scope": "session",
  "queue_mode": "parallel",
  "session_rollover": "daily",
  "delivery_target": "user_choose",
  "track_upstream": false,
  "worktree_base": "/absolute/path/to/worktrees",
  "base": "origin/main",
  "push_enabled": false,
  "pr_enabled": false,
  "test_cmd": "mvn test"
}
```

共享会话字段：

- `workspace_scope`: `task`（默认）或 `session`；
- `queue_mode`: `parallel`（Agent 并行、Git 集成串行）或 `serial`；
- `session_rollover`: `daily`（按日期分会话）或 `manual`（冻结后再创建）；
- `delivery_target`: `fixed` 或 `user_choose`；
- `track_upstream`: push 后是否设置 Niuma 分支的 upstream，默认 `false`。

创建会话时会解析并更新配置基线，然后基于该 SHA 创建无 upstream 的会话分支。每个任务从会话当时 HEAD 创建临时 worktree；Review 通过后才串行 rebase/fast-forward 到会话分支。冲突只暂停当前任务，最终合并目标仍由用户决定。

`session` 仅支持 Git + `worktree`，不能与 inline 或 SVN 组合。

## Push 和 PR

全局 `PIPELINE_PUSH_ENABLED` / `PIPELINE_PR_ENABLED` 仅用于没有工作区文件时的默认工作区。存在 `workspaces.json` 时，以每个工作区的 `push_enabled`、`pr_enabled` 为准。

默认建议关闭自动 push/PR，先验证本地分支、测试和 Review。GitHub 需要已登录 `gh`；GitLab 需要已登录 `glab`。

## 第三方连接器

禅道、Jira、Slack 当前通过管理控制台配置，保存在 `state/integrations.json`。能力和 Webhook 入口见 [third-party-integrations.md](third-party-integrations.md)。
