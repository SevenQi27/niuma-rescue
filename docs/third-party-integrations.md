# 第三方集成中心

Niuma 的本地 SQLite 和 Go 状态机始终是任务主控。飞书、禅道、Jira 和 Slack 只作为任务入口、外部工作项绑定或通知渠道；停用任意连接器不会删除本地任务，也不会停止 Agent 流水线。

## 当前 MVP

| 平台 | 当前能力 | 下一阶段 |
| --- | --- | --- |
| 飞书 | 多维表格双向同步、消息卡片、长连接接入、运行时启停 | 迁入统一外部绑定模型 |
| 禅道 | 保存配置、API 连通测试、工作区映射、Webhook 验 Token、事件去重入队 | 根据事件拉取完整 Bug/需求并创建任务；评论和状态回写 |
| Jira | Cloud/DC 配置、账号 Token 测试、项目与工作区映射、Webhook 验 Token、事件去重入队 | JQL 过滤、Issue 字段拉取、动态 Transition 和状态回写 |
| Slack | Bot/App/Signing Secret 配置、Bot Token 测试、Socket/Events API 模式配置 | Socket Mode 消费、表单/快捷操作和阶段通知 |

当前版本不会把收到的禅道/Jira 事件直接交给 Agent。事件会先安全落入 `integration_events`，管理员可以在 `/manage` 的“最近接入事件”中确认真实载荷已经到达，再启用字段映射和任务创建，避免第一次接入时因平台字段差异误启动任务。

## 配置位置

打开 `http://<局域网 IP>:8787/manage`，进入“第三方集成”：

1. 选择禅道、Jira 或 Slack 卡片；
2. 填写服务地址、Token、项目和本地工作区；
3. 先点“测试连接”；
4. 填写 Webhook Secret，启用并保存连接器；
5. 将页面显示的事件入口配置到第三方平台。

管理 API 不返回已保存的 Token，只返回 `has_*` 标志。密码框留空表示保留原密钥。配置保存在 `state/integrations.json`，文件权限为 `0600`。

## Webhook 入口

禅道和 Jira 的入口分别为：

```text
POST /api/integrations/zentao/events
POST /api/integrations/jira/events
```

Token 可以通过下面任一种方式发送：

```text
X-Niuma-Integration-Token: <Webhook Secret>
```

```text
?token=<Webhook Secret>
```

收到合法 JSON 后，服务立即落库并返回 `202 Accepted`。同一事件再次发送时仍返回成功，但响应中的 `inserted` 为 `false`，不会创建第二条队列记录。

Slack 选择 Events API 模式时使用：

```text
POST /api/integrations/slack/events
```

该入口按照 Slack 的时间戳和 `v0` HMAC 规则校验 `X-Slack-Signature`，并支持 `url_verification` challenge。Socket Mode 配置当前只负责保存和测试 Token，尚未启动 WebSocket 消费循环。

## 数据表

- `integration_events`：原始第三方事件、幂等键、状态和重试信息；
- `external_bindings`：本地任务与多个第三方对象之间的绑定，为后续双向同步预留；
- `local_records`：仍然是 Bug/需求主数据，不因第三方连接器改变。
