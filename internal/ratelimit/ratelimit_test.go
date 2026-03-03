package ratelimit

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestLimiterWait(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	l := NewLimiter(logger)
	l.Configure(LimiterConfig{Provider: "claude", RPS: 100, Burst: 10})

	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if err := l.Wait(ctx, "claude"); err != nil {
			t.Fatalf("wait should succeed: %v", err)
		}
	}
}

func TestLimiterAllow(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	l := NewLimiter(logger)
	l.Configure(LimiterConfig{Provider: "gemini", RPS: 1, Burst: 1})

	// 第一次应该允许
	if !l.Allow("gemini") {
		t.Fatal("first call should be allowed")
	}
	// 未配置的 provider 应直接通过
	if !l.Allow("unknown") {
		t.Fatal("unconfigured provider should be allowed")
	}
}

func TestLimiterContextCancel(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	l := NewLimiter(logger)
	l.Configure(LimiterConfig{Provider: "openai", RPS: 0.1, Burst: 1})

	ctx := context.Background()
	// 消耗掉突发配额
	l.Wait(ctx, "openai")

	// 带超时的 context 应该报错
	ctx2, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := l.Wait(ctx2, "openai"); err == nil {
		t.Fatal("should fail with context timeout")
	}
}

func TestBreakerOpenAndRecover(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	b := NewBreaker(3, 100*time.Millisecond, logger)

	// 初始应允许
	if !b.Allow("claude") {
		t.Fatal("should allow initially")
	}

	// 连续失败 3 次触发熔断
	b.RecordFailure("claude")
	b.RecordFailure("claude")
	b.RecordFailure("claude")

	if b.Allow("claude") {
		t.Fatal("should be open after 3 failures")
	}

	// 等待冷却期
	time.Sleep(150 * time.Millisecond)

	// 冷却后应进入半开状态，允许一次
	if !b.Allow("claude") {
		t.Fatal("should allow after cooldown (half-open)")
	}

	// 成功后应恢复
	b.RecordSuccess("claude")
	if !b.Allow("claude") {
		t.Fatal("should allow after success")
	}
}

func TestBreakerStatus(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	b := NewBreaker(2, time.Hour, logger)

	b.RecordFailure("gemini")
	b.RecordFailure("gemini")

	status := b.Status()
	if s, ok := status["gemini"]; !ok || s.State != "open" {
		t.Fatalf("expected open state, got %+v", status["gemini"])
	}
}
