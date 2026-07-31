# Changelog

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
