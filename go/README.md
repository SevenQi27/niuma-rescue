# niuma（Go 实现）

把需求丢进飞书，Agent 替你澄清 → 开发 → Review → 交付。
listener + dispatcher 合并为一个 Go 常驻进程，goroutine 并发；仅 Bug 返修环会启动本地 Python LangGraph 子进程。

## 局域网 Bug 收件台

服务默认同时监听 `:8787`，局域网用户访问 `http://<这台机器的局域网 IP>:8787` 后可以：

- 新建 Bug，选择代码工作区和 Codex/Cursor 修复 Agent；
- 上传图片、PDF、Excel 或 CSV 附件；图片可预览、文档可打开或下载，Agent 会在 Bug 档案中读取附件；
- 在「待选择 / 待回答 / 已阻塞」阶段修改描述和补充信息；
- 确认后启动既有的 worktree → 修复 → 测试 → 独立 Review → 人工合并路线；
- 查看飞书多维表格中同一批 Bug 的状态和执行日志；
- 在 Bug 卡片内展开 AI 执行过程，实时查看各阶段状态及 Agent 调查、修复、验证和 Review 结果。

页面和 API 无需登录，飞书凭据仍只保留在服务端，不会发送到浏览器。此入口使用普通 HTTP，只适合可信局域网，不应直接暴露公网；如需公网访问，应在前面增加 HTTPS 和身份认证。`NIUMA_WEB_ENABLED=0` 可完全关闭。

---

## 前置条件

- **Go 1.25+**（仅构建时需要）
- **Python 3.10+**（仅 Bug LangGraph 流水线需要；普通需求仍只走 Go）
- **Git**，以及一个目标代码仓库
- 至少一个**可无头运行的 Agent CLI**，并已登录可用：
  - 默认 `cursor-agent`（澄清/开发/Review 默认都用 cursor）
  - 也支持 `claude` / `codex` / `gemini`，按需在 `.env` 或飞书里切换
- 一个**飞书自建应用**（开通多维表格读写 + IM 发消息 + 长连接接收私聊），
  详见 [../docs/feishu-app-setup.md](../docs/feishu-app-setup.md)

---

## 从零运行（克隆后四步）

### 1) 克隆 & 构建

```bash
git clone <your-repo-url> agent-pipeline
cd agent-pipeline/go
GOPROXY=https://goproxy.cn,direct go build -o niuma .
cd ../buggraph
python3 -m venv .venv
.venv/bin/python -m pip install -e .
cd ../go
```

> 交叉编译分发 Go 控制面（普通需求链路仍是单文件；Bug 链路还需部署 `buggraph/` 与 Python 环境）：
> ```bash
> GOOS=linux  GOARCH=amd64 go build -o niuma-linux .
> GOOS=darwin GOARCH=arm64 go build -o niuma-mac   .
> ```

### 2) 配置 `.env`

`.env` 放在**仓库根**（即 `go/` 的上一层），程序会自动在 `./.env` / `../.env` 里找：

```bash
cp ../.env.example ../.env
# 编辑 ../.env，至少填这 5 个必填项：
#   FEISHU_APP_ID / FEISHU_APP_SECRET
#   PIPELINE_BASE_TOKEN / PIPELINE_TABLE_ID   ← 来自第 3 步的多维表格 URL
#   PIPELINE_REPO_PATH                         ← 目标 git 仓库绝对路径
```

常用可选项：

| 环境变量 | 默认 | 说明 |
|---|---|---|
| `PIPELINE_ENGINE_CLARIFY` / `_CODE` / `_REVIEW` | `cursor` | 各阶段默认 Agent |
| `PIPELINE_MAX_CONCURRENCY` | `2` | 并发上限（澄清并行、不同工作区并行各占一个名额） |
| `PIPELINE_SETUP_GATE` | `1` | 开=新需求先进「需求池」等人勾选；关=直接开跑 |
| `PIPELINE_BATCH_DEVELOP` | `1` | inline 下多需求合批成一次开发调用 |
| `PIPELINE_INLINE_SKIP_GATE` | `1` | inline 开发跳过自动测试门（由人决定 Review） |
| `PIPELINE_TEST_CMD` | 空 | 验收门 shell，exit 0 视为通过；空则不跑 |
| `PIPELINE_POLL_INTERVAL` | `900` | 兜底轮询秒数（主要靠事件驱动） |
| `PIPELINE_AGENT_RUNS_KEEP` | `200` | `state/agent-runs/` 保留最近 N 次调用产物 |
| `PIPELINE_ENGINE_BUG_FIX` / `_BUG_REVIEW` | `codex` / `cursor` | Bug 修复与独立 Review Agent（必须不同） |
| `PIPELINE_BUG_REPAIR_LIMIT` | `2` | 测试失败或 Review FAIL 后最多返修总轮数 |
| `PIPELINE_BUG_GRAPH_PYTHON` | 自动发现 `buggraph/.venv` | LangGraph Python 解释器 |

多工作区 / worktree 模式 / SCM 见 [../workspaces.example.json](../workspaces.example.json) 与 [../docs/config-reference.md](../docs/config-reference.md)。

### 3) 准备飞书多维表格（Base）

新建一张多维表格，加好这些字段，并把表格 URL 里的 `app_token` / `table_id` 填进 `.env`
的 `PIPELINE_BASE_TOKEN` / `PIPELINE_TABLE_ID`。字段名必须**完全一致**：

- 文本/单选：`需求标题` `需求描述` `澄清记录` `PRD` `分支PR链接` `执行日志` `失败次数` `提需求人` `会话ID` `工作区`
- Agent 单选：`执行Agent` `澄清Agent` `开发Agent` `ReviewAgent`
- 任务类型单选：`任务类型`，选项为 `需求` / `Bug`
- **`状态`（单选）必须包含这些选项**（名字完全一致，少一个会导致写入被飞书拒绝）：

  ```
  待选择 · 待澄清 · 待回答 · 待确认 · 待开发 · 开发中 · Bug处理中 · Review中 · 待合并 · 完成 · 已阻塞
  ```

已有表可先检查再幂等补齐：

```bash
./niuma schema-check-bug
./niuma schema-ensure-bug
```

详见 [../docs/feishu-app-setup.md](../docs/feishu-app-setup.md)。

### 4) 运行

前台跑（先这样验证）：

```bash
./niuma
```

看到「连上 `wss://msg-frontier.feishu.cn`」+「扫描 N 条记录」即正常。

---

## 装成常驻服务

### macOS（launchd）

把下面存成 `~/Library/LaunchAgents/com.niuma.rescue.plist`，**改掉两处路径**为你的实际路径：

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>            <string>com.niuma.rescue</string>
  <key>ProgramArguments</key> <array><string>/ABS/PATH/agent-pipeline/go/niuma</string></array>
  <key>WorkingDirectory</key> <string>/ABS/PATH/agent-pipeline/go</string>
  <key>EnvironmentVariables</key>
  <dict><key>PATH</key><string>/Users/你/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string></dict>
  <key>RunAtLoad</key> <true/>
  <key>KeepAlive</key> <true/>
  <key>StandardOutPath</key>   <string>/ABS/PATH/agent-pipeline/logs/niuma.log</string>
  <key>StandardErrorPath</key> <string>/ABS/PATH/agent-pipeline/logs/niuma.log</string>
</dict>
</plist>
```

> `PATH` 必须包含你的 Agent CLI 所在目录（如 `cursor-agent` 在 `~/.local/bin`），否则常驻进程找不到命令。

```bash
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.niuma.rescue.plist
launchctl kickstart -k gui/$(id -u)/com.niuma.rescue   # 重新构建后用它重启
tail -f logs/niuma.log
```

### Linux（systemd，用户级）

`~/.config/systemd/user/niuma.service`：

```ini
[Unit]
Description=niuma feishu pipeline
[Service]
WorkingDirectory=/ABS/PATH/agent-pipeline/go
ExecStart=/ABS/PATH/agent-pipeline/go/niuma
Environment=PATH=/home/你/.local/bin:/usr/local/bin:/usr/bin:/bin
Restart=always
[Install]
WantedBy=default.target
```

```bash
systemctl --user enable --now niuma
journalctl --user -u niuma -f
```

### Windows

`niuma.exe` 是控制台程序，直接跑会**一直挂一个 cmd 窗口**。推荐装成 Windows 服务（[NSSM](https://nssm.cc/)），
后台无窗口、开机自启、崩溃自愈：

```cmd
nssm install niuma "C:\path\agent-pipeline\go\niuma.exe"
nssm set niuma AppDirectory "C:\path\agent-pipeline\go"
nssm set niuma AppStdout "C:\path\agent-pipeline\logs\niuma.log"
nssm set niuma AppStderr "C:\path\agent-pipeline\logs\niuma.log"
nssm set niuma AppEnvironmentExtra "PATH=C:\path\to\agent-cli;%PATH%"
nssm start niuma          :: 重新 build 后用 nssm restart niuma
```

不想装服务也可以编译成无窗口程序：`go build -ldflags="-H=windowsgui" -o niuma.exe .`。
完整三种方式（NSSM / 无窗口编译 / 任务计划程序）见 [../docs/windows-install.md](../docs/windows-install.md)。

---

## 工作流程（默认 inline 模式）

```
飞书发「需求：<一句话>」
   └─ 进「需求池(待选择)」，回一张多选卡片
        └─ 勾选要做的几条 → 「✅ 确认开始」
             └─ 待澄清（多条并行澄清）→ 待回答(补充) / 待确认(确认 PRD)
                  └─ 确认 → 进「待开发」队列（攒着，不自动跑）
                       └─ 点「🚀 开始开发本批」→ 开发中（同工作区合批成一次执行）
                            └─ 待合并 → 「🔍 做 Review」或「确认已人工合并并完成」
```

Bug 走独立最小链路：

```text
飞书发「Bug@codex：<现象>」（或 @cursor）
   └─ 待选择 → 强制创建 BUG worktree
        └─ 修复 Agent 调查 → 修复 → test_cmd
             └─ 另一个 Agent 只读 Review
                  ├─ FAIL → 返回修复（最多 2 轮）
                  ├─ NEEDS_INPUT → 待回答
                  └─ PASS → 本地提交/按配置推送 → 待人工合并
测试失败 ──→ 返回修复；达到返修上限后阻塞，不进入 Review
Bug 完成 ──→ 校验目标分支已包含 Bug 提交或等价补丁
```

Bug 流水线不会自动建 PR、不会自动合并，也不会使用 inline 工作树。

- **inline**（默认）：所有需求在 `PIPELINE_REPO_PATH` 的**当前工作树**上改动，不自动提交，由人决定。
- 想每条需求隔离独立目录/分支、自动 push/PR/MR：在 `workspaces.json` 用 `work_mode: "worktree"`，并可用 `worktree_base` 指定这些目录的统一位置；新分支从最新远端基线创建且不跟踪主线。若规则未提交到主线，可用 `rules_source` 把来源仓库的 `AGENTS.md` 和 `.claude/rules` 链接到每个目录。
- 同一 inline 工作区内开发/Review 串行（共享一棵树）；澄清并行；不同工作区整体并行。

---

## 飞书命令

| 命令 | 作用 |
|---|---|
| `需求：<描述>` | 提一个新需求进需求池 |
| `Bug@codex：<现象>` / `Bug@cursor：<现象>` | 提交 Bug；指定修复 Agent，另一个自动 Review |
| `需求池` / `池子` | 列出待选择需求（可勾选确认） |
| `开始开发` | 把「待开发」队列整批开跑 |
| `看板` / `状态` / `配置` / `诊断` | 查看在途需求 / 当前需求 / 选 Agent 工作区 / 单条诊断 |
| `健康` / `统计` / `周报` | 服务健康 / 运行报表 |
| `重试` / `清锁` / `重新澄清` / `解除阻塞` / `完成` | 恢复类操作 |
| `切换Agent <claude\|codex\|gemini\|cursor>` / `切换工作区 <key>` | 改当前需求的 Agent / 工作区 |

---

## 注意

- **一个飞书 app 只能有一个长连接消费者。** 别同时跑第二个 listener（比如老的 Python 版或
  Hermes gateway 用同一个 app），否则两边抢事件、行为会错乱。
- 状态/锁/去重存在 `<仓库根>/state/niuma.sqlite3`；每次 Agent 调用的 prompt/输出/元数据
  落在 `state/agent-runs/`（保留最近 `PIPELINE_AGENT_RUNS_KEEP` 次）。
- 重新构建后用 `launchctl kickstart -k …`（或 `systemctl --user restart niuma`）重启才会生效。
