package main

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// startListener 起飞书长连接（消息 + 卡片回调走同一条 WS，无需公网 URL）。阻塞。
func (a *App) startListener(ctx context.Context, trigger func(), settings integrationSettings) {
	h := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(ctx context.Context, e *larkim.P2MessageReceiveV1) error {
			a.onMessage(e, trigger)
			return nil
		}).
		OnP2CardActionTrigger(func(ctx context.Context, e *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
			return a.onCardAction(e, trigger)
		}).
		// 应用订阅了"消息已读"事件但我们不处理；注册空 handler 免得 SDK 刷 ERROR 日志。
		OnP2MessageReadV1(func(ctx context.Context, e *larkim.P2MessageReadV1) error { return nil })
	cli := larkws.NewClient(settings.AppID, settings.AppSecret, larkws.WithEventHandler(h))

	// 心跳：每 60s 记一次"还活着"，供 `健康`/doctor 判断 listener 是否在跑。
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(60 * time.Second):
				emit("listener", "alive", nil)
			}
		}
	}()

	emit("listener", "starting", nil)
	for {
		if ctx.Err() != nil {
			emit("listener", "disabled", nil)
			return
		}
		elog("连接飞书长连接，监听消息 + 卡片回调 …")
		if err := cli.Start(ctx); err != nil {
			if ctx.Err() != nil {
				emit("listener", "disabled", nil)
				return
			}
			elog("listener 退出: %v，5s 后重连", err)
			emit("listener", "stopped", map[string]any{"error": err.Error()})
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		elog("listener 返回 nil，主进程保持常驻")
		emit("listener", "ready", nil)
		select {}
	}
}

func (a *App) restartListener(trigger func()) {
	a.listenerMu.Lock()
	defer a.listenerMu.Unlock()
	if a.listenerCancel != nil {
		a.listenerCancel()
		a.listenerCancel = nil
	}
	settings := a.records.currentSettings()
	if !settings.Enabled || !integrationConfigured(settings) {
		emit("listener", "disabled", nil)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.listenerCancel = cancel
	go a.startListener(ctx, trigger, settings)
}

func (a *App) onMessage(e *larkim.P2MessageReceiveV1, trigger func()) {
	defer func() {
		if r := recover(); r != nil {
			elog("处理消息异常: %v", r)
		}
	}()
	if e.Event == nil || e.Event.Message == nil {
		return
	}
	m := e.Event.Message
	if deref(m.MessageType) != "text" {
		return
	}
	var content struct {
		Text string `json:"text"`
	}
	json.Unmarshal([]byte(deref(m.Content)), &content)
	text := strings.TrimSpace(content.Text)
	sender := ""
	if e.Event.Sender != nil && e.Event.Sender.SenderId != nil {
		sender = deref(e.Event.Sender.SenderId.OpenId)
	}
	chatID := deref(m.ChatId)
	msg := map[string]any{
		"message_type": "text", "chat_id": chatID, "content": text,
		"sender_id": sender, "message_id": deref(m.MessageId),
	}
	id, inserted := a.st.enqueue(deref(m.MessageId), msg)
	emit("listener", "message_received", map[string]any{"chat_id": chatID, "inbox_id": id, "inserted": inserted})
	elog("recv chat=%s inbox=%d inserted=%v text=%q", chatID, id, inserted, cut(text, 80))
	if inserted {
		go a.st.processPending(a.handleMessage, trigger, 10)
	}
}

func (a *App) onCardAction(e *callback.CardActionTriggerEvent, trigger func()) (*callback.CardActionTriggerResponse, error) {
	var value map[string]any
	var msgID string
	if e.Event != nil {
		if e.Event.Action != nil {
			value = e.Event.Action.Value
			if value == nil {
				value = map[string]any{}
			}
			if fv := e.Event.Action.FormValue; fv != nil {
				value["_form"] = fv // 表单提交值（多选等），按组件 name 取
			}
		}
		if e.Event.Context != nil {
			msgID = e.Event.Context.OpenMessageID
		}
	}
	// 重活异步做，绝不阻塞 WS 收包线程（否则 keepalive 超时会掉线丢消息）。
	go func() {
		defer func() {
			if r := recover(); r != nil {
				elog("卡片回调异常: %v", r)
			}
		}()
		toast, dispatch, newCard := a.handleCardAction(value)
		emit("listener", "card_action", map[string]any{"dispatch": dispatch})
		elog("card_action %v → %s", value, toast)
		if newCard != nil && msgID != "" {
			if err := a.fs.patchCard(msgID, newCard); err != nil {
				elog("卡片更新失败: %v", err)
			}
		}
		if dispatch {
			trigger()
		}
	}()
	return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: "info", Content: "已收到，处理中…"}}, nil
}
