# 飞书应用配置

## 应用类型

使用飞书开发者后台创建一个企业自建应用。系统需要：

- 通过 OpenAPI 读写多维表格
- 通过 IM 给用户发消息
- 通过长连接接收用户私聊消息

## 必要配置

把凭据填入项目 `.env`：

```text
FEISHU_APP_ID=cli_xxx
FEISHU_APP_SECRET=xxx
```

## 权限

至少需要覆盖这些能力：

- 多维表格创建、读取、写入字段和记录
- IM 消息发送
- 接收机器人私聊消息事件

权限名称会随飞书后台调整，按能力勾选即可。授权或发布后，手动建一张多维表格（字段见下），
把表格 URL 里的 `app_token` / `table_id` 填进 `.env`，然后 `cd go && ./niuma` 即可。

## 事件订阅

listener 使用官方 `lark-oapi` SDK 长连接接收：

```text
im.message.receive_v1
```

请确保机器人能接收用户私聊消息。不要同时启动 Hermes gateway 使用同一个 app，否则两个长连接消费者可能分流事件。

## Base 表结构

手动新建一张多维表格，把 URL 里的 `app_token` / `table_id` 填进 `.env` 的
`PIPELINE_BASE_TOKEN` / `PIPELINE_TABLE_ID`。字段名必须**完全一致**。

核心字段（文本，`状态` 为单选）：

```text
需求标题 / 状态 / 需求描述 / 澄清记录 / PRD / 分支PR链接 / 执行日志 / 失败次数 / 提需求人 / 会话ID / 工作区
```

Agent 字段（单选）：

```text
执行Agent / 澄清Agent / 开发Agent / ReviewAgent
```

任务分类字段（单选）：

```text
任务类型：需求 / Bug
```

**`状态` 单选必须包含以下选项**（名字完全一致，缺一个会导致写入被飞书拒绝）：

```text
待选择 · 待澄清 · 待回答 · 待确认 · 待开发 · 开发中 · Bug处理中 · Review中 · 待合并 · 完成 · 已阻塞
```

可先只读检查，再幂等补齐 Bug 所需字段/选项：

```bash
cd go
./niuma schema-check-bug
./niuma schema-ensure-bug
```
