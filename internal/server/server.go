package server

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/lyymini/gotems/internal/agent"
	"github.com/lyymini/gotems/internal/cost"
	"github.com/lyymini/gotems/internal/ratelimit"
	"github.com/lyymini/gotems/internal/task"
	"github.com/lyymini/gotems/pkg/schema"
)

//go:embed static
var staticFS embed.FS

// Server 是 GoTems 的 Web 仪表盘 HTTP 服务器
type Server struct {
	mux         *http.ServeMux
	agents      map[string]agent.Agent
	taskPool    *task.Pool
	costTracker *cost.Tracker
	breaker     *ratelimit.Breaker
	eventBus    *EventBus
	logger      *slog.Logger
}

// Config Web 服务器配置
type Config struct {
	Addr        string
	Agents      map[string]agent.Agent
	TaskPool    *task.Pool
	CostTracker *cost.Tracker
	Breaker     *ratelimit.Breaker
}

// New 创建 Web 服务器
func New(cfg Config, logger *slog.Logger) *Server {
	s := &Server{
		mux:         http.NewServeMux(),
		agents:      cfg.Agents,
		taskPool:    cfg.TaskPool,
		costTracker: cfg.CostTracker,
		breaker:     cfg.Breaker,
		eventBus:    NewEventBus(),
		logger:      logger,
	}
	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	// API 路由
	s.mux.HandleFunc("GET /api/agents", s.handleAgents)
	s.mux.HandleFunc("GET /api/tasks", s.handleTasks)
	s.mux.HandleFunc("GET /api/cost", s.handleCost)
	s.mux.HandleFunc("GET /api/breaker", s.handleBreaker)
	s.mux.HandleFunc("GET /api/events", s.handleSSE)
	s.mux.HandleFunc("POST /api/run", s.handleRun)

	// 静态文件
	staticContent, err := fs.Sub(staticFS, "static")
	if err != nil {
		s.logger.Error("failed to load static files", "error", err)
		return
	}
	s.mux.Handle("GET /", http.FileServer(http.FS(staticContent)))
}

// Start 启动 HTTP 服务器
func (s *Server) Start(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:         addr,
		Handler:      s.corsMiddleware(s.mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	s.logger.Info("web dashboard starting", "addr", addr)
	return srv.ListenAndServe()
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- API Handlers ---

func (s *Server) handleAgents(w http.ResponseWriter, _ *http.Request) {
	infos := make([]map[string]any, 0, len(s.agents))
	for _, a := range s.agents {
		infos = append(infos, map[string]any{
			"id":           a.ID(),
			"provider":     a.Provider(),
			"model":        a.Model(),
			"status":       a.Status().String(),
			"capabilities": a.Capabilities(),
		})
	}
	writeJSON(w, infos)
}

func (s *Server) handleTasks(w http.ResponseWriter, _ *http.Request) {
	if s.taskPool == nil {
		writeJSON(w, []any{})
		return
	}
	writeJSON(w, s.taskPool.All())
}

func (s *Server) handleCost(w http.ResponseWriter, _ *http.Request) {
	if s.costTracker == nil {
		writeJSON(w, map[string]string{"message": "not configured"})
		return
	}
	writeJSON(w, s.costTracker.Summarize())
}

func (s *Server) handleBreaker(w http.ResponseWriter, _ *http.Request) {
	if s.breaker == nil {
		writeJSON(w, map[string]any{})
		return
	}
	writeJSON(w, s.breaker.Status())
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prompt   string `json:"prompt"`
		Provider string `json:"provider,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.Prompt == "" {
		http.Error(w, `{"error":"prompt is required"}`, http.StatusBadRequest)
		return
	}

	// 选择 Agent
	var selected agent.Agent
	for _, a := range s.agents {
		if req.Provider != "" && string(a.Provider()) != req.Provider {
			continue
		}
		if a.Status() == agent.StatusIdle {
			selected = a
			break
		}
	}
	if selected == nil {
		http.Error(w, `{"error":"no available agent"}`, http.StatusServiceUnavailable)
		return
	}

	t := &task.Task{
		ID:     fmt.Sprintf("web-%d", time.Now().UnixNano()),
		Prompt: req.Prompt,
	}

	s.eventBus.Publish(Event{Type: "task_started", Data: map[string]any{
		"task_id": t.ID, "agent": selected.ID(), "prompt": req.Prompt,
	}})

	result, err := selected.Execute(r.Context(), t)
	if err != nil {
		s.eventBus.Publish(Event{Type: "task_failed", Data: map[string]any{
			"task_id": t.ID, "error": err.Error(),
		}})
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	s.eventBus.Publish(Event{Type: "task_completed", Data: map[string]any{
		"task_id": t.ID, "agent": selected.ID(),
		"tokens_in": result.TokensIn, "tokens_out": result.TokensOut,
	}})

	writeJSON(w, result)
}

// handleSSE 服务端推送事件，用于仪表盘实时更新
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := s.eventBus.Subscribe()
	defer s.eventBus.Unsubscribe(ch)

	// 初始状态推送
	s.sendSSE(w, flusher, Event{Type: "connected", Data: map[string]any{
		"agents": len(s.agents), "time": time.Now().Format(time.RFC3339),
	}})

	for {
		select {
		case <-r.Context().Done():
			return
		case evt := <-ch:
			s.sendSSE(w, flusher, evt)
		}
	}
}

func (s *Server) sendSSE(w http.ResponseWriter, flusher http.Flusher, evt Event) {
	data, _ := json.Marshal(evt)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Type, string(data))
	flusher.Flush()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// --- SSE Event Bus ---

// Event SSE 事件
type Event struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

// EventBus SSE 事件总线
type EventBus struct {
	mu   sync.RWMutex
	subs []chan Event
}

// NewEventBus 创建事件总线
func NewEventBus() *EventBus {
	return &EventBus{}
}

// Publish 发布事件到所有订阅者
func (b *EventBus) Publish(evt Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- evt:
		default:
		}
	}
}

// Subscribe 订阅事件流
func (b *EventBus) Subscribe() chan Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan Event, 50)
	b.subs = append(b.subs, ch)
	return ch
}

// Unsubscribe 取消订阅
func (b *EventBus) Unsubscribe(ch chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, sub := range b.subs {
		if sub == ch {
			b.subs = append(b.subs[:i], b.subs[i+1:]...)
			close(ch)
			break
		}
	}
}

// 避免 schema 包未使用的编译警告
var _ = schema.Result{}
