package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"my-eino-app/internal/execution"
)

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && strings.EqualFold(parsed.Host, r.Host)
}}

// wsConn 串行化所有写操作，避免多个 goroutine 并发写同一连接。
type wsConn struct {
	conn   *websocket.Conn
	mu     sync.Mutex
	closed atomic.Bool
}

func (c *wsConn) writeJSON(value any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed.Load() {
		return net.ErrClosed
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.conn.WriteJSON(value)
}

func (c *wsConn) close() {
	if c.closed.CompareAndSwap(false, true) {
		_ = c.conn.Close()
	}
}

// wsRequest 是入站帧。旧客户端不带 type，因此空 type 等同 chat。
type wsRequest struct {
	Type string `json:"type"`
	// SessionID 仅为兼容旧客户端的报文结构而保留，取值一律忽略：
	// 会话由连接建立时的 handshake（或服务端生成）确定，见 handleWebSocket。
	SessionID string `json:"session_id"`
	Query     string `json:"query"`
	Skill     string `json:"skill"`
}

// directChunkWriter 在未开启执行事件时沿用旧协议，直接把正文写成 chunk 帧。
type directChunkWriter struct{ conn *wsConn }

func (w directChunkWriter) Write(p []byte) (int, error) {
	if err := w.conn.writeJSON(map[string]any{"type": "chunk", "content": string(p)}); err != nil {
		return 0, err
	}
	return len(p), nil
}

// WriteReasoning 兼容执行事件关闭时的旧 WebSocket 路径；思考内容单独成帧，
// 不污染历史消息中的最终回答正文。
func (w directChunkWriter) WriteReasoning(p []byte) (int, error) {
	if err := w.conn.writeJSON(map[string]any{"type": "reasoning", "content": string(p)}); err != nil {
		return 0, err
	}
	return len(p), nil
}

// queueSink 把事件投递到出站队列，由单一写循环串行消费。
type queueSink struct{ queue *execution.Queue }

func (q queueSink) Push(ev execution.Event) { q.queue.Push(ev) }

// frameFor 把事件转换为出站帧。正文沿用旧的 chunk 帧，其余走统一的 event 帧。
func frameFor(ev execution.Event, dropped bool) map[string]any {
	if ev.Type == execution.Chunk {
		frameType := "chunk"
		if ev.Content == "" && ev.ReasoningContent != "" {
			frameType = "reasoning"
		}
		frame := map[string]any{"type": frameType, "content": ev.Content, "run_id": ev.RunID, "sequence": ev.Sequence}
		if ev.ReasoningContent != "" {
			frame["reasoning_content"] = ev.ReasoningContent
		}
		if dropped {
			frame["dropped"] = true
		}
		return frame
	}
	ev.Dropped = dropped
	return map[string]any{"type": "event", "event": ev}
}

// handleWebSocket 使用「单写循环 + 独立读循环」结构：
// 读循环只解析入站帧（chat 或 cancel），每轮执行放在独立 goroutine 中，
// 因此取消、断线和超时都能及时到达后端，而不是等待 Chat 返回。
func (s *Service) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// session id 在升级之前定下来：归属他人时用普通 HTTP 403 回绝。
	// 升级之后再报错就只能走 WebSocket 错误帧，调用方拿不到状态码，也不好区分
	// 「越权」和「服务端故障」。合法路径只有两种：调用方带来服务端签发过的 id，
	// 或者留空由服务端生成。
	id, resolveErr := s.resolveSessionID(r.Context(), r.URL.Query().Get("session_id"))
	if resolveErr != nil {
		writeSessionError(w, resolveErr)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &wsConn{conn: conn}
	defer c.close()

	// ready 帧携带 debug 状态：前端据此决定是否显示技能选择器与技能事件。
	if err := c.writeJSON(map[string]any{"type": "ready", "session_id": id, "debug": s.debugEnabled()}); err != nil {
		return
	}

	queueSize := execution.DefaultQueueSize
	if s.execCfg.Enabled && s.execCfg.QueueSize > 0 {
		queueSize = s.execCfg.QueueSize
	}
	queue := execution.NewQueue(queueSize)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		queue.Range(func(ev execution.Event, dropped bool) error {
			return c.writeJSON(frameFor(ev, dropped))
		})
	}()

	baseCtx, cancelAll := context.WithCancel(r.Context())
	defer cancelAll()

	var turnMu sync.Mutex
	var cancelTurn context.CancelFunc
	turnActive := false
	var turns sync.WaitGroup

	for {
		var req wsRequest
		if err := conn.ReadJSON(&req); err != nil {
			break
		}
		switch req.Type {
		case "cancel":
			turnMu.Lock()
			if cancelTurn != nil {
				cancelTurn()
			}
			turnMu.Unlock()
		case "", "chat":
			// 帧内不再接受 session_id：连接建立时就已确定（步骤 2.7）。
			// 「帧内还能覆盖 id」是原来的越权入口之一，保留字段只为兼容旧客户端的
			// 报文结构，取值一律忽略。
			if strings.TrimSpace(req.Query) == "" {
				continue
			}
			turnMu.Lock()
			if turnActive {
				turnMu.Unlock()
				_ = c.writeJSON(map[string]any{"type": "error", "error": "上一轮仍在执行，请先发送 cancel"})
				continue
			}
			turnActive = true
			turnCtx, cancel := context.WithCancel(baseCtx)
			cancelTurn = cancel
			turnMu.Unlock()

			sessionID := id
			turns.Add(1)
			go func() {
				defer turns.Done()
				defer func() {
					turnMu.Lock()
					turnActive = false
					cancelTurn = nil
					turnMu.Unlock()
				}()
				s.runTurn(turnCtx, c, queue, sessionID, req)
			}()
		}
	}

	cancelAll()
	drained := make(chan struct{})
	go func() { turns.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
	}
	queue.Close()
	<-writerDone
}

// runTurn 执行一轮对话，并保证终态事件先于控制帧写出。
func (s *Service) runTurn(ctx context.Context, c *wsConn, queue *execution.Queue, sessionID string, req wsRequest) {
	var writer io.Writer
	if !s.execCfg.Enabled {
		// 未开启执行事件：沿用旧的直接 chunk 写入，行为与 P7 完全一致。
		writer = directChunkWriter{conn: c}
	}
	result, err := s.ChatWithSink(ctx, sessionID, req.Query, s.requestedSkill(req.Skill), writer, queueSink{queue})
	// 同步点保证本轮已发出的事件全部写连接，避免 done 抢在终态之前。
	queue.Sync()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			_ = c.writeJSON(map[string]any{"type": "cancelled", "session_id": sessionID})
			return
		}
		_ = c.writeJSON(map[string]any{"type": "error", "error": err.Error()})
		return
	}
	if result.Approval != nil {
		_ = c.writeJSON(map[string]any{"type": "approval", "approval": result.Approval, "run_id": result.RunID, "turn_id": result.TurnID})
		return
	}
	_ = c.writeJSON(map[string]any{"type": "done", "session_id": sessionID, "run_id": result.RunID, "turn_id": result.TurnID})
}
