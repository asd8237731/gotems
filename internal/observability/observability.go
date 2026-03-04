package observability

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Metrics 收集 Prometheus 风格的指标
type Metrics struct {
	mu sync.RWMutex

	// 计数器
	TasksTotal       atomic.Int64
	TasksSucceeded   atomic.Int64
	TasksFailed      atomic.Int64
	TokensInTotal    atomic.Int64
	TokensOutTotal   atomic.Int64

	// 按 Provider 分组的计数器
	byProvider map[string]*ProviderMetrics

	logger *slog.Logger
}

// ProviderMetrics 每个 Provider 的指标
type ProviderMetrics struct {
	Requests     atomic.Int64
	Failures     atomic.Int64
	TokensIn     atomic.Int64
	TokensOut    atomic.Int64
	TotalLatency atomic.Int64 // 累计延迟（毫秒）
}

// NewMetrics 创建指标收集器
func NewMetrics(logger *slog.Logger) *Metrics {
	return &Metrics{
		byProvider: make(map[string]*ProviderMetrics),
		logger:     logger,
	}
}

// RecordTask 记录任务执行指标
func (m *Metrics) RecordTask(provider string, success bool, tokensIn, tokensOut int, latency time.Duration) {
	m.TasksTotal.Add(1)
	if success {
		m.TasksSucceeded.Add(1)
	} else {
		m.TasksFailed.Add(1)
	}
	m.TokensInTotal.Add(int64(tokensIn))
	m.TokensOutTotal.Add(int64(tokensOut))

	pm := m.getOrCreateProvider(provider)
	pm.Requests.Add(1)
	if !success {
		pm.Failures.Add(1)
	}
	pm.TokensIn.Add(int64(tokensIn))
	pm.TokensOut.Add(int64(tokensOut))
	pm.TotalLatency.Add(latency.Milliseconds())
}

func (m *Metrics) getOrCreateProvider(provider string) *ProviderMetrics {
	m.mu.RLock()
	pm, ok := m.byProvider[provider]
	m.mu.RUnlock()
	if ok {
		return pm
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	pm, ok = m.byProvider[provider]
	if ok {
		return pm
	}
	pm = &ProviderMetrics{}
	m.byProvider[provider] = pm
	return pm
}

// Snapshot 返回指标快照（用于 Prometheus 导出）
type MetricsSnapshot struct {
	TasksTotal     int64                       `json:"tasks_total"`
	TasksSucceeded int64                       `json:"tasks_succeeded"`
	TasksFailed    int64                       `json:"tasks_failed"`
	TokensIn       int64                       `json:"tokens_in_total"`
	TokensOut      int64                       `json:"tokens_out_total"`
	ByProvider     map[string]ProviderSnapshot `json:"by_provider"`
}

// ProviderSnapshot 单个 Provider 的指标快照
type ProviderSnapshot struct {
	Requests      int64   `json:"requests"`
	Failures      int64   `json:"failures"`
	TokensIn      int64   `json:"tokens_in"`
	TokensOut     int64   `json:"tokens_out"`
	AvgLatencyMs  float64 `json:"avg_latency_ms"`
}

// Snapshot 返回当前指标快照
func (m *Metrics) Snapshot() MetricsSnapshot {
	snap := MetricsSnapshot{
		TasksTotal:     m.TasksTotal.Load(),
		TasksSucceeded: m.TasksSucceeded.Load(),
		TasksFailed:    m.TasksFailed.Load(),
		TokensIn:       m.TokensInTotal.Load(),
		TokensOut:      m.TokensOutTotal.Load(),
		ByProvider:     make(map[string]ProviderSnapshot),
	}

	m.mu.RLock()
	for provider, pm := range m.byProvider {
		reqs := pm.Requests.Load()
		avgLatency := float64(0)
		if reqs > 0 {
			avgLatency = float64(pm.TotalLatency.Load()) / float64(reqs)
		}
		snap.ByProvider[provider] = ProviderSnapshot{
			Requests:     reqs,
			Failures:     pm.Failures.Load(),
			TokensIn:     pm.TokensIn.Load(),
			TokensOut:    pm.TokensOut.Load(),
			AvgLatencyMs: avgLatency,
		}
	}
	m.mu.RUnlock()

	return snap
}

// PrometheusText 返回 Prometheus exposition 格式文本
func (m *Metrics) PrometheusText() string {
	snap := m.Snapshot()
	var b strings.Builder
	b.Grow(2048) // 预分配容量

	b.WriteString("# HELP gotems_tasks_total Total number of tasks executed\n")
	b.WriteString("# TYPE gotems_tasks_total counter\n")
	fmt.Fprintf(&b, "gotems_tasks_total %d\n", snap.TasksTotal)

	b.WriteString("# HELP gotems_tasks_succeeded_total Successful tasks\n")
	b.WriteString("# TYPE gotems_tasks_succeeded_total counter\n")
	fmt.Fprintf(&b, "gotems_tasks_succeeded_total %d\n", snap.TasksSucceeded)

	b.WriteString("# HELP gotems_tasks_failed_total Failed tasks\n")
	b.WriteString("# TYPE gotems_tasks_failed_total counter\n")
	fmt.Fprintf(&b, "gotems_tasks_failed_total %d\n", snap.TasksFailed)

	b.WriteString("# HELP gotems_tokens_in_total Total input tokens\n")
	b.WriteString("# TYPE gotems_tokens_in_total counter\n")
	fmt.Fprintf(&b, "gotems_tokens_in_total %d\n", snap.TokensIn)

	b.WriteString("# HELP gotems_tokens_out_total Total output tokens\n")
	b.WriteString("# TYPE gotems_tokens_out_total counter\n")
	fmt.Fprintf(&b, "gotems_tokens_out_total %d\n", snap.TokensOut)

	b.WriteString("# HELP gotems_provider_requests_total Requests per provider\n")
	b.WriteString("# TYPE gotems_provider_requests_total counter\n")
	for provider, ps := range snap.ByProvider {
		fmt.Fprintf(&b, "gotems_provider_requests_total{provider=%q} %d\n", provider, ps.Requests)
	}

	b.WriteString("# HELP gotems_provider_failures_total Failures per provider\n")
	b.WriteString("# TYPE gotems_provider_failures_total counter\n")
	for provider, ps := range snap.ByProvider {
		fmt.Fprintf(&b, "gotems_provider_failures_total{provider=%q} %d\n", provider, ps.Failures)
	}

	b.WriteString("# HELP gotems_provider_avg_latency_ms Average latency per provider\n")
	b.WriteString("# TYPE gotems_provider_avg_latency_ms gauge\n")
	for provider, ps := range snap.ByProvider {
		fmt.Fprintf(&b, "gotems_provider_avg_latency_ms{provider=%q} %.2f\n", provider, ps.AvgLatencyMs)
	}

	return b.String()
}

// --- OpenTelemetry 链路追踪 ---

// Tracer 封装 OpenTelemetry 链路追踪
type Tracer struct {
	tp     *sdktrace.TracerProvider
	tracer trace.Tracer
	logger *slog.Logger
}

// NewTracer 创建链路追踪器（stdout 导出，生产环境可替换为 OTLP）
func NewTracer(serviceName string, logger *slog.Logger) (*Tracer, error) {
	exporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
	if err != nil {
		return nil, fmt.Errorf("create trace exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(nil),
	)
	otel.SetTracerProvider(tp)

	return &Tracer{
		tp:     tp,
		tracer: tp.Tracer(serviceName),
		logger: logger,
	}, nil
}

// StartSpan 开始一个追踪 span
func (t *Tracer) StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return t.tracer.Start(ctx, name, trace.WithAttributes(attrs...))
}

// TaskSpan 为任务执行创建 span
func (t *Tracer) TaskSpan(ctx context.Context, taskID, agentID, provider string) (context.Context, trace.Span) {
	return t.StartSpan(ctx, "task.execute",
		attribute.String("task.id", taskID),
		attribute.String("agent.id", agentID),
		attribute.String("agent.provider", provider),
	)
}

// Shutdown 关闭追踪器
func (t *Tracer) Shutdown(ctx context.Context) error {
	return t.tp.Shutdown(ctx)
}
