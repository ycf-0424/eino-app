// my-eino-app 控制台入口。
// 用法：
//
//	go run ./cmd/console "你的问题"     # 单次问答
//	go run ./cmd/console                # 交互模式（支持多轮，Ctrl+C 退出）
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/cloudwego/eino/schema"
	"os"
	"os/signal"
	"strings"
	"time"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/google/uuid"

	"my-eino-app/internal/checkpoint"
	"my-eino-app/internal/config"
	"my-eino-app/internal/eino/agent"
	"my-eino-app/internal/eino/chain"
	"my-eino-app/internal/eino/model"
	"my-eino-app/internal/eino/observability"
	"my-eino-app/internal/eino/rag"
	toolset "my-eino-app/internal/eino/tool"
	"my-eino-app/internal/health"
	appserver "my-eino-app/internal/server"
	"my-eino-app/internal/session"
	"my-eino-app/internal/skill"
)

// consoleOwner 是控制台 CLI 的会话归属。
//
// console 没有登录流程，天然是单用户工具，因此传空串——与「认证关闭」时
// HTTP 服务的行为一致，两边写入的会话可以互相读取。会话隔离上线后，
// 空 owner 不再匹配任何已归属的会话，这是预期的：CLI 不代登录用户操作数据。
const consoleOwner = ""

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	sessionID := flag.String("session", "", "会话 ID；留空则创建新会话")
	mode := flag.String("mode", "agent", "运行模式：agent 或 rag-chain")
	jsonOutput := flag.Bool("json", false, "rag-chain 模式下以 JSON 输出结构化答案")
	skillName := flag.String("skill", "", "加载 skills/<name>/SKILL.md 中的业务规则")
	flag.Parse()

	// 1. 加载配置 + 构造模型
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	if cfg.Debug {
		observability.EnableDebug()
	}
	if cfg.Memory.Enabled && *mode == "agent" {
		if err := runMemoryConsole(ctx, cfg, *sessionID, *skillName, strings.TrimSpace(strings.Join(flag.Args(), " "))); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		return
	}
	// 创建一个可以调用大模型的 Eino ChatModel 实例
	cm, err := model.NewChatModel(ctx, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "new chat model:", err)
		os.Exit(1)
	}
	if cfg.Ollama.HealthCheck && cfg.RAG.Enabled && strings.Contains(strings.ToLower(cfg.OpenAI.BaseURL), "localhost") {
		if err := health.CheckOllama(ctx, cfg.OpenAI.BaseURL, cfg.OpenAI.Model, cfg.RAG.Embedding.Model, cfg.RAG.Dimension, time.Duration(cfg.Ollama.Timeout)); err != nil {
			fmt.Fprintln(os.Stderr, "ollama:", err)
			os.Exit(1)
		}
	}

	var extraTools []einotool.BaseTool
	if cfg.LocalFiles.Enabled {
		fileTool, toolErr := toolset.NewLocalFileReadTool(cfg.LocalFiles.Roots, cfg.LocalFiles.MaxBytes)
		if toolErr != nil {
			fmt.Fprintln(os.Stderr, "local files:", toolErr)
			os.Exit(1)
		}
		extraTools = append(extraTools, fileTool)
	}
	var knowledgeStore rag.Store
	if cfg.RAG.Enabled {
		var ragErr error
		// 向量库不可用时限制初始化等待时间，并在启动阶段返回明确错误。
		storeContext, cancelStore := context.WithTimeout(ctx, 30*time.Second)
		knowledgeStore, ragErr = rag.NewFromConfig(storeContext, cfg.RAG)
		cancelStore()
		if ragErr != nil {
			fmt.Fprintln(os.Stderr, "rag:", ragErr)
			os.Exit(1)
		}
		extraTools = append(extraTools, toolset.NewKnowledgeToolWithOptions(knowledgeStore, rag.SearchOptions{
			TopK: cfg.RAG.TopK, ScoreThreshold: cfg.RAG.ScoreThreshold, MaxContextChars: cfg.RAG.MaxContextChars,
		}))
	}
	var ragChain *chain.RAGChain
	if *mode == "rag-chain" {
		if knowledgeStore == nil {
			fmt.Fprintln(os.Stderr, "rag-chain requires rag.enabled=true")
			os.Exit(1)
		}
		ragChain, err = chain.New(ctx, knowledgeStore, cm)
		if err != nil {
			fmt.Fprintln(os.Stderr, "new rag chain:", err)
			os.Exit(1)
		}
	}
	checkpointStore, err := checkpoint.New(cfg.Agent.CheckpointDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "checkpoint:", err)
		os.Exit(1)
	}
	selectedSkill := *skillName
	if selectedSkill == "" {
		selectedSkill = cfg.Skills.Default
	}
	skillInstruction, skillTool, _, err := skill.NewLoader(cfg.Skills.Dir).Runtime(selectedSkill)
	if err != nil {
		fmt.Fprintln(os.Stderr, "skills:", err)
		os.Exit(1)
	}
	extraTools = append(extraTools, skillTool)
	chat, err := agent.NewWithInstruction(ctx, cm, cfg.Debug, cfg.Agent.MultiAgent, checkpointStore, skillInstruction, extraTools...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "new agent:", err)
		os.Exit(1)
	}
	store, err := session.Open(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "session:", err)
		os.Exit(1)
	}
	if *sessionID == "" {
		*sessionID = uuid.NewString()
		fmt.Printf("已创建新会话: %s\n", *sessionID)
	} else {
		fmt.Printf("正在恢复会话: %s\n", *sessionID)
	}
	defer store.Close()
	chat.SetPersistence(func(messages []*schema.Message) error { return store.Save(consoleOwner, *sessionID, messages) }, cfg.Session.MaxMessages, cfg.Session.MaxChars)
	chat.SetSessionID(*sessionID)
	approvalScanner := bufio.NewScanner(os.Stdin)
	if history, loadErr := store.Load(consoleOwner, *sessionID); loadErr != nil {
		fmt.Fprintln(os.Stderr, "load session:", loadErr)
	} else if len(history) > 0 {
		chat.SetHistory(history)
		fmt.Printf("已恢复会话: %s（%d 条消息）\n", *sessionID, len(history))
	}
	if pending, pendingErr := checkpointStore.LoadApproval(*sessionID); pendingErr != nil {
		fmt.Fprintln(os.Stderr, "load pending approval:", pendingErr)
		os.Exit(1)
	} else if pending != nil {
		request := &agent.ApprovalRequest{TargetID: pending.TargetID, ToolName: pending.ToolName, Arguments: pending.Arguments}
		if err := resumeWithApproval(ctx, chat, checkpointStore, *sessionID, approvalScanner, request); err != nil {
			fmt.Fprintln(os.Stderr, "resume approval:", err)
			os.Exit(1)
		}
		if err := saveCompactedSession(store, chat, *sessionID, cfg); err != nil {
			fmt.Fprintln(os.Stderr, "save resumed session:", err)
		}
	}

	// 2. 单次问答模式
	if query := strings.TrimSpace(strings.Join(flag.Args(), " ")); query != "" {
		if *mode == "rag-chain" {
			if err := runRAGChain(ctx, ragChain, cfg.RAG.TopK, cfg.RAG.ScoreThreshold, cfg.RAG.MaxContextChars, query, *jsonOutput); err != nil {
				fmt.Fprintln(os.Stderr, "rag-chain:", err)
			}
			return
		}
		if err := runWithApproval(ctx, chat, checkpointStore, *sessionID, approvalScanner, query); err != nil {
			fmt.Fprintln(os.Stderr, "ask:", err)
		}
		_ = saveCompactedSession(store, chat, *sessionID, cfg)
		return
	}

	// 3. 交互模式
	fmt.Println("输入问题开始对话，Ctrl+C 退出")
	scanner := approvalScanner
	for {
		fmt.Print("你> ")
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				fmt.Fprintln(os.Stderr, "input:", err)
			}
			return
		}
		q := strings.TrimSpace(scanner.Text())
		if q == "" {
			continue
		}
		if q == "exit" || q == "quit" {
			return
		}
		if *mode == "rag-chain" {
			if err := runRAGChain(ctx, ragChain, cfg.RAG.TopK, cfg.RAG.ScoreThreshold, cfg.RAG.MaxContextChars, q, *jsonOutput); err != nil {
				fmt.Fprintln(os.Stderr, "rag-chain:", err)
			}
			continue
		}
		if err := runWithApproval(ctx, chat, checkpointStore, *sessionID, scanner, q); err != nil {
			fmt.Fprintln(os.Stderr, "ask:", err)
			continue
		}
		if err := saveCompactedSession(store, chat, *sessionID, cfg); err != nil {
			fmt.Fprintln(os.Stderr, "save session:", err)
		}
	}
}

// The automatic-memory console uses exactly the HTTP/WS service coordinator.
func runMemoryConsole(ctx context.Context, cfg *config.Config, id, skillName, query string) error {
	svc, err := appserver.NewService(ctx, cfg)
	if err != nil {
		return err
	}
	defer svc.Close()
	if id == "" {
		id = uuid.NewString()
	}
	fmt.Println("会话:", id)
	scanner := bufio.NewScanner(os.Stdin)
	run := func(q string) error {
		result, err := svc.Chat(ctx, id, q, skillName, os.Stdout)
		for err == nil && result.Approval != nil {
			fmt.Printf("\n工具 %s 需要审批，允许？(y/N)> ", result.Approval.ToolName)
			if !scanner.Scan() {
				return fmt.Errorf("approval pending; resume session %s", id)
			}
			result, err = svc.Approve(ctx, id, strings.EqualFold(strings.TrimSpace(scanner.Text()), "y"), os.Stdout)
		}
		if result.TurnID != "" {
			fmt.Printf("\n记忆任务轮次: %s\n", result.TurnID)
		}
		return err
	}
	if query != "" {
		return run(query)
	}
	for {
		fmt.Print("\n你> ")
		if !scanner.Scan() {
			return scanner.Err()
		}
		q := strings.TrimSpace(scanner.Text())
		if q == "exit" || q == "quit" {
			return nil
		}
		if q != "" {
			if err = run(q); err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
		}
	}
}

// saveCompactedSession 保存完整历史；上下文压缩由 Agent 在调用模型前单独执行。
func saveCompactedSession(store *session.Store, chat *agent.ChatAgent, sessionID string, cfg *config.Config) error {
	return store.Save(consoleOwner, sessionID, chat.History())
}

// runRAGChain 是固定 RAG 模式的控制台适配层；业务流程由 internal/chain 实现。
func runRAGChain(ctx context.Context, c *chain.RAGChain, topK int, threshold float64, maxChars int, query string, asJSON bool) error {
	result, err := c.Run(ctx, chain.Input{Query: query, TopK: topK, ScoreThreshold: threshold, MaxContextChars: maxChars})
	if err != nil {
		return err
	}
	if asJSON {
		data, marshalErr := result.JSON()
		if marshalErr != nil {
			return marshalErr
		}
		fmt.Println(string(data))
		return nil
	}
	fmt.Printf("答案：%s\n", result.Answer)
	if len(result.Sources) > 0 {
		fmt.Printf("来源：%s\n", strings.Join(result.Sources, ", "))
	}
	if result.Error != "" {
		fmt.Fprintf(os.Stderr, "提示：%s\n", result.Error)
	}
	return nil
}

// runWithApproval 处理工具触发的 StatefulInterrupt，并在用户决定后恢复原执行。
func runWithApproval(ctx context.Context, chat *agent.ChatAgent, store *checkpoint.FileStore, sessionID string, scanner *bufio.Scanner, query string) error {
	err := chat.Ask(ctx, query)
	for err != nil {
		var approval *agent.ApprovalRequest
		if !errors.As(err, &approval) {
			return err
		}
		if saveErr := store.SaveApproval(sessionID, checkpoint.PendingApproval{TargetID: approval.TargetID, ToolName: approval.ToolName, Arguments: approval.Arguments}); saveErr != nil {
			return saveErr
		}
		err = resumeWithApproval(ctx, chat, store, sessionID, scanner, approval)
	}
	return nil
}

func resumeWithApproval(ctx context.Context, chat *agent.ChatAgent, store *checkpoint.FileStore, sessionID string, scanner *bufio.Scanner, approval *agent.ApprovalRequest) error {
	fmt.Printf("\n需要审批：工具=%s 参数=%s\n允许执行？(y/N)> ", approval.ToolName, approval.Arguments)
	if !scanner.Scan() {
		return fmt.Errorf("approval input ended; restart with --session %s to continue", sessionID)
	}
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	err := chat.ResumeApproval(ctx, approval, answer == "y" || answer == "yes")
	if err == nil {
		return store.ClearApproval(sessionID)
	}
	var next *agent.ApprovalRequest
	if errors.As(err, &next) {
		if saveErr := store.SaveApproval(sessionID, checkpoint.PendingApproval{TargetID: next.TargetID, ToolName: next.ToolName, Arguments: next.Arguments}); saveErr != nil {
			return saveErr
		}
		return resumeWithApproval(ctx, chat, store, sessionID, scanner, next)
	}
	return err
}
