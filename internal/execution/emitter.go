package execution

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Emitter 是一轮 run 的事件出口。它负责补齐信封字段并分配单调递增的 sequence。
// 正文 chunk 与执行事件共用同一个 Emitter，因此排序是结构保证而不是约定。
type Emitter interface {
	RunID() string
	SessionID() string
	NextSequence() int64
	Emit(Event)
}

// Sink 是事件的实时出口（WebSocket 队列、HTTP 内存记录器或自定义消费者）。
type Sink interface {
	Push(Event)
}

type nopEmitter struct{}

func (nopEmitter) RunID() string       { return "" }
func (nopEmitter) SessionID() string   { return "" }
func (nopEmitter) NextSequence() int64 { return 0 }
func (nopEmitter) Emit(Event)          {}

// Nop 返回一个什么都不做的发射器：开关关闭或控制台模式下使用。
// FromContext 永不返回 nil，因此调用方无需判空。
func Nop() Emitter { return nopEmitter{} }

type Session struct {
	runID     string
	sessionID string
	seq       atomic.Int64
	sink      Sink
	store     Store

	persist   chan Event
	closeOnce sync.Once
	wg        sync.WaitGroup

	emitted atomic.Int64
	limit   int64
	closed  atomic.Bool
}

// SessionOptions 控制会话发射器的持久化行为。
type SessionOptions struct {
	// MaxEventsPerRun 限制单个 run 入库的事件数量；<=0 表示不限制。
	MaxEventsPerRun int
	// StartSequence 是恢复执行时的已有最大序号；新 run 传 0。
	StartSequence int64
	// BatchSize 与 FlushInterval 控制入库批次，避免阻塞模型调用。
	BatchSize     int
	FlushInterval time.Duration
}

// NewSession 创建会话级发射器。store 与 sink 均可为 nil。
func NewSession(runID, sessionID string, store Store, sink Sink, options SessionOptions) *Session {
	s := &Session{runID: runID, sessionID: sessionID, sink: sink, store: store}
	s.seq.Store(options.StartSequence)
	s.limit = int64(options.MaxEventsPerRun)
	if s.limit <= 0 {
		s.limit = 5000
	}
	if store != nil {
		size := options.BatchSize
		if size <= 0 {
			size = 64
		}
		s.persist = make(chan Event, size*2)
		interval := options.FlushInterval
		if interval <= 0 {
			interval = 500 * time.Millisecond
		}
		s.wg.Add(1)
		go s.flushLoop(size, interval)
	}
	return s
}

func (s *Session) RunID() string     { return s.runID }
func (s *Session) SessionID() string { return s.sessionID }

// NextSequence 返回下一个 run 内唯一的序号，正文和事件共用。
func (s *Session) NextSequence() int64 { return s.seq.Add(1) }

// Emit 补齐字段后立刻投递给实时出口，并把事件异步排队入库。
// 入库走独立 goroutine，模型调用不会因数据库写入而阻塞。
func (s *Session) Emit(ev Event) {
	if s == nil || s.closed.Load() {
		return
	}
	if !ev.Type.Known() {
		// 未知类型只可能来自外部构造；不投递、不入库，避免污染契约。
		return
	}
	s.prepare(&ev)
	if s.sink != nil {
		s.sink.Push(ev)
	}
	if s.store == nil || s.persist == nil {
		return
	}
	if s.emitted.Add(1) > s.limit {
		// 超过单 run 上限后停止入库，但实时进度不受影响。
		return
	}
	select {
	case s.persist <- ev:
	default:
		// 入库队列已满时丢弃中间事件，保证不阻塞调用方。
	}
}

// prepare 补齐信封中缺失的字段，并保证 sequence 单调。
func (s *Session) prepare(ev *Event) {
	if ev.Sequence <= 0 {
		ev.Sequence = s.NextSequence()
	}
	if ev.Version == 0 {
		ev.Version = Version
	}
	if ev.EventID == "" {
		ev.EventID = uuid.NewString()
	}
	if ev.RunID == "" {
		ev.RunID = s.runID
	}
	if ev.SessionID == "" {
		ev.SessionID = s.sessionID
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now().UTC()
	}
}

// Close 刷出剩余事件并停止后台写入。可重复调用。
func (s *Session) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		if s.persist != nil {
			close(s.persist)
		}
	})
	s.wg.Wait()
}

// Finish 写入 run 终态并落库，最后刷出所有待写入事件。
// status 为 StatusAwaitingApproval 时不发终态（审批暂停不是结束）。
func (s *Session) Finish(status, errorCode string) {
	if s == nil {
		return
	}
	switch status {
	case StatusCompleted:
		s.Emit(Event{Type: RunCompleted})
	case StatusCancelled:
		payload := map[string]any{}
		if errorCode != "" {
			payload["reason"] = errorCode
		}
		s.Emit(Event{Type: RunCancelled, Payload: payload})
	case StatusFailed:
		payload := map[string]any{}
		if errorCode != "" {
			payload["error_code"] = errorCode
		}
		s.Emit(Event{Type: RunFailed, Payload: payload})
	default:
		// awaiting_approval：不写终态，也不写结束时间。
	}
	if s.store != nil && status != StatusAwaitingApproval {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.store.FinishRun(ctx, s.runID, status)
		cancel()
	}
	s.Close()
}

func (s *Session) flushLoop(batchSize int, interval time.Duration) {
	defer s.wg.Done()
	batch := make([]Event, 0, batchSize)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.store.AppendEvents(ctx, s.runID, batch)
		cancel()
		batch = batch[:0]
	}
	for {
		select {
		case ev, ok := <-s.persist:
			if !ok {
				flush()
				return
			}
			batch = append(batch, ev)
			if len(batch) >= batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// Recorder 把事件留在内存中，供 HTTP 同步接口一次性返回。
type Recorder struct {
	mu     sync.Mutex
	events []Event
	limit  int
}

// NewRecorder 创建内存事件记录器；limit<=0 时使用 5000 条上限。
func NewRecorder(limit int) *Recorder {
	if limit <= 0 {
		limit = 5000
	}
	return &Recorder{limit: limit}
}

func (r *Recorder) Push(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) >= r.limit {
		return
	}
	r.events = append(r.events, ev)
}

// Events 返回已记录事件的副本。
func (r *Recorder) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.events))
	copy(out, r.events)
	return out
}

type emitterKey struct{}

// WithEmitter 把发射器挂到 context 上，供中间件和 Agent 读取。
func WithEmitter(ctx context.Context, em Emitter) context.Context {
	if em == nil {
		return ctx
	}
	return context.WithValue(ctx, emitterKey{}, em)
}

// FromContext 读取当前发射器；缺失时返回 Nop，永不返回 nil。
func FromContext(ctx context.Context) Emitter {
	if ctx == nil {
		return Nop()
	}
	if em, ok := ctx.Value(emitterKey{}).(Emitter); ok && em != nil {
		return em
	}
	return Nop()
}

// Bag 是一组工具执行期间的结构化标注，按工具调用隔离，支持并发。
type Bag struct {
	mu     sync.Mutex
	values map[string]any
}

// NewBag 创建标注容器。
func NewBag() *Bag { return &Bag{values: map[string]any{}} }

// Set 写入一个标注，值必须可 JSON 序列化。
func (b *Bag) Set(key string, value any) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.values[key] = value
}

// MergeInto 把标注复制到目标 payload 中。
func (b *Bag) MergeInto(into map[string]any) {
	if b == nil || into == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, v := range b.values {
		into[k] = v
	}
}

type bagKey struct{}

// WithBag 把一个标注容器挂到 context 上。中间件在调用工具前调用它，
// 从而让工具的 Annotate 与中间件读取到同一份数据，且在并发工具调用间互相隔离。
func WithBag(ctx context.Context, b *Bag) context.Context {
	if b == nil {
		return ctx
	}
	return context.WithValue(ctx, bagKey{}, b)
}

// Annotate 记录工具的结构化结果。没有容器时静默忽略，工具无需感知开关状态。
func Annotate(ctx context.Context, kv map[string]any) {
	if ctx == nil || len(kv) == 0 {
		return
	}
	b, ok := ctx.Value(bagKey{}).(*Bag)
	if !ok || b == nil {
		return
	}
	for k, v := range kv {
		b.Set(k, v)
	}
}

// MergeAnnotations 把当前容器中的标注写入 payload；没有容器时不做任何事。
func MergeAnnotations(ctx context.Context, into map[string]any) {
	if ctx == nil {
		return
	}
	if b, ok := ctx.Value(bagKey{}).(*Bag); ok {
		b.MergeInto(into)
	}
}

// ChunkWriter 把正文增量同时投递为 chunk 事件并写入兜底 Writer。
// 关闭开关时 em 为 Nop，行为与旧实现完全一致。
type ChunkWriter struct {
	em       Emitter
	fallback io.Writer
}

// NewChunkWriter 创建正文写入器。fallback 可为 nil。
func NewChunkWriter(em Emitter, fallback io.Writer) *ChunkWriter {
	return &ChunkWriter{em: em, fallback: fallback}
}

func (w *ChunkWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.em.Emit(Event{Type: Chunk, Content: string(p)})
	if w.fallback != nil {
		return w.fallback.Write(p)
	}
	return len(p), nil
}

// WriteReasoning 投递模型单独返回的推理增量。支持该方法的 fallback（如 WebSocket
// 兼容写入器）可以实时展示思考；普通 io.Writer 会安全忽略该附加通道。
func (w *ChunkWriter) WriteReasoning(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.em.Emit(Event{Type: Chunk, ReasoningContent: string(p)})
	if rw, ok := w.fallback.(interface{ WriteReasoning([]byte) (int, error) }); ok {
		return rw.WriteReasoning(p)
	}
	return len(p), nil
}
