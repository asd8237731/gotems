package comm

import (
	"sync"

	"github.com/lyymini/gotems/pkg/schema"
)

// Bus 是通信总线接口，支持不同的底层实现
type Bus interface {
	// Publish 发布消息到指定主题
	Publish(topic string, msg *schema.Message) error
	// Subscribe 订阅指定主题
	Subscribe(topic string) (<-chan *schema.Message, error)
	// Close 关闭总线
	Close() error
}

// ChannelBus 基于 Go channel 的进程内通信总线
type ChannelBus struct {
	mu     sync.RWMutex
	topics map[string][]chan *schema.Message
}

// NewChannelBus 创建基于 channel 的通信总线
func NewChannelBus() *ChannelBus {
	return &ChannelBus{
		topics: make(map[string][]chan *schema.Message),
	}
}

func (b *ChannelBus) Publish(topic string, msg *schema.Message) error {
	b.mu.RLock()
	subs := b.topics[topic]
	b.mu.RUnlock()
	for _, ch := range subs {
		select {
		case ch <- msg:
		default:
			// 订阅者满了就跳过
		}
	}
	return nil
}

func (b *ChannelBus) Subscribe(topic string) (<-chan *schema.Message, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan *schema.Message, 100)
	b.topics[topic] = append(b.topics[topic], ch)
	return ch, nil
}

func (b *ChannelBus) Close() error {
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
