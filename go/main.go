package main

import (
	"os"
	"time"
)

func main() {
	cfg = loadConfig()
	if len(os.Args) > 1 && os.Args[1] == "agent-run" {
		os.Exit(runAgentCommand())
	}
	if len(os.Args) > 1 && (os.Args[1] == "schema-check-bug" || os.Args[1] == "schema-ensure-bug") {
		if err := cfg.validateSchemaCommand(); err != nil {
			logf("配置错误: %v", err)
			os.Exit(1)
		}
		os.Exit(runSchemaCommand(os.Args[1] == "schema-ensure-bug"))
	}
	if err := cfg.validate(); err != nil {
		logf("配置错误: %v", err)
		os.Exit(1)
	}
	run()
}

func run() {
	st, err := openStore(cfg.StateDir)
	if err != nil {
		logf("打开 store 失败: %v", err)
		os.Exit(1)
	}
	conc := cfg.MaxConcurrency
	if conc < 1 {
		conc = 1
	}
	app := &App{fs: newFeishu(cfg), st: st, sem: make(chan struct{}, conc)}

	// 调度触发：合并多次触发（缓冲 1），单 goroutine 串行执行 tick，tick 内对各记录并发。
	trigger := make(chan struct{}, 1)
	fire := func() {
		select {
		case trigger <- struct{}{}:
		default:
		}
	}
	go func() {
		for range trigger {
			app.tick()
		}
	}()
	// 兜底轮询（也兼顾 WS 掉线窗口漏掉的触发）：启动先跑一轮，之后每 PollInterval 一次。
	go func() {
		for {
			fire()
			time.Sleep(time.Duration(cfg.PollInterval) * time.Second)
		}
	}()

	logf("niuma 启动 · base=%s… table=%s · 并发=%d · 轮询每 %ds · 待选择门=%v",
		short(cfg.BaseToken, 8), cfg.TableID, conc, cfg.PollInterval, cfg.SetupGate)
	emit("dispatcher", "starting", map[string]any{"concurrency": conc, "poll": cfg.PollInterval})
	app.startListener(fire) // 阻塞常驻
}
