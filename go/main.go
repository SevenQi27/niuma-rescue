package main

import (
	"context"
	"os"
	"strings"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "mysql-readonly-mcp" {
		os.Exit(runReadonlyMySQLMCP(os.Stdin, os.Stdout))
	}
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
	loadPipelineOverrides(cfg)
	st, err := openStore(cfg.StateDir)
	if err != nil {
		logf("打开 store 失败: %v", err)
		os.Exit(1)
	}
	conc := cfg.MaxConcurrency
	if conc < 1 {
		conc = 1
	}
	local := newLocalRecords(st)
	records := newHybridRecords(local, cfg.StateDir, loadIntegrationSettings(cfg))
	integrations := newIntegrationHub(cfg.StateDir)
	app := &App{fs: records, records: records, integrations: integrations, st: st, sem: make(chan struct{}, conc)}

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
	if cfg.WebEnabled {
		go func() {
			if err := app.serveWeb(fire); err != nil {
				elog("Web 控制台退出: %v", err)
			}
		}()
	}
	if _, enabled := records.currentFeishu(); enabled {
		if err := records.syncNow(); err != nil {
			elog("飞书首次同步失败，本地控制台继续运行: %v", err)
		}
	}
	app.restartListener(fire)
	go app.syncLoop(context.Background())
	// 兜底轮询（也兼顾 WS 掉线窗口漏掉的触发）：启动先跑一轮，之后每 PollInterval 一次。
	go func() {
		for {
			fire()
			time.Sleep(time.Duration(cfg.PollInterval) * time.Second)
		}
	}()

	mode := "本地"
	if _, enabled := records.currentFeishu(); enabled {
		mode = "本地 + 飞书"
	}
	logf("niuma 启动 · 数据源=%s · 第三方连接器=%d · 并发=%d · 轮询每 %ds · 待选择门=%v", mode, integrations.enabledCount(), conc, cfg.PollInterval, cfg.SetupGate)
	emit("dispatcher", "starting", map[string]any{"concurrency": conc, "poll": cfg.PollInterval})
	select {}
}

func (a *App) syncLoop(ctx context.Context) {
	for {
		settings := a.records.currentSettings()
		period := settings.SyncPeriod
		if period < 15 {
			period = 60
		}
		timer := time.NewTimer(time.Duration(period) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			if settings.Enabled {
				if err := a.records.syncNow(); err != nil && !strings.Contains(err.Error(), "正在进行") {
					elog("飞书同步失败: %v", err)
				}
			}
		}
	}
}
