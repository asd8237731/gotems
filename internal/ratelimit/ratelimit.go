package ratelimit

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter 为每个 Provider 提供独立的限流控制
type Limiter struct {
	mu       sync.RWMutex
	limiters map[string]*rate.Limiter // provider -> limiter
	logger   *slog.Logger
}

// LimiterConfig 单个 Provider 的限流配置
type LimiterConfig struct {
	Provider string
	RPS      float64       // 每秒请求数
	Burst    int           // 突发上限
	Timeout  time.Duration // 等待超时
}

// NewLimiter 创建限流器
func NewLimiter(logger *slog.Logger) *Limiter {
	return &Limiter{
		limiters: make(map[string]*rate.Limiter),
		logger:   logger,
	}
}

// Configure 为指定 Provider 配置限流参数
func (l *Limiter) Configure(cfg LimiterConfig) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.limiters[cfg.Provider] = rate.NewLimiter(rate.Limit(cfg.RPS), cfg.Burst)
	l.logger.Info("rate limiter configured",
		"provider", cfg.Provider,
		"rps", cfg.RPS,
		"burst", cfg.Burst,
	)
}

// Wait 在调用 API 之前等待令牌，受 context 超时控制
func (l *Limiter) Wait(ctx context.Context, provider string) error {
	l.mu.RLock()
	limiter, ok := l.limiters[provider]
	l.mu.RUnlock()
	if !ok {
		return nil // 未配置限流则直接通过
	}
	if err := limiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limit wait for %s: %w", provider, err)
	}
	return nil
}

// Allow 非阻塞检查是否允许请求
func (l *Limiter) Allow(provider string) bool {
	l.mu.RLock()
	limiter, ok := l.limiters[provider]
	l.mu.RUnlock()
	if !ok {
		return true
	}
	return limiter.Allow()
}

// Breaker 熔断器，当某个 Provider 连续失败超过阈值时自动断开
type Breaker struct {
	mu        sync.Mutex
	states    map[string]*breakerState
	threshold int           // 连续失败阈值
	cooldown  time.Duration // 熔断冷却时间
	logger    *slog.Logger
}

type breakerState struct {
	failures    int
	lastFailure time.Time
	open        bool // true = 熔断中
}

// NewBreaker 创建熔断器
func NewBreaker(threshold int, cooldown time.Duration, logger *slog.Logger) *Breaker {
	return &Breaker{
		states:    make(map[string]*breakerState),
		threshold: threshold,
		cooldown:  cooldown,
		logger:    logger,
	}
}

// Allow 检查 Provider 是否允许调用
func (b *Breaker) Allow(provider string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	s, ok := b.states[provider]
	if !ok {
		return true
	}
	if !s.open {
		return true
	}
	// 检查冷却期是否已过
	if time.Since(s.lastFailure) > b.cooldown {
		s.open = false
		s.failures = 0
		b.logger.Info("breaker half-open, allowing retry", "provider", provider)
		return true
	}
	return false
}

// RecordSuccess 记录成功调用
func (b *Breaker) RecordSuccess(provider string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if s, ok := b.states[provider]; ok {
		s.failures = 0
		s.open = false
	}
}

// RecordFailure 记录失败调用
func (b *Breaker) RecordFailure(provider string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	s, ok := b.states[provider]
	if !ok {
		s = &breakerState{}
		b.states[provider] = s
	}

	s.failures++
	s.lastFailure = time.Now()

	if s.failures >= b.threshold {
		s.open = true
		b.logger.Warn("breaker opened",
			"provider", provider,
			"failures", s.failures,
			"cooldown", b.cooldown,
		)
	}
}

// Status 返回所有 Provider 的熔断状态
func (b *Breaker) Status() map[string]BreakerStatus {
	b.mu.Lock()
	defer b.mu.Unlock()

	result := make(map[string]BreakerStatus)
	for provider, s := range b.states {
		status := "closed"
		if s.open {
			if time.Since(s.lastFailure) > b.cooldown {
				status = "half-open"
			} else {
				status = "open"
			}
		}
		result[provider] = BreakerStatus{
			State:       status,
			Failures:    s.failures,
			LastFailure: s.lastFailure,
		}
	}
	return result
}

// BreakerStatus 熔断器状态信息
type BreakerStatus struct {
	State       string    `json:"state"` // closed, open, half-open
	Failures    int       `json:"failures"`
	LastFailure time.Time `json:"last_failure"`
}
