// Package agent 封装 Eino ADK Agent/Runner，避免控制台直接处理底层事件。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/supervisor"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	modelset "my-eino-app/internal/eino/model"
	"my-eino-app/internal/eino/observability"
	"my-eino-app/internal/eino/prompt"
	toolset "my-eino-app/internal/eino/tool"
	"my-eino-app/internal/execution"
	"my-eino-app/internal/session"
)

// ChatAgent 持有 Runner 和当前会话历史。
// 历史由该对象管理，控制台不再直接维护消息切片。
type ChatAgent struct {
	runner              *adk.Runner
	history             []*schema.Message
	sessionID           string
	persist             func([]*schema.Message) error
	maxMessages         int
	maxChars            int
	dynamicMaxMessages  int
	dynamicMaxChars     int
	contextBudget       session.ContextBudget
	maxSummaryChars     int
	promptReserveTokens int
	// em 是本轮执行事件发射器，由 AskTo/ResumeApprovalTo 从 context 读取。
	em    execution.Emitter
	runID string
	// reasoning 保存推理模型单独返回的思考内容；它与最终回答分开持久化和传输。
	reasoning      string
	debug          bool
	memoryContext  func(context.Context, string) (string, error)
	preflight      string
	promptProfile  string
	promptVersion  string
	promptHash     string
	promptRole     prompt.Role
	toolSetHash    string
	shadowHash     string
	turnID         string
	stepID         string
	attemptID      string
	rolePromptMeta map[prompt.Role]map[string]string
}

func (a *ChatAgent) SetMemoryContext(f func(context.Context, string) (string, error)) {
	a.memoryContext = f
}

// SetPreflightContext injects server-verified context for the current turn.
// It is kept on the per-request agent, so concurrent sessions cannot share it.
func (a *ChatAgent) SetPreflightContext(text string) { a.preflight = strings.TrimSpace(text) }

// PromptMetadata exposes the exact prompt identity selected for this agent.
// The full prompt remains model-internal; only version and hash are suitable
// for execution audit payloads.
func (a *ChatAgent) PromptMetadata() map[string]string {
	meta := map[string]string{"profile": a.promptProfile, "version": a.promptVersion, "hash": a.promptHash, "agent_role": string(a.promptRole), "tool_set_hash": a.toolSetHash}
	if a.shadowHash != "" {
		meta["shadow_prompt_hash"] = a.shadowHash
	}
	return meta
}

// RuntimeScope returns the durable identifiers of the current turn and the
// latest model/tool attempt. It is used when persisting an approval ticket.
func (a *ChatAgent) RuntimeScope() (turnID, stepID, attemptID string) {
	return a.turnID, a.stepID, a.attemptID
}

// RestoreTurn attaches a previously persisted turn to a newly constructed
// Agent. ResumeApprovalTo creates a fresh step and attempt under this turn.
func (a *ChatAgent) RestoreTurn(turnID string) {
	if strings.TrimSpace(turnID) != "" {
		a.turnID = strings.TrimSpace(turnID)
	}
}

func (a *ChatAgent) setScope(turnID, stepID, attemptID string) {
	a.turnID, a.stepID, a.attemptID = turnID, stepID, attemptID
	if scoped, ok := a.em.(execution.ScopedEmitter); ok {
		scoped.SetScope(turnID, stepID, attemptID)
	}
}

func (a *ChatAgent) promptAuditPayload(phase string, contextSnapshot prompt.ContextSnapshot, query string) map[string]any {
	blocks := make([]map[string]any, 0, len(contextSnapshot.Blocks))
	for _, block := range contextSnapshot.Blocks {
		blocks = append(blocks, map[string]any{
			"name": block.Name, "required": block.Required,
			"hash": prompt.HashText(block.Text), "chars": utf8.RuneCountInString(block.Text),
		})
	}
	payload := map[string]any{
		"phase":   phase,
		"profile": a.promptProfile, "prompt_version": a.promptVersion, "prompt_hash": a.promptHash,
		"agent_role": string(a.promptRole), "tool_set_hash": a.toolSetHash,
		"model_request": map[string]any{
			"agent_role": string(a.promptRole), "prompt_version": a.promptVersion,
			"prompt_hash": a.promptHash, "tool_set_hash": a.toolSetHash,
			"turn_id": a.turnID, "step_id": a.stepID, "attempt_id": a.attemptID,
		},
		"context_snapshot": map[string]any{
			"hash": contextSnapshot.Hash, "chars": utf8.RuneCountInString(contextSnapshot.Text),
			"blocks": blocks,
		},
		"user_query_hash": prompt.HashText(query),
	}
	if a.shadowHash != "" {
		payload["shadow_prompt_hash"] = a.shadowHash
	}
	return payload
}

func (a *ChatAgent) emitPromptAudits(phase, query string, snapshot prompt.ContextSnapshot) {
	roles := a.rolePromptMeta
	if len(roles) == 0 {
		roles = map[prompt.Role]map[string]string{a.promptRole: {
			"version": a.promptVersion, "hash": a.promptHash, "tool_set_hash": a.toolSetHash,
		}}
	}
	orderedRoles := make([]prompt.Role, 0, len(roles))
	for role := range roles {
		orderedRoles = append(orderedRoles, role)
	}
	sort.Slice(orderedRoles, func(i, j int) bool { return orderedRoles[i] < orderedRoles[j] })
	for _, role := range orderedRoles {
		meta := roles[role]
		payload := a.promptAuditPayload(phase, snapshot, query)
		payload["agent_role"] = string(role)
		payload["prompt_version"] = meta["version"]
		payload["prompt_hash"] = meta["hash"]
		payload["tool_set_hash"] = meta["tool_set_hash"]
		if request, ok := payload["model_request"].(map[string]any); ok {
			request["agent_role"] = string(role)
			request["prompt_version"] = meta["version"]
			request["prompt_hash"] = meta["hash"]
			request["tool_set_hash"] = meta["tool_set_hash"]
		}
		a.em.Emit(execution.Event{Type: execution.PromptAssembled, Payload: payload})
	}
}

// AppendPreflightContext adds another independent server-verified evidence
// block without allowing later routing rules to erase an earlier block. A
// request may legitimately contain both an uploaded attachment and a private
// knowledge hit, so replacement semantics are unsafe here.
func (a *ChatAgent) AppendPreflightContext(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if a.preflight == "" {
		a.preflight = text
		return
	}
	a.preflight += "\n\n" + text
}

// Respond records a deterministic server response without invoking the model.
// It is used for a knowledge miss where fabricating an answer would be unsafe.
func (a *ChatAgent) Respond(query, answer string) error {
	user := schema.UserMessage(query)
	user.Extra = map[string]any{"memory_turn": uuid.NewString()}
	assistant := schema.AssistantMessage(answer, nil)
	assistant.Extra = map[string]any{"storage_status": "completed", "storage_turn": uuid.NewString()}
	a.history = append(a.history, user, assistant)
	return a.saveHistory()
}

// ApprovalRequest 描述一个等待用户决定的中断点。
type ApprovalRequest struct {
	TargetID  string `json:"target_id"`
	ToolName  string `json:"tool_name"`
	Arguments string `json:"arguments"`
}

func (a *ApprovalRequest) Error() string { return fmt.Sprintf("tool %s requires approval", a.ToolName) }

// New 创建带工具、中间件和流式 Runner 的 ChatModelAgent。
func New(ctx context.Context, cm einomodel.ToolCallingChatModel, debug, multiAgent bool, checkPointStore adk.CheckPointStore, extraTools ...einotool.BaseTool) (*ChatAgent, error) {
	return NewWithInstruction(ctx, cm, debug, multiAgent, checkPointStore, "", extraTools...)
}

// NewWithInstruction 在基础 Prompt 后追加所选 Skill 的业务规则。
func NewWithInstruction(ctx context.Context, cm einomodel.ToolCallingChatModel, debug, multiAgent bool, checkPointStore adk.CheckPointStore, instruction string, extraTools ...einotool.BaseTool) (*ChatAgent, error) {
	return NewWithInstructionProfile(ctx, cm, debug, multiAgent, checkPointStore, instruction, prompt.ProfileLegacy, extraTools...)
}

// NewWithInstructionProfile is the profile-aware constructor used by CLI
// callers that own the complete tool list.
func NewWithInstructionProfile(ctx context.Context, cm einomodel.ToolCallingChatModel, debug, multiAgent bool, checkPointStore adk.CheckPointStore, instruction, profile string, extraTools ...einotool.BaseTool) (*ChatAgent, error) {
	tools, err := ensureWriteNoteTool(ctx, extraTools)
	if err != nil {
		return nil, err
	}
	return NewWithRoleModelsProfile(ctx, RoleModels{Supervisor: cm, Knowledge: cm, Writer: cm}, debug, multiAgent, checkPointStore, instruction, profile, tools...)
}

func ensureWriteNoteTool(ctx context.Context, tools []einotool.BaseTool) ([]einotool.BaseTool, error) {
	for _, registered := range tools {
		info, err := registered.Info(ctx)
		if err != nil {
			return nil, err
		}
		if info.Name == "write_note" {
			return tools, nil
		}
	}
	return append(tools, toolset.NewWriteNoteTool()), nil
}

// RoleModels allows automatic routing to assign models by responsibility.
// Concrete-model mode supplies the same model for all three fields.
type RoleModels struct {
	Supervisor einomodel.ToolCallingChatModel
	Knowledge  einomodel.ToolCallingChatModel
	Writer     einomodel.ToolCallingChatModel
	// ContextWindowTokens and MaxCompletionTokens describe the selected model
	// profile. Zero context means the provider limit is unknown and legacy
	// message/byte compaction is used.
	ContextWindowTokens int
	MaxCompletionTokens int
	// Automatic marks a model chosen by the server's classifier, so the
	// supervisor follows the automatic-routing prompt rather than treating the
	// model as a user-pinned profile.
	Automatic bool
	// SingleModel disables the supervisor/sub-agent graph for a confident fast
	// route. This keeps simple questions to one Qwen call after classification.
	SingleModel bool
	// ModelID is the resolved profile used to reconstruct an approval resume.
	ModelID string
}

// NewWithRoleModels builds the agent graph with explicit per-role models.
func NewWithRoleModels(ctx context.Context, models RoleModels, debug, multiAgent bool, checkPointStore adk.CheckPointStore, instruction string, extraTools ...einotool.BaseTool) (*ChatAgent, error) {
	return NewWithRoleModelsProfile(ctx, models, debug, multiAgent, checkPointStore, instruction, prompt.ProfileLegacy, extraTools...)
}

// NewWithRoleModelsProfile builds the graph with an explicit prompt profile.
// The legacy wrapper above preserves callers that have not opted into v2.
func NewWithRoleModelsProfile(ctx context.Context, models RoleModels, debug, multiAgent bool, checkPointStore adk.CheckPointStore, instruction, profile string, extraTools ...einotool.BaseTool) (*ChatAgent, error) {
	if strings.TrimSpace(profile) == "" {
		profile = prompt.ProfileLegacy
	}
	tools := []einotool.BaseTool{toolset.NewTimeTool()}
	tools = append(tools, extraTools...)
	toolNames := toolNames(ctx, tools)
	renderProfile := profile
	shadowHash := ""
	if prompt.ResolveProfile(profile) == prompt.ProfileV2Shadow {
		renderProfile = prompt.ProfileLegacy
	}
	supervisorInstruction, promptVersion, promptHash := prompt.RenderRole(prompt.RoleSupervisor, renderProfile, instruction, toolNames, models.Automatic)
	rolePromptMeta := map[prompt.Role]map[string]string{
		prompt.RoleSupervisor: {"version": promptVersion, "hash": promptHash, "tool_set_hash": prompt.ToolSetHash(ctx, tools)},
	}
	if prompt.ResolveProfile(profile) == prompt.ProfileV2Shadow {
		_, _, shadowHash = prompt.RenderRole(prompt.RoleSupervisor, prompt.ProfileV2, instruction, toolNames, models.Automatic)
	}
	if models.Automatic {
		// RenderRole includes automatic routing guidance for the supervisor.
	}
	models.Supervisor = modelset.WithRequestMetadata(models.Supervisor, map[string]any{
		"agent_role": string(prompt.RoleSupervisor), "prompt_version": promptVersion,
		"prompt_hash": promptHash, "tool_set_hash": rolePromptMeta[prompt.RoleSupervisor]["tool_set_hash"],
	})
	if multiAgent {
		models.Knowledge = modelset.WithRequestMetadata(models.Knowledge, map[string]any{
			"agent_role": string(prompt.RoleKnowledge), "prompt_version": rolePromptMeta[prompt.RoleKnowledge]["version"],
			"prompt_hash": rolePromptMeta[prompt.RoleKnowledge]["hash"], "tool_set_hash": rolePromptMeta[prompt.RoleKnowledge]["tool_set_hash"],
		})
		models.Writer = modelset.WithRequestMetadata(models.Writer, map[string]any{
			"agent_role": string(prompt.RoleWriter), "prompt_version": rolePromptMeta[prompt.RoleWriter]["version"],
			"prompt_hash": rolePromptMeta[prompt.RoleWriter]["hash"], "tool_set_hash": rolePromptMeta[prompt.RoleWriter]["tool_set_hash"],
		})
	}
	promptReserveTokens := estimatePromptTokens(ctx, supervisorInstruction, tools)
	if multiAgent {
		knowledgeToolsForRole := knowledgeTools(extraTools)
		writerToolsForRole := skillReadTools(extraTools)
		knowledgeInstruction, knowledgeVersion, knowledgeHash := prompt.RenderRole(prompt.RoleKnowledge, renderProfile, instruction, toolNamesFor(ctx, knowledgeToolsForRole), models.Automatic)
		writerInstruction, writerVersion, writerHash := prompt.RenderRole(prompt.RoleWriter, renderProfile, instruction, toolNamesFor(ctx, writerToolsForRole), models.Automatic)
		rolePromptMeta[prompt.RoleKnowledge] = map[string]string{"version": knowledgeVersion, "hash": knowledgeHash, "tool_set_hash": prompt.ToolSetHash(ctx, knowledgeToolsForRole)}
		rolePromptMeta[prompt.RoleWriter] = map[string]string{"version": writerVersion, "hash": writerHash, "tool_set_hash": prompt.ToolSetHash(ctx, writerToolsForRole)}
		promptReserveTokens = max(
			promptReserveTokens,
			estimatePromptTokens(ctx, knowledgeInstruction, knowledgeTools(extraTools)),
			estimatePromptTokens(ctx, writerInstruction, skillReadTools(extraTools)),
		)
		// The concrete instructions are reused below after token accounting.
	}
	a, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "xinghe-system-assistant",
		Description: "星河系统的中文智能助手。",
		Instruction: supervisorInstruction,
		Model:       models.Supervisor,
		// ToolsConfig 把工具定义和统一工具中间件挂到 Agent 的执行循环中。
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools:               tools,
			UnknownToolsHandler: unknownToolHint(tools),
			ToolCallMiddlewares: []compose.ToolMiddleware{safeToolMiddleware(debug, "xinghe-system-assistant")},
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("new agent: %w", err)
	}
	var root adk.Agent = a
	if multiAgent {
		knowledgeInstruction, _, _ := prompt.RenderRole(prompt.RoleKnowledge, renderProfile, instruction, toolNamesFor(ctx, knowledgeTools(extraTools)), models.Automatic)
		// knowledge-agent 专注检索事实，避免主 Agent 同时承担检索和写作职责。
		knowledgeAgent, subErr := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
			Name: "knowledge-agent", Description: "Searches private knowledge and returns grounded facts.",
			Instruction: knowledgeInstruction,
			Model:       models.Knowledge,
			// 子 Agent 也必须挂同一中间件，否则它会绕过执行事件采集。
			ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools:               knowledgeTools(extraTools),
				UnknownToolsHandler: unknownToolHint(knowledgeTools(extraTools)),
				ToolCallMiddlewares: []compose.ToolMiddleware{safeToolMiddleware(debug, "knowledge-agent")},
			}},
		})
		if subErr != nil {
			return nil, fmt.Errorf("new knowledge agent: %w", subErr)
		}
		writerInstruction, _, _ := prompt.RenderRole(prompt.RoleWriter, renderProfile, instruction, toolNamesFor(ctx, skillReadTools(extraTools)), models.Automatic)
		writerAgent, subErr := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
			Name: "writer-agent", Description: "Organizes facts into a clear final answer.",
			Instruction: writerInstruction, Model: models.Writer,
			ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools:               skillReadTools(extraTools),
				UnknownToolsHandler: unknownToolHint(skillReadTools(extraTools)),
				ToolCallMiddlewares: []compose.ToolMiddleware{safeToolMiddleware(debug, "writer-agent")},
			}},
		})
		if subErr != nil {
			return nil, fmt.Errorf("new writer agent: %w", subErr)
		}
		// Supervisor 根据任务选择或协调子 Agent，对控制台仍表现为一个统一 Agent。
		root, err = supervisor.New(ctx, &supervisor.Config{Supervisor: a, SubAgents: []adk.Agent{knowledgeAgent, writerAgent}})
		if err != nil {
			return nil, fmt.Errorf("new supervisor: %w", err)
		}
	}

	return &ChatAgent{
		// 开启流式后，Runner 会通过事件携带模型的 MessageStream。
		runner: adk.NewRunner(ctx, adk.RunnerConfig{
			Agent:           root,
			EnableStreaming: true,
			CheckPointStore: checkPointStore,
		}),
		history:             make([]*schema.Message, 0, 16),
		debug:               debug,
		em:                  execution.Nop(),
		promptReserveTokens: promptReserveTokens,
		promptProfile:       profile,
		promptVersion:       promptVersion,
		promptHash:          promptHash,
		promptRole:          prompt.RoleSupervisor,
		toolSetHash:         prompt.ToolSetHash(ctx, tools),
		shadowHash:          shadowHash,
		rolePromptMeta:      rolePromptMeta,
	}, nil
}

func toolNames(ctx context.Context, tools []einotool.BaseTool) []string {
	return toolNamesFor(ctx, tools)
}

func toolNamesFor(ctx context.Context, tools []einotool.BaseTool) []string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		if info, err := t.Info(ctx); err == nil && info != nil {
			names = append(names, info.Name)
		}
	}
	return names
}

func estimatePromptTokens(ctx context.Context, instruction string, tools []einotool.BaseTool) int {
	total := session.EstimateTextTokens(instruction)
	for _, tool := range tools {
		info, err := tool.Info(ctx)
		if err != nil || info == nil {
			continue
		}
		encoded, err := json.Marshal(info)
		if err == nil {
			total += session.EstimateTextTokens(string(encoded)) + 4
		}
	}
	return total
}

// wrapSkillInstruction 把每轮动态加载的 Skill 与固定系统规则隔离。
// Skill 是业务指导，不能借助拼接文本改变系统规则或扩大工具权限。
func wrapSkillInstruction(instruction string) string {
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		return "<skill_context>\n无额外 Skill 规则。\n</skill_context>"
	}
	return "<skill_context>\n" + instruction + "\n</skill_context>"
}

// unknownToolHint 把「模型调用了不存在的工具」从整轮硬失败降级成一次可自愈的纠正。
//
// 为什么必须处理（2026-09-17 实测，22 题评测日志）：
// 技能目录里每个技能渲染成 `- report_writer: 一句话描述`，这种「名字 + 描述」的排版
// 与工具定义长得一样，模型会直接把技能名当工具调用。框架默认行为是返回
//
//	[NodeRunError] tool report_writer not found in toolsNode indexes
//
// 整轮就此结束（实测 answer_chars=0）—— 模型连改正的机会都没有，用户只看到空回答。
//
// eino 的 UnknownToolsHandler 正是为这种幻觉准备的：返回值会被当作该工具的
// 「执行结果」交回模型，于是模型在同一轮里就能改用 load_skills 重试。
// 相比反复调提示词措辞，这是能自愈的那一半：措辞降低发生概率，兜底保证发生时不致命。
//
// 提示里带上真实工具名，是为了让模型下一步能直接选对，而不是再猜一次。
func unknownToolHint(tools []einotool.BaseTool) func(ctx context.Context, name, input string) (string, error) {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		if info, err := t.Info(context.Background()); err == nil && info.Name != "" {
			names = append(names, info.Name)
		}
	}
	available := strings.Join(names, ", ")
	if available == "" {
		available = "（本轮没有注册任何工具）"
	}
	return func(_ context.Context, name, _ string) (string, error) {
		return fmt.Sprintf(
			"错误：不存在名为 %q 的工具。本轮实际注册的工具只有：%s。\n"+
				"若 %q 是技能目录里的名字，请注意技能名不是工具名 —— 请改用 load_skills，"+
				"把 %q 放进 names 参数里读取该技能，然后按技能规则执行。",
			name, available, name, name), nil
	}
}

// knowledgeTools 限制知识 Agent 只处理检索相关工具，避免它绕过职责执行文件写入。
func knowledgeTools(tools []einotool.BaseTool) []einotool.BaseTool {
	allowed := make([]einotool.BaseTool, 0, len(tools))
	for _, t := range tools {
		info, err := t.Info(context.Background())
		if err != nil {
			continue
		}
		if info.Name == "knowledge_search" || info.Name == "load_skills" {
			allowed = append(allowed, t)
		}
	}
	return allowed
}

// emitter 永不返回 nil，避免调用方到处判空。
func (a *ChatAgent) emitter() execution.Emitter {
	if a.em == nil {
		return execution.Nop()
	}
	return a.em
}

// SetEmitter 显式注入发射器；通常无需调用，AskTo 会从 context 自动读取。
func (a *ChatAgent) SetEmitter(em execution.Emitter) {
	if em == nil {
		em = execution.Nop()
	}
	a.em = em
}

// RunID 返回当前 run 标识；关闭开关时为空串。
func (a *ChatAgent) RunID() string { return a.runID }

// SetHistory 恢复一个已有会话的消息历史。
func (a *ChatAgent) SetHistory(history []*schema.Message) { a.history = history }

// SetPersistence 注入会话存储。保存错误立即返回，不能向用户谎报保存成功。
func (a *ChatAgent) SetPersistence(save func([]*schema.Message) error, maxMessages, maxChars int) {
	a.persist, a.maxMessages, a.maxChars = save, maxMessages, maxChars
}

// SetContextBudget enables per-model token-aware history compaction. A zero
// model window leaves token limiting disabled while preserving legacy caps.
func (a *ChatAgent) SetContextBudget(budget session.ContextBudget, maxSummaryChars int) {
	budget.PromptTokens += a.promptReserveTokens
	a.contextBudget = budget
	a.maxSummaryChars = maxSummaryChars
}

// SetDynamicHistoryCaps limits message count and serialized text size when a
// model-specific token window is active. Legacy caps remain the fallback when
// the selected model's context window is unknown.
func (a *ChatAgent) SetDynamicHistoryCaps(maxMessages, maxChars int) {
	a.dynamicMaxMessages, a.dynamicMaxChars = maxMessages, maxChars
}

func (a *ChatAgent) saveHistory() error {
	if a.persist == nil {
		return nil
	}
	return a.persist(a.history)
}

// finishReply 即使模型失败也保存已生成的内容；不使用已取消的模型请求事务。
func (a *ChatAgent) finishReply(answer string, approval *ApprovalRequest, runErr error) error {
	status := "completed"
	if approval != nil {
		status = "awaiting_approval"
	}
	if runErr != nil {
		status = "failed"
	}
	if errors.Is(runErr, context.Canceled) {
		status = "cancelled"
	}
	message := a.history[len(a.history)-1]
	message.Content = answer
	message.ReasoningContent = a.reasoning
	message.Extra["storage_status"] = status
	saveErr := a.saveHistory()
	if runErr != nil {
		return errors.Join(runErr, saveErr)
	}
	if saveErr != nil {
		return saveErr
	}
	if approval != nil {
		return approval
	}
	return nil
}

// beginReply 在调用模型前持久化助手占位消息，可识别异常退出留下的生成中状态。
func (a *ChatAgent) beginReply() error {
	if len(a.history) > 0 {
		last := a.history[len(a.history)-1]
		if last != nil && last.Role == schema.Assistant && storageStatus(last) == "generating" {
			if last.Extra == nil {
				last.Extra = map[string]any{}
			}
			last.Extra["run_id"] = a.runID
			return a.saveHistory()
		}
	}
	message := schema.AssistantMessage("", nil)
	extra := map[string]any{"storage_status": "generating", "storage_turn": uuid.NewString()}
	// 记录 run_id，刷新后前端可据此把执行记录挂回正确的助手消息。
	if a.runID != "" {
		extra["run_id"] = a.runID
	}
	message.Extra = extra
	a.history = append(a.history, message)
	return a.saveHistory()
}

func storageStatus(message *schema.Message) string {
	if message == nil || message.Extra == nil {
		return ""
	}
	status, _ := message.Extra["storage_status"].(string)
	return status
}

// SetSessionID 同时用作 Runner 的 checkpoint ID。
func (a *ChatAgent) SetSessionID(id string) { a.sessionID = id }

// History 返回当前会话历史，供持久化层保存。
func (a *ChatAgent) History() []*schema.Message { return a.history }

// Ask 提交一轮问题，实时打印 Agent 的流式输出，并把完整回答保存到历史。
func (a *ChatAgent) Ask(ctx context.Context, query string) error {
	return a.AskTo(ctx, query, os.Stdout)
}

// AskTo 与 Ask 相同，但将增量文本写入指定 Writer，供 CLI、HTTP 和 WebSocket 复用。
func (a *ChatAgent) AskTo(ctx context.Context, query string, writer io.Writer) error {
	// 发射器随 context 传入；开关关闭时为 Nop，行为与旧实现一致。
	a.em = execution.FromContext(ctx)
	a.runID = a.em.RunID()
	a.reasoning = ""
	// write_note is approval-gated and only available for an explicit local
	// note/document request. Automatic memory never needs this tool.
	ctx = withWriteNotePermission(ctx, toolset.WriteNoteRequested(query))

	previousLength := len(a.history)
	// Runner 每次需要完整历史；ChatAgent 对外隐藏这部分管理细节。
	userMessage := schema.UserMessage(query)
	turnID := uuid.NewString()
	userMessage.Extra = map[string]any{"memory_turn": turnID}
	a.setScope(turnID, uuid.NewString(), uuid.NewString())
	a.history = append(a.history, userMessage)
	options := []adk.AgentRunOption{}
	if a.sessionID != "" {
		options = append(options, adk.WithCheckPointID(a.sessionID))
	}
	// Prefix data is loaded before compaction so retrieved memory/knowledge is
	// charged against this selected model's input window.
	var memoryMessage *schema.Message
	if a.memoryContext != nil {
		text, err := a.memoryContext(ctx, query)
		if err != nil {
			a.history = a.history[:previousLength]
			return fmt.Errorf("load memory: %w", err)
		}
		memoryHit := memoryContextHasFacts(text)
		memoryPayload := map[string]any{
			"source_type":  "long_term_memory",
			"hit":          memoryHit,
			"hit_count":    memoryContextFactCount(text),
			"context_hash": prompt.HashText(text),
		}
		a.em.Emit(execution.Event{Type: execution.MemoryRetrieved, Payload: memoryPayload})
		if wrapped := prompt.WrapMemoryContext(text); wrapped != "" {
			memoryMessage = schema.SystemMessage(wrapped)
		}
	}
	var prefix []*schema.Message
	var contextBlocks []prompt.ContextBlock
	if a.preflight != "" {
		wrapped := prompt.WrapRuntimeContext(a.preflight)
		prefix = append(prefix, schema.SystemMessage(wrapped))
		contextBlocks = append(contextBlocks, preflightContextBlocks(a.preflight)...)
	}
	if memoryMessage != nil {
		prefix = append(prefix, memoryMessage)
		contextBlocks = append(contextBlocks, prompt.ContextBlock{Name: "memory_context", Order: 200, Text: memoryMessage.Content})
	}
	// Include the user request in the audit snapshot by hash only. The model
	// still receives the normal user message below; duplicating its body in a
	// system block would waste context and could change legacy behavior.
	contextBlocks = append(contextBlocks, prompt.ContextBlock{Name: "user_query", Order: 300, Text: prompt.WrapUserQuery(query), Required: true})
	historyTokenBudget := a.contextBudget.HistoryTokens(prefix)
	maxMessages, maxChars := a.maxMessages, a.maxChars
	if historyTokenBudget >= 0 {
		if a.dynamicMaxMessages > 0 {
			maxMessages = a.dynamicMaxMessages
		}
		if a.dynamicMaxChars > 0 {
			maxChars = a.dynamicMaxChars
		}
	}
	input := session.CompactHistoryWithBudget(a.history, maxMessages, maxChars, historyTokenBudget, a.maxSummaryChars)
	if historyTokenBudget >= 0 && session.EstimateMessagesTokens(input) > historyTokenBudget {
		a.history = a.history[:previousLength]
		return fmt.Errorf("%w: 当前问题及检索上下文超过所选模型的估算输入预算，请缩短问题、减少检索资料或选择更大上下文模型", session.ErrContextBudgetExceeded)
	}
	input = append(prefix, input...)
	contextSnapshotRendered := prompt.RenderContextBlocks(contextBlocks...)
	// Keep one auditable record per model-facing role. Bodies remain private;
	// only hashes, lengths and block hashes enter the execution log.
	a.emitPromptAudits("chat", query, contextSnapshotRendered)
	ctx = execution.WithModelMetadata(ctx, map[string]any{
		"prompt_version": a.promptVersion, "prompt_hash": a.promptHash,
		"agent_role": string(a.promptRole), "tool_set_hash": a.toolSetHash,
		"context_hash": contextSnapshotRendered.Hash,
	})
	if err := a.beginReply(); err != nil {
		a.history = a.history[:previousLength]
		return err
	}
	// 模型调用前先发 model_waiting，前端据此区分「等模型」和「等工具」。
	a.em.Emit(execution.Event{Type: execution.ModelWaiting, Payload: map[string]any{"phase": "chat", "attempt": 1}})
	events := a.runner.Run(ctx, input, options...)
	// 正文与执行事件共用同一发射器，sequence 天然单调且排序稳定。
	answer, approval, err := a.collect(events, execution.NewChunkWriter(a.em, writer))
	return a.finishReply(answer, approval, err)
}

func memoryContextHasFacts(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	return !strings.Contains(trimmed, "<memory_context>[]</memory_context>") && !strings.Contains(trimmed, "<memory_context>\n[]\n</memory_context>")
}

func memoryContextFactCount(text string) int {
	if !memoryContextHasFacts(text) {
		return 0
	}
	return strings.Count(text, `"id"`)
}

// preflightContextBlocks keeps one model-visible runtime message for backward
// compatibility while giving audit/replay a stable classification of the
// evidence inside it. The classification is server-side metadata only.
func preflightContextBlocks(text string) []prompt.ContextBlock {
	blocks := []prompt.ContextBlock{}
	if strings.Contains(text, "<knowledge_preflight") || strings.Contains(text, "<knowledge_results>") {
		blocks = append(blocks, prompt.ContextBlock{Name: "knowledge_context", Order: 120, Text: text})
	}
	if strings.Contains(text, "<untrusted_local_file_evidence>") || strings.Contains(text, "<local_file_preflight") {
		blocks = append(blocks, prompt.ContextBlock{Name: "file_context", Order: 130, Text: text})
	}
	if strings.Contains(text, "<untrusted_attachment_evidence>") {
		blocks = append(blocks, prompt.ContextBlock{Name: "attachment_context", Order: 140, Text: text})
	}
	if len(blocks) == 0 {
		blocks = append(blocks, prompt.ContextBlock{Name: "runtime_context", Order: 100, Text: text})
	}
	return blocks
}

// ResumeApproval 使用同一 checkpoint 恢复被审批中断的执行。
func (a *ChatAgent) ResumeApproval(ctx context.Context, request *ApprovalRequest, approved bool) error {
	return a.ResumeApprovalTo(ctx, request, approved, os.Stdout)
}

// ResumeInterrupted continues a checkpoint after a process interruption using
// Eino's implicit resume strategy. It is only called after recovery confirms
// that no approval is pending.
func (a *ChatAgent) ResumeInterrupted(ctx context.Context, writer io.Writer) error {
	a.em = execution.FromContext(ctx)
	if a.runID == "" {
		a.runID = a.em.RunID()
	}
	a.reasoning = ""
	if a.turnID == "" {
		a.turnID = uuid.NewString()
	}
	a.setScope(a.turnID, uuid.NewString(), uuid.NewString())
	a.emitPromptAudits("resume_interrupted", "", prompt.ContextSnapshot{})
	ctx = execution.WithModelMetadata(ctx, map[string]any{
		"prompt_version": a.promptVersion, "prompt_hash": a.promptHash,
		"agent_role": string(a.promptRole), "tool_set_hash": a.toolSetHash,
	})
	partial := ""
	if len(a.history) > 0 && storageStatus(a.history[len(a.history)-1]) == "generating" {
		partial = a.history[len(a.history)-1].Content
	}
	if err := a.beginReply(); err != nil {
		return err
	}
	a.em.Emit(execution.Event{Type: execution.ModelWaiting, Payload: map[string]any{"phase": "resume_interrupted", "attempt": 1}})
	events, err := a.runner.Resume(ctx, a.sessionID)
	if err != nil {
		return a.finishReply(partial, nil, err)
	}
	answer, approval, err := a.collect(events, execution.NewChunkWriter(a.em, writer))
	answer = partial + answer
	return a.finishReply(answer, approval, err)
}

// ResumeApprovalTo 将恢复后的增量输出写到指定 Writer。
func (a *ChatAgent) ResumeApprovalTo(ctx context.Context, request *ApprovalRequest, approved bool, writer io.Writer) error {
	a.em = execution.FromContext(ctx)
	// A checkpoint can only resume a tool that was explicitly allowed when the
	// original request was made. Preserve permission for the pending write_note
	// invocation while keeping unrelated resumed calls unchanged.
	ctx = withWriteNotePermission(ctx, request != nil && request.ToolName == "write_note")
	if a.runID == "" {
		a.runID = a.em.RunID()
	}
	a.reasoning = ""
	if a.turnID == "" {
		a.turnID = uuid.NewString()
	}
	a.setScope(a.turnID, uuid.NewString(), uuid.NewString())
	a.emitPromptAudits("resume", "", prompt.ContextSnapshot{})
	// TargetID 精确指向产生中断的工具节点，同一会话可从该位置继续运行。
	if err := a.beginReply(); err != nil {
		a.history = a.history[:len(a.history)-1]
		return err
	}
	a.em.Emit(execution.Event{Type: execution.ModelWaiting, Payload: map[string]any{"phase": "resume", "attempt": 1}})
	events, err := a.runner.ResumeWithParams(ctx, a.sessionID, &adk.ResumeParams{Targets: map[string]any{
		request.TargetID: &toolset.ApprovalResult{Approved: approved, Reason: "user rejected"},
	}})
	if err != nil {
		return a.finishReply("", nil, err)
	}
	answer, nextApproval, err := a.collect(events, execution.NewChunkWriter(a.em, writer))
	return a.finishReply(answer, nextApproval, err)
}

func (a *ChatAgent) collect(events *adk.AsyncIterator[*adk.AgentEvent], writer io.Writer) (result string, approval *ApprovalRequest, collectErr error) {
	var answer string
	started := time.Now()
	defer func() {
		if a.debug {
			fmt.Fprintf(os.Stderr, "[agent-output-end] session=%q run=%q duration=%s answer_chars=%d reasoning_chars=%d awaiting_approval=%t err=%s\n", a.sessionID, a.runID, time.Since(started), utf8.RuneCountInString(answer), utf8.RuneCountInString(a.reasoning), approval != nil, observability.Redact(fmt.Sprint(collectErr)))
		}
	}()
	lastSave := time.Now()
	reasoningWriter, _ := writer.(interface{ WriteReasoning([]byte) (int, error) })
	writeReasoning := func(content string) error {
		if content == "" {
			return nil
		}
		a.reasoning += content
		if len(a.history) > 0 {
			a.history[len(a.history)-1].ReasoningContent = a.reasoning
		}
		if reasoningWriter != nil {
			_, err := reasoningWriter.WriteReasoning([]byte(content))
			return err
		}
		return nil
	}
	// 长回答每隔至少三秒随新片段保存一次；不逐字写库，也不需要后台定时任务。
	saveProgress := func() error {
		if time.Since(lastSave) < 3*time.Second || a.persist == nil {
			return nil
		}
		a.history[len(a.history)-1].Content = answer
		if err := a.saveHistory(); err != nil {
			return err
		}
		lastSave = time.Now()
		return nil
	}
	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if event == nil {
			continue
		}
		if event.Err != nil {
			return answer, nil, event.Err
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			// 只把根因中断转换成控制台审批请求，忽略框架向上传播的重复中断。
			for _, interrupt := range event.Action.Interrupted.InterruptContexts {
				if !interrupt.IsRootCause {
					continue
				}
				if info, ok := interrupt.Info.(*toolset.ApprovalInfo); ok {
					// 审批暂停不是失败：发审批事件，不补工具终态，也不发 run 终态。
					a.emitter().Emit(execution.Event{Type: execution.ApprovalRequired, Payload: map[string]any{
						"tool_name": info.ToolName, "tool_call_id": interrupt.ID,
					}})
					return answer, &ApprovalRequest{TargetID: interrupt.ID, ToolName: info.ToolName, Arguments: info.ArgumentsInJSON}, nil
				}
			}
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}

		output := event.Output.MessageOutput
		if output.IsStreaming {
			finishReason := "unknown"
			bodyChars, reasoningChars, chunks := 0, 0, 0
			streamStarted := time.Now()
			logStreamEnd := func(err error) {
				payload := map[string]any{"agent_name": event.AgentName, "finish_reason": finishReason}
				if err != nil {
					payload["error"] = execution.DigestArgs(err.Error())
				}
				a.em.Emit(execution.Event{Type: execution.ModelCompleted, Payload: payload})
				if a.debug {
					fmt.Fprintf(os.Stderr, "[model-stream-end] session=%q run=%q agent=%q role=%q read_duration=%s chunks=%d answer_chars=%d reasoning_chars=%d finish_reason=%q err=%s\n", a.sessionID, a.runID, event.AgentName, output.Role, time.Since(streamStarted), chunks, bodyChars, reasoningChars, finishReason, observability.Redact(fmt.Sprint(err)))
				}
			}
			// 流式事件需要继续读取 MessageStream 才能获得文本片段。
			for {
				chunk, err := output.MessageStream.Recv()
				if err == io.EOF {
					logStreamEnd(nil)
					break
				}
				if err != nil {
					logStreamEnd(err)
					return answer, nil, err
				}
				if chunk != nil {
					chunks++
					bodyChars += utf8.RuneCountInString(chunk.Content)
					reasoningChars += utf8.RuneCountInString(chunk.ReasoningContent)
					if chunk.ResponseMeta != nil && chunk.ResponseMeta.FinishReason != "" {
						finishReason = chunk.ResponseMeta.FinishReason
					}
				}
				if chunk != nil && output.Role == schema.Assistant {
					if err := writeReasoning(chunk.ReasoningContent); err != nil {
						return answer, nil, err
					}
					answer += chunk.Content
					if err := saveProgress(); err != nil {
						return answer, nil, err
					}
					if _, err := io.WriteString(writer, chunk.Content); err != nil {
						return answer, nil, err
					}
				}
			}
		} else if output.Message != nil && output.Message.Role == schema.Assistant {
			if a.debug {
				finishReason := "unknown"
				if output.Message.ResponseMeta != nil && output.Message.ResponseMeta.FinishReason != "" {
					finishReason = output.Message.ResponseMeta.FinishReason
				}
				fmt.Fprintf(os.Stderr, "[model-message-end] session=%q run=%q agent=%q answer_chars=%d reasoning_chars=%d finish_reason=%q\n", a.sessionID, a.runID, event.AgentName, utf8.RuneCountInString(output.Message.Content), utf8.RuneCountInString(output.Message.ReasoningContent), finishReason)
			}
			if err := writeReasoning(output.Message.ReasoningContent); err != nil {
				return answer, nil, err
			}
			if output.Message.Content == "" {
				continue
			}
			// 工具结果等事件可能以完整消息形式返回。
			answer += output.Message.Content
			if _, err := io.WriteString(writer, output.Message.Content); err != nil {
				return answer, nil, err
			}
		}
	}

	return answer, nil, nil
}

// 写作子 Agent 仅增加技能读取能力，不扩大其业务工具权限。
func skillReadTools(tools []einotool.BaseTool) []einotool.BaseTool {
	var readers []einotool.BaseTool
	for _, t := range tools {
		info, err := t.Info(context.Background())
		if err == nil && info.Name == "load_skills" {
			readers = append(readers, t)
		}
	}
	return readers
}
