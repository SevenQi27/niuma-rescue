# Release Checklist

## 代码与测试

- [ ] 发布提交基于预期的 `origin/main` 基线，分支关系已确认
- [ ] `cd go && go test ./...` 通过
- [ ] `cd go && go build ./...` 通过
- [ ] `cd buggraph && .venv/bin/python -m unittest discover -s tests -v` 通过
- [ ] `git diff --check` 通过
- [ ] 工作区没有意外生成的二进制、数据库、日志或附件

## 配置与安全

- [ ] `.env`、`workspaces.json`、`state/`、`logs/` 未进入提交或发布包
- [ ] `.env.example` 和 `workspaces.example.json` 只包含占位值
- [ ] 页面截图不包含真实 Bug、账号、Token、绝对私人路径或业务附件
- [ ] Web 无鉴权和可信局域网限制已在 README 中说明
- [ ] 至少一个 Agent CLI 在目标服务用户下完成无头调用验证
- [ ] 代码工作区配置了与风险相符的真实 `test_cmd`

## 功能回归

- [ ] 本地模式无需飞书即可启动、保存任务和打开管理页
- [ ] 需求流程停在澄清确认、开发启动和最终交付等人工卡点
- [ ] Bug 流程使用独立 worktree，修复与 Review Agent 不相同
- [ ] 重叠 Bug 会等待代码区域，不会同时写入相同文件
- [ ] task worktree 和 session 分支不跟踪基线分支
- [ ] 共享会话任务可并行执行、Review 后串行集成
- [ ] 集成冲突只阻塞当前任务且会话 worktree 保持干净
- [ ] 会话有未集成任务时不能冻结；冻结不会自动合并目标分支
- [ ] 飞书启停/失败不影响本地任务主库
- [ ] 禅道/Jira Webhook Token 和幂等逻辑测试通过

## 文档与交付

- [ ] README 截图和功能说明与当前二进制一致
- [ ] quickstart、config、operations、agent CLI、delivery 文档已同步
- [ ] CHANGELOG 记录本次功能和已知边界
- [ ] 目标平台二进制已在对应操作系统实测，而不只是交叉编译成功
- [ ] 升级前已说明停止服务并备份 `state/`
