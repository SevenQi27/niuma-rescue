# Changelog

- 新增局域网 Bug 收件台：局域网内无需登录，支持飞书 Bug 的录入、修改、状态查看和启动修复
- Web 入口复用现有 Go 控制面和 LangGraph Bug 路线，处理中的记录保持只读
- worktree 工作区支持独立根目录；任务分支从最新远端基线创建且不跟踪主线，便于并发隔离
- 局域网 Bug 收件台支持图片、PDF、XLSX、XLS 和 CSV 附件；统一限制为最多 5 个、单个 8MB，保存在本机状态目录并复制到 Agent 的隔离 Bug 档案，不进入修复提交
- Bug 卡片支持展开“查看 AI 执行过程”：实时展示调查、修复、验证、独立 Review 和人工合并门，并兼容回溯已有 Agent 运行结果
- 实时刷新会保留“查看处理记录”和 AI 输出块的展开状态、文本滚动位置及页面位置，未变化的流水线不再每 2 秒重复重绘

## Unreleased

- 飞书支持 `Bug@codex：...` / `Bug@cursor：...` 直接录入 Bug
- Bug 强制使用独立 Git worktree，不进入需求澄清/合批开发主链路
- 新增 Python LangGraph 调查 → 修复 → 测试 → 独立 Review → 有界返修状态图
- Codex 与 Cursor 强制分任修复/Review；通过后只到人工合并门，不自动合并
- 测试失败不再进入 Review/PASS，而是返回修复并在达到上限后阻塞
- Bug 的完成操作会校验本地目标分支已包含修复提交或等价补丁
- LangGraph checkpoint 落 SQLite，Agent 执行继续复用 Go 侧认证、重试、超时与产物日志
- Base 新增 `任务类型` 单选与 `Bug处理中` 状态，并提供幂等 schema 检查/补齐命令

## v0.1.0

- 飞书 Base 作为需求池和状态面板
- `listener.py` 使用 lark-oapi 长连接接收飞书消息
- `dispatcher.py` 处理澄清、开发、Review、PR 创建
- 支持 `cursor/claude/codex/gemini` 作为可选 agent
- 支持 `需求@agent：xxx` 按需求选择澄清 agent
- 支持 GitHub/GitLab/SVN 工作区发布适配骨架
- 支持 bootstrap 自动建 Base 和字段
- 支持 doctor 部署自检
- 支持 macOS/Linux `install.sh`
- 支持 Windows `install.ps1`
