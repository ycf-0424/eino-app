package main

import (
	"context"
	"flag"
	"fmt"
	"my-eino-app/internal/config"
	"my-eino-app/internal/memory"
	modelset "my-eino-app/internal/model"
	"my-eino-app/internal/session"
	"os"
	"os/signal"
	"time"
)

func main() {
	execute := flag.Bool("execute", false, "从当前身份范围的 SQL 记忆重建向量；默认只统计")
	worker := flag.Bool("worker", false, "持续处理持久化队列，供控制台单次请求退出后使用")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	cfg, err := config.Load()
	if err != nil {
		fail(err)
	}
	if !cfg.Memory.Enabled {
		fail(fmt.Errorf("memory is disabled"))
	}
	s, err := session.Open(cfg)
	if err != nil {
		fail(err)
	}
	defer s.Close()
	if cfg.Memory.ExtractorModel != "inherit_chat" {
		cfg.OpenAI.Model = cfg.Memory.ExtractorModel
	}
	cfg.OpenAI.MaxCompletionTokens = 2048
	cm, err := modelset.NewChatModel(ctx, cfg)
	if err != nil {
		fail(err)
	}
	engine, err := memory.Open(ctx, cfg, s, cm)
	if err != nil {
		fail(err)
	}
	defer engine.Close()
	if *worker {
		engine.Start(ctx)
		<-ctx.Done()
		return
	}
	n, err := engine.Repo.Reindex(ctx, *execute)
	if err != nil {
		fail(err)
	}
	fmt.Printf("scope=%s candidates=%d execute=%v\n", cfg.Memory.ProjectID, n, *execute)
	if *execute {
		for {
			worked, err := engine.ProcessOne(ctx)
			if err != nil {
				fail(err)
			}
			if !worked {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
}
func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
