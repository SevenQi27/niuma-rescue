# Changelog

- SQLite 升级为任务唯一主数据源；无飞书配置时服务、Web 和 Agent 流水线可以完整运行
- Web 升级为双页签任务中心：Bug 与需求分开录入、筛选和管理；需求支持 AI 澄清、人工确认、开始开发、独立 Review 与人工完成
- 飞书改为可选连接器：支持运行时启停、连接检测、手动/定时同步、失败状态记录和已有 Base 记录导入
- 新增 `/manage` 管理控制台：管理任务停止、重试、确认完成、归档、工作区及 Bug 默认 Agent/超时
- 管理控制台新增第三方集成中心：统一展示飞书、禅道、Jira 和 Slack；支持运行时保存/停用、密钥脱敏、远端连接测试和工作区映射
- 禅道/Jira 新增受 Webhook Token 保护的事件入口；第三方事件先落入 SQLite，按平台事件键幂等去重，并可在管理页查看最近接入记录
- SQLite 新增第三方事件队列和外部对象绑定表，为后续字段拉取、双向状态回写及一任务多平台绑定提供基础
- Bug 卡片展示仅本地、等待同步、已同步和同步失败状态；飞书失败不再回滚本地操作
- 管理控制台当前按部署要求不做鉴权，仅适用于可信局域网
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
