# niuma — an AI agent orchestration daemon

**Drop a requirement or Bug into Feishu (Lark). Agents clarify, investigate, fix, test and review it.**

niuma is a Go control-plane daemon that turns Feishu IM into the front-end of a
multi-stage AI development pipeline. It listens for requirements over a long
connection, tracks them through a validated state machine backed by Feishu Base
(Bitable), and drives CLI coding agents — **Cursor / Claude Code / Codex /
Gemini** — through clarification → development → review → delivery, with
explicit human gates at every decision point.

Built because I use it daily: it runs my own backlog.

<!-- TODO: 30s demo GIF here — send a requirement in Feishu, watch the agent deliver -->

## How it works

```
Feishu IM ──long conn──▶ niuma (Go daemon)
                          ├─ message/card callbacks → router → Feishu Base records
                          ├─ dispatcher (goroutines) → per-stage CLI agent calls
                          ├─ Bug → Python LangGraph → Codex/Cursor repair loop
                          └─ git worktree / inline edits → review gate → status,
                             logs and alert cards pushed back to Feishu
```

**Pipeline states** (human gates in bold):
requirement pool → **multi-select intake** → auto-clarification (parallel, produces
PRD or follow-up questions) → **PRD confirmation** → dev queue → **batch start** →
agent development → **merge/review** → done. Illegal transitions are rejected by
a whitelist (`ValidTransitions`), so a record can never skip a human gate.

## Features

- **Requirement pool with batch intake** — incoming requirements accumulate;
  one multi-select card confirms which to run
- **Parallel auto-clarification** — each requirement gets a PRD draft or
  follow-up questions before any code is written
- **Batch development** — multiple requirements targeting the same workspace
  are merged into a *single* agent call (fewer context switches, lower token cost)
- **Two execution modes** — `inline` (edit the target repo's working tree,
  human commits) or `worktree` (isolated directory/branch per requirement,
  auto push + PR/MR)
- **Reliability layer** — SQLite-backed execution leases with heartbeats and
  crash recovery, exponential retry with a failure ceiling, hung-agent watchdog,
  blocked-state alert cards
- **Feishu commands** — kanban, health, stats, weekly report, retry, unblock…
- **Bug repair graph** — isolated worktree, investigation, minimal fix, tests,
  independent Codex/Cursor review, bounded repair loop, then human merge

## Design decisions

**Go control plane, small Python graph runtime.** Feishu, locks, Git policy and
agent CLI adapters stay in one Go daemon. Only the stateful Bug repair loop runs
in a local Python LangGraph sidecar, invoked as a subprocess. The two components
communicate through a strict JSON protocol and persist checkpoints in SQLite.

**Feishu Base as the source of truth, SQLite as the execution ledger.**
Requirement status must be visible and editable by humans, so it lives in a
Bitable the whole team can open. What Bitable can't provide — execution locks,
dedup, retry bookkeeping — lives in a local SQLite ledger: a `claim/lease`
table (owner pid, heartbeat, `next_retry_at`, attempt count) makes crashed runs
reclaimable, and an inbox table with a unique `event_key` deduplicates Feishu
event redelivery.

**A validated state machine instead of free-form status.** Every transition is
checked against a whitelist; anything else is an error. Combined with the five
human gates, this is what makes an *autonomous* pipeline safe to point at real
repositories: the machine can only move along edges a human already approved.

**Heterogeneous agents behind one interface.** Each CLI agent speaks a
different dialect (Cursor emits `stream-json` events; others emit raw text) and
fails differently (auth expiry, rate limits, workspace-trust prompts). A `sink`
abstraction normalizes output streams, and per-engine error-marker tables
classify failures into *retryable* vs *needs-human*, so retry policy is uniform
across engines.

**Three-level concurrency control.** A global semaphore caps parallel agent
runs; a git mutex serializes worktree surgery on the shared base repo; lazy
per-workspace locks ensure inline mode never runs two batches in one working
tree. Coarse enough to reason about, fine enough to keep unrelated workspaces
fully parallel.

**Humans decide, agents execute.** Every irreversible step — which requirements
to run, whether a PRD is right, when a batch starts, whether the diff merges —
is a card in Feishu waiting for a tap. The pipeline's throughput comes from
automating everything *between* those taps, not from removing them.

## Quick start

```bash
cd go
GOPROXY=https://goproxy.cn,direct go build -o niuma .
cd ../buggraph
python3 -m venv .venv && .venv/bin/python -m pip install -e .
# reuse .env from the parent dir (Feishu credentials / Base / repo paths), see .env.example
./niuma
```

Full setup from a fresh clone (prerequisites, Feishu Base fields and status
options, running as a service) → [go/README.md](go/README.md).
Feishu app permissions and event subscriptions → [docs/feishu-app-setup.md](docs/feishu-app-setup.md).
Multi-workspace / SCM config → `workspaces.example.json`.

## License

See [LICENSE](LICENSE).

---

# 牛马自救中心 (niuma) · 中文说明

把 Bug 或需求丢进局域网页面，也可以选择接入飞书、禅道、Jira 或 Slack，让 Agent 替你加班。

本地 SQLite 是任务主库和状态机；第三方平台是可选的录入、同步和通知连接器。本地 Go 常驻进程调度 Cursor / Claude / Codex / Gemini。
需求走澄清 → 开发 → Review；Bug 由 Python LangGraph 编排调查 → 修复 → 测试 → 独立 Review → 有界返修。

> **控制面已是 Go 实现**；Bug 的有状态返修环由 [`buggraph/`](buggraph/) 中的 Python LangGraph
> sidecar 承担。早期全 Python 版只保留在 git 历史中。

## 它能做什么

- 局域网页面直接收 Bug，本地保存并进入任务池；飞书可选接入
- **需求池多选**：发来的需求先攒在「待选择」，一张多选卡片让你勾选要做的几条、一次确认（`PIPELINE_SETUP_GATE=0` 可关，直接开跑）
- 自动澄清（产出 PRD 或追问，多条**并行**）、人工确认后进「待开发」队列
- **合批开发**：点「开始开发本批」把同工作区的多条需求合并成**一次** Agent 调用，开发完停在「待合并」由人决定 Review
- 飞书命令：`需求池` `开始开发` `看板` `状态` `配置` `健康` `统计` `周报` `重试` `解除阻塞` …
- **Bug 最小链路**：`Bug@codex：现象`（或 `@cursor`）→ 强制独立 worktree → 测试通过 → 另一 Agent Review → 人工合并；测试失败会返修并在达到上限后阻塞
- **局域网任务中心**：页面按 `Bug / 需求` 页签分流；Bug 走调查修复链路，需求走澄清、人工确认、开发、Review 和人工合并；两边都支持附件、修改、进度与归档
- **管理控制台**：管理本地任务、工作区、Agent 和第三方连接器；默认监听 `:8787`，无需登录
- **可选飞书同步**：运行时启停、测试连接、立即同步；飞书异常不会阻塞本地任务或 Agent 流水线
- **第三方集成中心 MVP**：管理禅道、Jira、Slack 配置，测试远端 Token；禅道/Jira Webhook 经过 Token 校验和幂等去重后进入本地事件队列
- agent 瞬时网络错自动重试、卡死看门狗、阻塞主动告警卡片
- 默认 **inline 模式**（所有需求在目标仓库当前工作树上改、人工决定提交）；也可按工作区切 `worktree`（各自独立目录/分支、自动 push/PR/MR）。本地 SQLite 记录执行锁/去重/重试

## 人工卡点

`待选择`（需求池多选）· `待回答`（补充信息）· `待确认`（确认 PRD）· `待开发`（攒批后点「开始开发本批」）· `待合并`（做 Review / 确认已人工合并并完成；Bug 会校验本地 Git 合并证据）。

## 配置

复制 `.env.example` 为 `.env` 并配置目标仓库。飞书凭据和 Base 标识均为可选，也可以之后在 `/manage` 管理控制台中填写。
多工作区 / SCM 见 `workspaces.example.json`。飞书应用权限与事件订阅见 [docs/feishu-app-setup.md](docs/feishu-app-setup.md)。
第三方连接器当前能力和配置方式见 [docs/third-party-integrations.md](docs/third-party-integrations.md)。

> 注：`docs/` 下其余文档为早期 Python 版部署说明，正在迁移；以 [go/README.md](go/README.md) 为准。
