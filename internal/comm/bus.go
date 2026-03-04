package comm

import (
	"sync"
	"sync/atomic"

	"github.com/lyymini/gotems/pkg/schema"
)

// Bus 是通信总线接口，支持不同的底层实现
type Bus interface {
	// Publish 发布消息到指定主题
	Publish(topic string, msg *schema.Message) error
	// Subscribe 订阅指定主题
	Subscribe(topic string) (<-chan *schema.Message, error)
	// Unsubscribe 取消指定主题的订阅
	Unsubscribe(topic string, ch <-chan *schema.Message)
	// Close 关闭总线
	Close() error
}

// ChannelBus 基于 Go channel 的进程内通信总线
type ChannelBus struct {
	mu     sync.RWMutex
	topics map[string][]chan *schema.Message
	closed atomic.Bool
}

// NewChannelBus 创建基于 channel 的通信总线
func NewChannelBus() *ChannelBus {
	return &ChannelBus{
		topics: make(map[string][]chan *schema.Message),
	}
}

func (b *ChannelBus) Publish(topic string, msg *schema.Message) error {
	if b.closed.Load() {
		return nil
	}
	b.mu.RLock()
	// 在读锁保护下遍历并发送，防止 Close() 并发关闭 channel 导致 panic
	subs := b.topics[topic]
	for _, ch := range subs {
		select {
		case ch <- msg:
		default:
			// 订阅者满了就跳过
		}
	}
	b.mu.RUnlock()
	return nil
}

func (b *ChannelBus) Subscribe(topic string) (<-chan *schema.Message, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan *schema.Message, 100)
	b.topics[topic] = append(b.topics[topic], ch)
	return ch, nil
}

// Unsubscribe 取消指定主题的订阅
func (b *ChannelBus) Unsubscribe(topic string, target <-chan *schema.Message) {
	b.mu.Lock()
	defer b.mu.Unlock()
	subs := b.topics[topic]
	newSubs := make([]chan *schema.Message, 0, len(subs))
	for _, ch := range subs {
		if (<-chan *schema.Message)(ch) == target {
			close(ch)
			continue
		}
		newSubs = append(newSubs, ch)
	}
	b.topics[topic] = newSubs
}

func (b *ChannelBus) Close() error {
	b.closed.Store(true)
	b.mu.Lock()
	defer b.mu.Unlock()
	for topic, subs := range b.topics {
		for _, ch := range subs {
			close(ch)
		}
		delete(b.topics, topic)
	}
	return nil
}
