# niuma — an AI agent orchestration daemon

**Drop a requirement into Feishu (Lark). Agents clarify, develop, test and deliver it.**

niuma is a single-binary Go daemon that turns Feishu IM into the front-end of a
multi-stage AI development pipeline. It listens for requirements over a long
connection, tracks them through a validated state machine backed by Feishu Base
(Bitable), and drives CLI coding agents — **Cursor / Claude Code / Codex /
Gemini** — through clarification → development → review → delivery, with
explicit human gates at every decision point.

Built because I use it daily: it runs my own backlog.

<!-- TODO: 30s demo GIF here — send a requirement in Feishu, watch the agent deliver -->

## How it works

```
Feishu IM ──long conn──▶ niuma (single process)
                          ├─ message/card callbacks → router → Feishu Base records
                          ├─ dispatcher (goroutines) → per-stage CLI agent calls
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
- **ZenTao integration** — import bugs from ZenTao (incl. v12 token auth) into
  the same pipeline

## Design decisions

**Single process, single static binary.** The orchestrator's job is I/O
coordination, not computation — one Go process with goroutines replaces the
earlier multi-service Python version. Deployment is `scp + run`; state lives in
SQLite and Feishu Base, so the binary itself is disposable.

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

把需求丢进飞书，让 Agent 替你加班。

飞书多维表格做需求池 + 状态机，本地一个常驻进程监听消息、调度 Cursor / Claude / Codex / Gemini
等 CLI Agent 跑完需求澄清 → 开发 → 测试 → Review → 交付流转。

> **本仓库已是 Go 实现**（单进程、单静态二进制）。早期 Python 版的完整历史保留在 `main` 分支与
> git 历史中；当前代码全部在 [`go/`](go/)。

## 它能做什么

- 飞书私聊机器人收需求 → 写入多维表格，进「需求池」
- **需求池多选**：发来的需求先攒在「待选择」，一张多选卡片让你勾选要做的几条、一次确认（`PIPELINE_SETUP_GATE=0` 可关，直接开跑）
- 自动澄清（产出 PRD 或追问，多条**并行**）、人工确认后进「待开发」队列
- **合批开发**：点「开始开发本批」把同工作区的多条需求合并成**一次** Agent 调用，开发完停在「待合并」由人决定 Review
- 飞书命令：`需求池` `开始开发` `看板` `状态` `配置` `健康` `统计` `周报` `重试` `解除阻塞` …
- agent 瞬时网络错自动重试、卡死看门狗、阻塞主动告警卡片
- 默认 **inline 模式**（所有需求在目标仓库当前工作树上改、人工决定提交）；也可按工作区切 `worktree`（各自独立目录/分支、自动 push/PR/MR）。本地 SQLite 记录执行锁/去重/重试

## 人工卡点

`待选择`（需求池多选）· `待回答`（补充信息）· `待确认`（确认 PRD）· `待开发`（攒批后点「开始开发本批」）· `待合并`（做 Review / 标记完成）。

## 配置

复制 `.env.example` 为 `.env` 填好飞书凭据、Base 标识、目标仓库路径。
多工作区 / SCM 见 `workspaces.example.json`。飞书应用权限与事件订阅见 [docs/feishu-app-setup.md](docs/feishu-app-setup.md)。

> 注：`docs/` 下其余文档为早期 Python 版部署说明，正在迁移；以 [go/README.md](go/README.md) 为准。
