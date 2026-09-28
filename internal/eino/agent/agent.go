// Package agent 封装 Eino ADK Agent/Runner，避免控制台直接处理底层事件。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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
	reasoning     string
	debug         bool
	memoryContext func(context.Context, string) (string, error)
	preflight     string
}

func (a *ChatAgent) SetMemoryContext(f func(context.Context, string) (string, error)) {
	a.memoryContext = f
}

// SetPreflightContext injects server-verified context for the current turn.
// It is kept on the per-request agent, so concurrent sessions cannot share it.
func (a *ChatAgent) SetPreflightContext(text string) { a.preflight = strings.TrimSpace(text) }

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
	tools, err := ensureWriteNoteTool(ctx, extraTools)
	if err != nil {
		return nil, err
	}
	return NewWithRoleModels(ctx, RoleModels{Supervisor: cm, Knowledge: cm, Writer: cm}, debug, multiAgent, checkPointStore, instruction, tools...)
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
}

// NewWithRoleModels builds the agent graph with explicit per-role models.
func NewWithRoleModels(ctx context.Context, models RoleModels, debug, multiAgent bool, checkPointStore adk.CheckPointStore, instruction string, extraTools ...einotool.BaseTool) (*ChatAgent, error) {
	tools := []einotool.BaseTool{toolset.NewTimeTool()}
	tools = append(tools, extraTools...)
	runtimeInstruction := wrapSkillInstruction(instruction)
	supervisorInstruction := prompt.SystemInstruction + "\n" + runtimeInstruction
	if models.Automatic {
		supervisorInstruction += `

<model_routing>
服务端已经先用 Qwen 9B 完成本轮难度分类，并为本轮绑定了最终模型。不要再次选择模型，也不要为了形式而重复委派。
简单问答直接回答；涉及私有资料先委派 knowledge-agent；复杂分析、长文组织、方案比较或取得检索结果后需要成稿时，才委派 writer-agent 输出最终答案。
</model_routing>`
	}
	promptReserveTokens := estimatePromptTokens(ctx, supervisorInstruction, tools)
	if multiAgent {
		promptReserveTokens = max(
			promptReserveTokens,
			estimatePromptTokens(ctx, prompt.KnowledgeAgentInstruction+"\n"+runtimeInstruction, knowledgeTools(extraTools)),
			estimatePromptTokens(ctx, prompt.WriterAgentInstruction+"\n"+runtimeInstruction, skillReadTools(extraTools)),
		)
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
		// knowledge-agent 专注检索事实，避免主 Agent 同时承担检索和写作职责。
		knowledgeAgent, subErr := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
			Name: "knowledge-agent", Description: "Searches private knowledge and returns grounded facts.",
			Instruction: prompt.KnowledgeAgentInstruction + "\n" + runtimeInstruction,
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
		writerAgent, subErr := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
			Name: "writer-agent", Description: "Organizes facts into a clear final answer.",
			Instruction: prompt.WriterAgentInstruction + "\n" + runtimeInstruction, Model: models.Writer,
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
	}, nil
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
	userMessage.Extra = map[string]any{"memory_turn": uuid.NewString()}
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
		memoryMessage = schema.SystemMessage(text)
	}
	var prefix []*schema.Message
	if a.preflight != "" {
		prefix = append(prefix, schema.SystemMessage(a.preflight))
	}
	if memoryMessage != nil {
		prefix = append(prefix, memoryMessage)
	}
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

// ResumeApproval 使用同一 checkpoint 恢复被审批中断的执行。
func (a *ChatAgent) ResumeApproval(ctx context.Context, request *ApprovalRequest, approved bool) error {
	return a.ResumeApprovalTo(ctx, request, approved, os.Stdout)
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
