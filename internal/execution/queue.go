package execution

import (
	"sync"
	"sync/atomic"
	"time"
)

// DefaultQueueSize 是 WebSocket 出站队列的默认容量。
const DefaultQueueSize = 256

type queueItem struct {
	ev   Event
	ack  chan struct{}
	sync bool
}

// Queue 是有界事件队列，由一个写循环串行消费。
//
// 背压策略（对应路线图 P8-3）：
//   - 必须送达：正文 chunk 与终态事件（RunCompleted/RunFailed/RunCancelled）。
//     正文丢失会让回答不完整，终态丢失则前端无法收尾；二者在队列满时先丢弃一条
//     最旧的中间事件腾出空间，绝不静默成功。
//   - 可丢弃：其余中间事件（等待模型、工具开始等）在队列满时直接丢弃，
//     并给下一条成功写出的事件打 dropped 标记，前端凭 after_sequence 补取。
type Queue struct {
	ch   chan queueItem
	done chan struct{}
	once sync.Once
	cap  int
	// drops 置位后，下一条成功写出的事件会带 dropped 标记。
	drops atomic.Bool
}

// NewQueue 创建有界队列；size<=0 时使用 DefaultQueueSize。
func NewQueue(size int) *Queue {
	if size <= 0 {
		size = DefaultQueueSize
	}
	return &Queue{ch: make(chan queueItem, size), done: make(chan struct{}), cap: size}
}

// Push 实现 Sink：非阻塞投递一条事件。正文与终态必须送达，其余中间事件可丢。
func (q *Queue) Push(ev Event) {
	if q == nil {
		return
	}
	if ev.Type.Terminal() || ev.Type == Chunk {
		q.enqueue(queueItem{ev: ev})
		return
	}
	select {
	case q.ch <- queueItem{ev: ev}:
	case <-q.done:
	default:
		// 中间事件可以丢；置位后下一条成功写出的事件会带 dropped 标记。
		q.drops.Store(true)
	}
}

// Sync 插入一个同步点并等待消费端处理完毕。
// 调用方据此保证「已发出的所有事件都已写连接」，再发送 done/approval 控制帧。
func (q *Queue) Sync() {
	if q == nil {
		return
	}
	ack := make(chan struct{})
	q.enqueue(queueItem{ack: ack, sync: true})
	select {
	case <-ack:
	case <-q.done:
	case <-time.After(3 * time.Second):
	}
}

// enqueue 阻塞入队，必要时丢弃最旧的中间事件为终态让路。
func (q *Queue) enqueue(it queueItem) {
	attempts := 0
	for {
		select {
		case q.ch <- it:
			return
		case <-q.done:
			closeAck(it)
			return
		default:
		}
		if attempts >= q.cap+2 {
			// 极端情况下消费端已停止；标记丢弃，绝不静默成功。
			q.drops.Store(true)
			closeAck(it)
			return
		}
		// 队列满：丢弃最旧的一条为终态或同步点腾出空间。
		select {
		case <-q.ch:
			q.drops.Store(true)
		default:
		}
		attempts++
	}
}

func closeAck(it queueItem) {
	if it.ack != nil {
		close(it.ack)
	}
}

// Range 串行消费队列直到 Close。handle 返回错误时停止消费。
func (q *Queue) Range(handle func(ev Event, dropped bool) error) {
	if q == nil {
		return
	}
	for {
		select {
		case it := <-q.ch:
			if it.sync {
				closeAck(it)
				continue
			}
			dropped := q.drops.Swap(false)
			if err := handle(it.ev, dropped); err != nil {
				closeAck(it)
				// 写连接失败：先唤醒等待同步点的调用方，再退出。
				q.Close()
				q.drain()
				return
			}
			closeAck(it)
		case <-q.done:
			q.drain()
			return
		}
	}
}

// drain 唤醒所有等待同步点的调用方，避免连接关闭时调用方卡住。
func (q *Queue) drain() {
	for {
		select {
		case it := <-q.ch:
			closeAck(it)
		default:
			return
		}
	}
}

// Close 停止消费。可重复调用。
func (q *Queue) Close() {
	if q == nil {
		return
	}
	q.once.Do(func() { close(q.done) })
}

// Size 返回队列容量，供测试与文档使用。
func (q *Queue) Size() int { return q.cap }
