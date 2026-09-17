// Package server 将 Agent、Session、Checkpoint 和 Skill 封装为可复用应用服务。
package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/cloudwego/eino/schema"
	"my-eino-app/internal/auth"
	"my-eino-app/internal/checkpoint"
	"my-eino-app/internal/config"
	"my-eino-app/internal/eino"
	"my-eino-app/internal/eino/agent"
	modelset "my-eino-app/internal/eino/model"
	"my-eino-app/internal/eino/rag"
	toolset "my-eino-app/internal/eino/tool"
	"my-eino-app/internal/execution"
	"my-eino-app/internal/memory"
	"my-eino-app/internal/session"
	"my-eino-app/internal/skill"
)

// Service 类型。
type Service struct {
	memories    *memory.Engine
	cfg         *config.Config
	model       eino.ChatModel
	tools       []eino.BaseTool
	checkpoints *checkpoint.FileStore
	sessions    *session.Store
	skills      *skill.Loader
	// executions 在执行事件关闭或后端不可用时为 nil。
	executions execution.Store
	execCfg    config.ExecutionEvents
	// 认证组件在 cfg.Auth.Enabled 时由 NewService 装配；零值时 authEnabled()
	// 为 false，路由不注册、请求不需要登录。
	authSessions *auth.Sessions
	authFeishu   *auth.FeishuClient
	authUsers    *auth.UserStore
	authMW       *auth.Middleware
	// localAccounts 与 loginLimiter 仅在 auth.local.enabled 时装配；
	// 两者为 nil 时 POST /auth/local 不注册（404）。
	localAccounts *auth.LocalStore
	loginLimiter  *rateLimiter
	// chatLimiter 是 /chat 与 /ws 的 per-user 限流（步骤 5.2），
	// runtime.rate_limit.enabled 为 false 时保持 nil，middleware 直接透传。
	chatLimiter   *rateLimiter
	lockMu        sync.Mutex
	locks         map[string]chan struct{}
	concurrency   chan struct{}
	cleanupCancel context.CancelFunc
	requests      atomic.Uint64
	errors        atomic.Uint64
	active        atomic.Int64
	durationNS    atomic.Int64
}

// ChatResult 类型。Events 只在开启执行事件且使用同步接口时返回。
type ChatResult struct {
	TurnID    string                 `json:"turn_id,omitempty"`
	SessionID string                 `json:"session_id"`
	Answer    string                 `json:"answer,omitempty"`
	Approval  *agent.ApprovalRequest `json:"approval,omitempty"`
	RunID     string                 `json:"run_id,omitempty"`
	Events    []execution.Event      `json:"events,omitempty"`
}

// NewService 函数。
func NewService(ctx context.Context, cfg *config.Config) (*Service, error) {
	cm, err := modelset.NewChatModel(ctx, cfg)
	if err != nil {
		return nil, err
	}
	var tools []eino.BaseTool
	if cfg.LocalFiles.Enabled {
		fileTool, toolErr := toolset.NewLocalFileReadTool(cfg.LocalFiles.Roots, cfg.LocalFiles.MaxBytes)
		if toolErr != nil {
			return nil, toolErr
		}
		tools = append(tools, fileTool)
	}
	if cfg.RAG.Enabled {
		initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		store, storeErr := rag.NewFromConfig(initCtx, cfg.RAG)
		cancel()
		if storeErr != nil {
			return nil, storeErr
		}
		tools = append(tools, toolset.NewKnowledgeToolWithOptions(store, rag.SearchOptions{TopK: cfg.RAG.TopK, ScoreThreshold: cfg.RAG.ScoreThreshold, MaxContextChars: cfg.RAG.MaxContextChars}))
	}
	cp, err := checkpoint.New(cfg.Agent.CheckpointDir)
	if err != nil {
		return nil, err
	}
	sessions, err := session.Open(cfg)
	if err != nil {
		return nil, err
	}
	// 技能名与技能目录属于内部实现，只在 debug 模式向调用方暴露。
	skillLoader := skill.NewLoader(cfg.Skills.Dir)
	skillLoader.ExposeNames = cfg.Debug
	service := &Service{cfg: cfg, model: cm, tools: tools, checkpoints: cp, sessions: sessions, skills: skillLoader, execCfg: cfg.ExecutionEvents, locks: map[string]chan struct{}{}, concurrency: make(chan struct{}, cfg.Runtime.MaxConcurrency)}
	executionStore, err := newExecutionStore(ctx, cfg, sessions)
	if err != nil {
		return nil, err
	}
	service.executions = executionStore
	if cfg.Auth.Enabled {
		// ValidateAuth 已保证 session.store=mysql 且至少一种登录方式可用。
		service.authSessions = auth.NewSessions(time.Duration(cfg.Auth.SessionTTL))
		service.authSessions.StartJanitor(ctx)
		service.authFeishu = auth.NewFeishuClient(cfg.Auth)
		var userDB *sql.DB
		if cfg.Session.Store == "mysql" {
			userDB = sessions.DB()
		}
		service.authUsers = auth.NewUserStore(userDB)
		service.authMW = auth.NewMiddleware(service.authSessions)
		if cfg.Auth.Local.Enabled {
			// 自有账号是例外通道（没有飞书账号的人 + 飞书故障兜底），
			// 与飞书共用同一套登录态签发，只是凭据来源不同。
			service.localAccounts = auth.NewLocalStore(userDB)
			// 每分钟 10 次、突发 10 次：正常人手速远低于此，爆破者会立刻撞墙。
			service.loginLimiter = newRateLimiter(loginRatePerMinute, loginBurst)
		}
	}
	if cfg.Runtime.RateLimit.Enabled {
		// key 与登录限流分开两套：登录按「IP + 用户名」，这里按「owner（或 IP）」。
		// 共用一个限流器会让一次登录失败扣掉聊天的配额，两者语义不同。
		service.chatLimiter = newRateLimiter(cfg.Runtime.RateLimit.PerMinute, cfg.Runtime.RateLimit.Burst)
	}
	if cfg.Memory.Enabled {
		// Reuse the already-loaded chat model for the background extractor. Creating
		// a second local llama server can exhaust GPU memory; an override model is
		// still supported by constructing a separate instance below.
		memoryModel := eino.BaseChatModel(cm)
		if cfg.Memory.ExtractorModel != "inherit_chat" && cfg.Memory.ExtractorModel != "" {
			memoryCfg := *cfg
			memoryCfg.OpenAI.Model = cfg.Memory.ExtractorModel
			memoryCfg.OpenAI.MaxCompletionTokens = 2048
			memoryModel, err = modelset.NewChatModel(ctx, &memoryCfg)
			if err != nil {
				sessions.Close()
				return nil, err
			}
		}
		service.memories, err = memory.Open(ctx, cfg, sessions, memoryModel)
		if err != nil {
			sessions.Close()
			return nil, err
		}
		service.memories.Start(ctx)
	}
	if cfg.Session.ExpireDays > 0 && cfg.Session.Store == "mysql" {
		cleanupCtx, cleanupCancel := context.WithCancel(ctx)
		service.cleanupCancel = cleanupCancel
		go func() {
			interval := 24 * time.Hour
			if cfg.Memory.CleanupInterval > 0 {
				interval = time.Duration(cfg.Memory.CleanupInterval)
			}
			cleanup := func() {
				batch := 500
				if cfg.Memory.CleanupBatchSize > 0 {
					batch = cfg.Memory.CleanupBatchSize
				}
				removed, err := sessions.CleanupOlderThanBatch(time.Duration(cfg.Session.ExpireDays)*24*time.Hour, batch)
				if err != nil {
					log.Printf("session cleanup failed: %v", err)
					return
				}
				if removed > 0 {
					log.Printf("session cleanup removed %d expired conversations", removed)
				}
			}
			cleanup()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-cleanupCtx.Done():
					return
				case <-ticker.C:
					cleanup()
				}
			}
		}()
	}
	return service, nil
}

func (s *Service) Close() error {
	if s.cleanupCancel != nil {
		s.cleanupCancel()
	}
	var closeErr error
	if s.memories != nil {
		closeErr = s.memories.Close()
	}
	if s.sessions != nil {
		if err := s.sessions.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}

// newExecutionStore 按配置创建执行记录后端；未启用时返回 nil。
func newExecutionStore(ctx context.Context, cfg *config.Config, sessions *session.Store) (execution.Store, error) {
	if !cfg.ExecutionEvents.Enabled {
		return nil, nil
	}
	var store execution.Store
	if cfg.Session.Store == "mysql" {
		db := sessions.DB()
		if db == nil {
			return nil, fmt.Errorf("execution events require a mysql session store when session.store=mysql")
		}
		store = execution.NewMySQLStore(db)
	} else {
		dir := cfg.ExecutionEvents.Dir
		if strings.TrimSpace(dir) == "" {
			dir = "data/executions"
		}
		fileStore, err := execution.NewFileStore(dir)
		if err != nil {
			return nil, err
		}
		store = fileStore
	}
	if store == nil {
		return nil, nil
	}
	// 启动时修复异常退出留下的 running 记录，并按保留期清理旧数据。
	recoverCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := store.RecoverStale(recoverCtx, 5*time.Minute); err != nil {
		return nil, fmt.Errorf("recover stale executions: %w", err)
	}
	if cfg.ExecutionEvents.RetentionDays > 0 {
		_, _ = store.CleanupOlderThan(recoverCtx, time.Duration(cfg.ExecutionEvents.RetentionDays)*24*time.Hour)
	}
	return store, nil
}

// Stats 类型。
type Stats struct {
	Requests          uint64  `json:"requests"`
	Errors            uint64  `json:"errors"`
	Active            int64   `json:"active"`
	AverageDurationMS float64 `json:"average_duration_ms"`
}

func (s *Service) Stats() Stats {
	requests := s.requests.Load()
	average := 0.0
	if requests > 0 {
		average = float64(s.durationNS.Load()) / float64(time.Millisecond) / float64(requests)
	}
	return Stats{Requests: requests, Errors: s.errors.Load(), Active: s.active.Load(), AverageDurationMS: average}
}
func (s *Service) enter(ctx context.Context) (func(error), error) {
	if s.concurrency != nil {
		select {
		case s.concurrency <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	s.requests.Add(1)
	s.active.Add(1)
	started := time.Now()
	return func(err error) {
		if err != nil {
			s.errors.Add(1)
		}
		s.durationNS.Add(time.Since(started).Nanoseconds())
		s.active.Add(-1)
		if s.concurrency != nil {
			<-s.concurrency
		}
	}, nil
}

func (s *Service) Sessions() *session.Store    { return s.sessions }
func (s *Service) Skills() *skill.Loader       { return s.skills }
func (s *Service) Executions() execution.Store { return s.executions }
func (s *Service) ExecutionEnabled() bool      { return s.execCfg.Enabled }

// debugEnabled 判断当前是否处于调试模式。
// 技能目录、技能名以及按名指定技能都属于内部实现，只在调试模式下对调用方开放；
// cfg 在部分单元测试中为 nil，因此这里必须做空值保护。
func (s *Service) debugEnabled() bool { return s.cfg != nil && s.cfg.Debug }

// requestedSkill 过滤调用方按名指定的技能。
// 能按名指定技能，等价于把技能清单交给调用方，属于内部实现：非调试模式一律忽略，
// 由模型依据当前问题自主选择。服务层的 Go API 仍保留 preferred 能力，
// 供 CLI 与评测在进程内强制指定技能跑基线。
func (s *Service) requestedSkill(skill string) string {
	if !s.debugEnabled() {
		return ""
	}
	return skill
}

// acquireSession 保证同一会话的消息串行处理，同时允许排队中的请求响应取消。
func (s *Service) acquireSession(ctx context.Context, id string) (func(), error) {
	s.lockMu.Lock()
	lock := s.locks[id]
	if lock == nil {
		lock = make(chan struct{}, 1)
		s.locks[id] = lock
	}
	s.lockMu.Unlock()
	select {
	case lock <- struct{}{}:
		return func() { <-lock }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// newAgent 创建本轮 Agent，并返回本轮直接预加载的技能名。
func (s *Service) newAgent(ctx context.Context, id, skillName string) (*agent.ChatAgent, []string, error) {
	// owner 在这里取出并捕获进闭包：SetPersistence 的回调可能在请求 context
	// 已取消之后才被调用，那时再读 context 已经取不到值。
	owner := auth.OwnerFromContext(ctx)
	if s.memories != nil {
		// 记忆的归属校验与检索都必须按当前登录身份派生 Repository，
		// 否则会读到配置里那个 owner 的数据（多用户下就是串号）。
		// OwnerScope 负责单用户/多用户两种模式的换算，见 memory.Engine.OwnerScope。
		if err := s.memories.Repo.For(s.memories.OwnerScope(owner)).CheckBinding(ctx, id); err != nil {
			return nil, nil, err
		}
	}
	if skillName == "" {
		skillName = s.cfg.Skills.Default
	}
	availableTools := []string{"current_time", "write_note", "load_skills"}
	for _, registered := range s.tools {
		info, infoErr := registered.Info(ctx)
		if infoErr != nil {
			return nil, nil, infoErr
		}
		availableTools = append(availableTools, info.Name)
	}
	instruction, skillTool, preloaded, err := s.skills.Runtime(skillName, availableTools...)
	if err != nil {
		return nil, nil, err
	}
	tools := append([]eino.BaseTool{}, s.tools...)
	tools = append(tools, skillTool)
	chat, err := agent.NewWithInstruction(ctx, s.model, s.cfg.Debug, s.cfg.Agent.MultiAgent, s.checkpointStoreFor(ctx), instruction, tools...)
	if err != nil {
		return nil, nil, err
	}
	chat.SetSessionID(id)
	history, err := s.sessions.Load(owner, id)
	if err != nil {
		return nil, nil, err
	}
	// 持久化完整历史；只有传给模型的上下文才会压缩。
	chat.SetHistory(history)
	if s.memories != nil {
		chat.SetMemoryContext(s.memories.ContextFor(s.memories.OwnerScope(owner)))
	}
	chat.SetPersistence(func(messages []*schema.Message) error { return s.sessions.Save(owner, id, messages) }, s.cfg.Session.MaxMessages, s.cfg.Session.MaxChars)
	return chat, preloaded, nil
}

// save 以当前请求身份落盘。owner 取自 context，因此 CLI 侧（不注入 owner）
// 与 HTTP 侧共用同一套调用，行为与改造前一致。
func (s *Service) save(ctx context.Context, id string, chat *agent.ChatAgent) error {
	return s.sessions.Save(auth.OwnerFromContext(ctx), id, chat.History())
}

// beginRun 建立本轮执行上下文。开关关闭时返回 Nop，不产生任何副作用。
func (s *Service) beginRun(ctx context.Context, id string, sink execution.Sink, resumeRunID string) (context.Context, *execution.Session) {
	if !s.execCfg.Enabled {
		return ctx, nil
	}
	runID := resumeRunID
	startSequence := int64(0)
	if runID == "" {
		runID = uuid.NewString()
	} else if s.executions != nil {
		// 审批恢复沿用同一个 run，序号必须接着已有最大值，避免唯一键冲突。
		if max, err := s.executions.MaxSequence(ctx, runID); err == nil {
			startSequence = max
		}
	}
	em := execution.NewSession(runID, id, s.executions, sink, execution.SessionOptions{
		MaxEventsPerRun: s.execCfg.MaxEventsPerRun,
		StartSequence:   startSequence,
	})
	if s.executions != nil {
		// StartRun 内部会先确保 conversations 行存在，否则首轮外键失败。
		// owner 必须一并写入：会话隔离后，若这里留下 owner_id='' 的行，
		// 紧接着的 Save 会因归属不匹配而拒绝落盘（新会话第一轮就报错）。
		_ = s.executions.StartRun(ctx, execution.Run{RunID: runID, SessionID: id, Owner: auth.OwnerFromContext(ctx), Status: execution.StatusRunning, StartedAt: time.Now().UTC()})
	}
	return execution.WithEmitter(ctx, em), em
}

// linkRun 把 run 关联到本轮产生的 user/assistant 消息序号。
// sequence 就是完整历史数组的下标，因此这里直接用下标即可稳定关联。
func (s *Service) linkRun(ctx context.Context, em *execution.Session, history []*schema.Message) {
	if em == nil || s.executions == nil || len(history) < 2 {
		return
	}
	assistant := int64(len(history) - 1)
	user := assistant
	for i := len(history) - 1; i >= 0; i-- {
		if history[i] != nil && history[i].Role == schema.User {
			user = int64(i)
			break
		}
	}
	_ = s.executions.LinkRunMessage(ctx, em.RunID(), user, assistant)
}

// statusFor 把 Agent 返回值映射为 run 状态与错误码。
func statusFor(err error, approval *agent.ApprovalRequest) (status, code string) {
	switch {
	case approval != nil:
		return execution.StatusAwaitingApproval, ""
	case err == nil:
		return execution.StatusCompleted, ""
	case errors.Is(err, context.Canceled):
		return execution.StatusCancelled, "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return execution.StatusFailed, "timeout"
	default:
		return execution.StatusFailed, "error"
	}
}

// Chat 执行一轮请求；发生 StatefulInterrupt 时保存审批元数据并返回 Approval。
func (s *Service) Chat(ctx context.Context, id, query, skillName string, writer io.Writer) (ChatResult, error) {
	return s.ChatWithSink(ctx, id, query, skillName, writer, nil)
}

// ChatWithSink 与 Chat 相同，但把执行事件实时投递到 sink（WebSocket 队列或内存记录器）。
func (s *Service) ChatWithSink(ctx context.Context, id, query, skillName string, writer io.Writer, sink execution.Sink) (result ChatResult, err error) {
	leave, enterErr := s.enter(ctx)
	if enterErr != nil {
		return ChatResult{}, enterErr
	}
	defer func() { leave(err) }()
	unlock, lockErr := s.acquireSession(ctx, id)
	if lockErr != nil {
		return ChatResult{}, lockErr
	}
	defer unlock()
	// 审批元数据按 owner 分目录：拿别人的 session id 也读不到他的中断现场。
	checks := s.checkpointStoreFor(ctx)
	if pending, err := checks.LoadApproval(id); err != nil {
		return ChatResult{}, err
	} else if pending != nil {
		return ChatResult{SessionID: id, RunID: pending.RunID, Approval: &agent.ApprovalRequest{TargetID: pending.TargetID, ToolName: pending.ToolName, Arguments: pending.Arguments}}, nil
	}
	// 目录查询短路只在调试模式生效：普通用户不应拿到内部技能清单，
	// 这类问题交给模型用能力语言回答（提示词已禁止暴露技能名）。
	if s.debugEnabled() && isSkillCatalogQuery(query) {
		return s.replySkillCatalog(ctx, id, query, writer, sink)
	}
	chat, preloaded, err := s.newAgent(ctx, id, skillName)
	if err != nil {
		return ChatResult{}, err
	}
	ctx, em := s.beginRun(ctx, id, sink, "")
	if em != nil {
		em.Emit(execution.Event{Type: execution.RunStarted, Payload: map[string]any{
			"query_chars": utf8.RuneCountInString(query), "skill_preferred": skillName,
		}})
		if len(preloaded) > 0 {
			em.Emit(execution.Event{Type: execution.SkillPreloaded, Payload: map[string]any{"skill_names": preloaded}})
		}
	}
	err = chat.AskTo(ctx, query, writer)
	var approval *agent.ApprovalRequest
	if errors.As(err, &approval) {
		if saveErr := checks.SaveApproval(id, checkpoint.PendingApproval{TargetID: approval.TargetID, ToolName: approval.ToolName, Arguments: approval.Arguments, RunID: runIDOf(em)}); saveErr != nil {
			finishRun(em, execution.StatusFailed, "save_approval_failed")
			return ChatResult{}, saveErr
		}
		_ = s.save(ctx, id, chat)
		s.linkRun(context.WithoutCancel(ctx), em, chat.History())
		finishRun(em, execution.StatusAwaitingApproval, "")
		return ChatResult{SessionID: id, RunID: runIDOf(em), Approval: approval}, nil
	}
	status, code := statusFor(err, nil)
	if err != nil {
		s.linkRun(context.WithoutCancel(ctx), em, chat.History())
		finishRun(em, status, code)
		return ChatResult{}, err
	}
	if err = s.save(ctx, id, chat); err != nil {
		s.linkRun(context.WithoutCancel(ctx), em, chat.History())
		finishRun(em, execution.StatusFailed, "save_failed")
		return ChatResult{}, err
	}
	s.linkRun(context.WithoutCancel(ctx), em, chat.History())
	finishRun(em, status, code)
	return ChatResult{SessionID: id, RunID: runIDOf(em), TurnID: latestTurn(chat.History())}, nil
}

// Approve 从同一 checkpoint 恢复执行，写操作只有 approved=true 时才会发生。
func (s *Service) Approve(ctx context.Context, id string, approved bool, writer io.Writer) (ChatResult, error) {
	return s.ApproveWithSink(ctx, id, approved, writer, nil)
}

// ApproveWithSink 与 Approve 相同，但把恢复阶段的事件实时投递到 sink。
func (s *Service) ApproveWithSink(ctx context.Context, id string, approved bool, writer io.Writer, sink execution.Sink) (result ChatResult, err error) {
	leave, enterErr := s.enter(ctx)
	if enterErr != nil {
		return ChatResult{}, enterErr
	}
	defer func() { leave(err) }()
	unlock, lockErr := s.acquireSession(ctx, id)
	if lockErr != nil {
		return ChatResult{}, lockErr
	}
	defer unlock()
	checks := s.checkpointStoreFor(ctx)
	pending, err := checks.LoadApproval(id)
	if err != nil {
		return ChatResult{}, err
	}
	if pending == nil {
		return ChatResult{}, fmt.Errorf("session %q has no pending approval", id)
	}
	chat, _, err := s.newAgent(ctx, id, "")
	if err != nil {
		return ChatResult{}, err
	}
	// 续用同一个 run：审批前后的事件属于同一次执行。
	ctx, em := s.beginRun(ctx, id, sink, pending.RunID)
	request := &agent.ApprovalRequest{TargetID: pending.TargetID, ToolName: pending.ToolName, Arguments: pending.Arguments}
	err = chat.ResumeApprovalTo(ctx, request, approved, writer)
	var next *agent.ApprovalRequest
	if errors.As(err, &next) {
		if e := checks.SaveApproval(id, checkpoint.PendingApproval{TargetID: next.TargetID, ToolName: next.ToolName, Arguments: next.Arguments, RunID: runIDOf(em)}); e != nil {
			finishRun(em, execution.StatusFailed, "save_approval_failed")
			return ChatResult{}, e
		}
		s.linkRun(context.WithoutCancel(ctx), em, chat.History())
		finishRun(em, execution.StatusAwaitingApproval, "")
		return ChatResult{SessionID: id, RunID: runIDOf(em), Approval: next}, nil
	}
	status, code := statusFor(err, nil)
	if err != nil {
		s.linkRun(context.WithoutCancel(ctx), em, chat.History())
		finishRun(em, status, code)
		return ChatResult{}, err
	}
	if err = checks.ClearApproval(id); err != nil {
		finishRun(em, execution.StatusFailed, "clear_approval_failed")
		return ChatResult{}, err
	}
	if err = s.save(ctx, id, chat); err != nil {
		s.linkRun(context.WithoutCancel(ctx), em, chat.History())
		finishRun(em, execution.StatusFailed, "save_failed")
		return ChatResult{}, err
	}
	s.linkRun(context.WithoutCancel(ctx), em, chat.History())
	finishRun(em, status, code)
	return ChatResult{SessionID: id, RunID: runIDOf(em), TurnID: latestTurn(chat.History())}, nil
}

func runIDOf(em *execution.Session) string {
	if em == nil {
		return ""
	}
	return em.RunID()
}

func finishRun(em *execution.Session, status, code string) {
	if em == nil {
		return
	}
	em.Finish(status, code)
}

// DeleteExecutions 清理某个会话的执行记录；供删除会话时调用。
func (s *Service) DeleteExecutions(ctx context.Context, id string) error {
	if s.executions == nil {
		return nil
	}
	return s.executions.DeleteBySession(ctx, id)
}

func latestTurn(messages []*schema.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i] != nil && messages[i].Role == schema.User {
			id, _ := messages[i].Extra["memory_turn"].(string)
			return id
		}
	}
	return ""
}
