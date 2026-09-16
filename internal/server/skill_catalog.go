package server

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/cloudwego/eino/schema"
	"my-eino-app/internal/execution"
)

// 只匹配独立的目录询问，不截走包含创建、执行或其他附加要求的任务。
var skillCatalogQuery = regexp.MustCompile(`^(?:(?:请|请问|你|你们|目前|现在|当前|都|已注册|可用|支持|的|我想知道|告诉我)\s*)*(?:有什么技能|有哪些技能|有什么skills|有哪些skills|技能列表|技能目录|列出技能|列出所有技能|列出可用技能|查看技能|查看所有技能|list skills|list available skills|what skills are available)[？?。.!！\s]*$`)

func isSkillCatalogQuery(query string) bool {
	return skillCatalogQuery.MatchString(strings.ToLower(strings.TrimSpace(query)))
}

// 目录来自磁盘注册信息，不经过模型、技能正文预加载或子 Agent。
// 同样保存会话并发送 chunk，HTTP 与 WebSocket 使用同一份答案。
func (s *Service) replySkillCatalog(ctx context.Context, id, query string, writer io.Writer, sink execution.Sink) (ChatResult, error) {
	names, err := s.skills.List()
	if err != nil {
		return ChatResult{}, err
	}
	var answer strings.Builder
	if len(names) == 0 {
		answer.WriteString("当前没有注册技能。")
	} else {
		fmt.Fprintf(&answer, "当前注册了 %d 个技能：\n\n", len(names))
		for _, name := range names {
			entry, err := s.skills.Load(name)
			if err != nil {
				return ChatResult{}, err
			}
			fmt.Fprintf(&answer, "- **%s**：%s\n", entry.Name, strings.Join(strings.Fields(entry.Description), " "))
		}
		answer.WriteString("\n以上是技能规则目录；实际执行能力取决于当前接入的工具，注册技能不代表已经具备其中提到的插件或桌面操作能力。")
	}
	history, err := s.sessions.Load(id)
	if err != nil {
		return ChatResult{}, err
	}
	ctx, em := s.beginRun(ctx, id, sink, "")
	if em != nil {
		em.Emit(execution.Event{Type: execution.RunStarted})
	}
	message := schema.AssistantMessage(answer.String(), nil)
	message.Extra = map[string]any{"storage_status": "completed"}
	if em != nil {
		message.Extra["run_id"] = em.RunID()
	}
	history = append(history, schema.UserMessage(query), message)
	if err = s.sessions.Save(id, history); err != nil {
		finishRun(em, execution.StatusFailed, "save_failed")
		return ChatResult{}, err
	}
	s.linkRun(ctx, em, history)
	_, err = io.WriteString(execution.NewChunkWriter(execution.FromContext(ctx), writer), answer.String())
	status, code := statusFor(err, nil)
	finishRun(em, status, code)
	return ChatResult{SessionID: id, RunID: runIDOf(em), Answer: answer.String()}, err
}
