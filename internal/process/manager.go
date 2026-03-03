package process

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"
)

// State 子进程状态
type State int

const (
	StateIdle     State = iota // 空闲（未启动）
	StateStarting              // 启动中
	StateRunning               // 运行中
	StateStopping              // 停止中
	StateStopped               // 已停止
	StateError                 // 异常
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateStopping:
		return "stopping"
	case StateStopped:
		return "stopped"
	case StateError:
		return "error"
	default:
		return "unknown"
	}
}

// Config 子进程配置
type Config struct {
	Binary  string            // 可执行文件路径
	Args    []string          // 启动参数
	WorkDir string            // 工作目录
	Env     map[string]string // 额外环境变量
	Timeout time.Duration     // 执行超时（0 = 无限）
}

// OutputLine 子进程输出的一行
type OutputLine struct {
	Stream    string    // "stdout" 或 "stderr"
	Content   string    // 行内容
	Timestamp time.Time // 时间戳
}

// Process 封装一个子进程的完整生命周期
type Process struct {
	mu        sync.RWMutex
	cfg       Config
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	stderr    io.ReadCloser
	state     State
	pid       int
	exitCode  int
	startTime time.Time
	stopTime  time.Time
	output    []OutputLine // 缓存输出
	logger    *slog.Logger
}

// NewProcess 创建子进程封装
func NewProcess(cfg Config, logger *slog.Logger) *Process {
	return &Process{
		cfg:    cfg,
		state:  StateIdle,
		output: make([]OutputLine, 0, 256),
		logger: logger,
	}
}

// Start 启动子进程
func (p *Process) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.state == StateRunning {
		return fmt.Errorf("process already running (pid=%d)", p.pid)
	}

	p.state = StateStarting

	cmd := exec.CommandContext(ctx, p.cfg.Binary, p.cfg.Args...)
	if p.cfg.WorkDir != "" {
		cmd.Dir = p.cfg.WorkDir
	}

	// 合并环境变量
	cmd.Env = os.Environ()
	for k, v := range p.cfg.Env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		p.state = StateError
		return fmt.Errorf("create stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		p.state = StateError
		return fmt.Errorf("create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		p.state = StateError
		return fmt.Errorf("create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		p.state = StateError
		return fmt.Errorf("start process %s: %w", p.cfg.Binary, err)
	}

	p.cmd = cmd
	p.stdin = stdin
	p.stdout = stdout
	p.stderr = stderr
	p.pid = cmd.Process.Pid
	p.startTime = time.Now()
	p.state = StateRunning
	p.output = p.output[:0]

	p.logger.Info("process started",
		"binary", p.cfg.Binary,
		"pid", p.pid,
		"work_dir", p.cfg.WorkDir,
	)

	return nil
}

// Stop 优雅停止子进程
func (p *Process) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.state != StateRunning {
		return nil
	}

	p.state = StateStopping

	// 先关闭 stdin
	if p.stdin != nil {
		_ = p.stdin.Close()
	}

	// 发送 SIGTERM
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Signal(os.Interrupt)
	}

	// 等待退出（最多 5 秒，否则 kill）
	done := make(chan error, 1)
	go func() {
		done <- p.cmd.Wait()
	}()

	select {
	case err := <-done:
		p.stopTime = time.Now()
		p.state = StateStopped
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				p.exitCode = exitErr.ExitCode()
			}
		}
		p.logger.Info("process stopped", "pid", p.pid, "exit_code", p.exitCode)
		return nil
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		p.stopTime = time.Now()
		p.state = StateStopped
		p.logger.Warn("process killed after timeout", "pid", p.pid)
		return nil
	}
}

// WriteStdin 向子进程的 stdin 写入数据
func (p *Process) WriteStdin(data []byte) error {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.state != StateRunning || p.stdin == nil {
		return fmt.Errorf("process not running")
	}

	_, err := p.stdin.Write(data)
	return err
}

// StreamOutput 流式读取子进程的 stdout 和 stderr，通过 channel 返回
func (p *Process) StreamOutput(ctx context.Context) <-chan OutputLine {
	ch := make(chan OutputLine, 256)

	var wg sync.WaitGroup

	// 读取 stdout
	p.mu.RLock()
	stdout := p.stdout
	stderr := p.stderr
	p.mu.RUnlock()

	if stdout != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.scanStream(ctx, stdout, "stdout", ch)
		}()
	}

	// 读取 stderr
	if stderr != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.scanStream(ctx, stderr, "stderr", ch)
		}()
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	return ch
}

// CollectOutput 收集子进程全部输出直到进程退出
func (p *Process) CollectOutput(ctx context.Context) (string, string, error) {
	var stdoutBuf, stderrBuf []byte
	var wg sync.WaitGroup
	var stdoutErr, stderrErr error

	p.mu.RLock()
	stdout := p.stdout
	stderr := p.stderr
	p.mu.RUnlock()

	if stdout != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stdoutBuf, stdoutErr = io.ReadAll(stdout)
		}()
	}

	if stderr != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stderrBuf, stderrErr = io.ReadAll(stderr)
		}()
	}

	wg.Wait()

	if stdoutErr != nil {
		return "", "", fmt.Errorf("read stdout: %w", stdoutErr)
	}
	if stderrErr != nil {
		return "", "", fmt.Errorf("read stderr: %w", stderrErr)
	}

	return string(stdoutBuf), string(stderrBuf), nil
}

// Wait 等待子进程退出
func (p *Process) Wait() error {
	p.mu.RLock()
	cmd := p.cmd
	p.mu.RUnlock()

	if cmd == nil {
		return fmt.Errorf("process not started")
	}

	err := cmd.Wait()

	p.mu.Lock()
	p.stopTime = time.Now()
	p.state = StateStopped
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			p.exitCode = exitErr.ExitCode()
		}
	}
	p.mu.Unlock()

	return err
}

// State 返回当前状态
func (p *Process) State() State {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.state
}

// PID 返回进程 ID
func (p *Process) PID() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pid
}

// Output 返回已缓存的输出
func (p *Process) Output() []OutputLine {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]OutputLine, len(p.output))
	copy(out, p.output)
	return out
}

func (p *Process) scanStream(ctx context.Context, r io.Reader, stream string, ch chan<- OutputLine) {
	scanner := bufio.NewScanner(r)
	// 增大缓冲区以支持大行输出（如 JSON）
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line := OutputLine{
			Stream:    stream,
			Content:   scanner.Text(),
			Timestamp: time.Now(),
		}

		// 缓存到 Process
		p.mu.Lock()
		p.output = append(p.output, line)
		p.mu.Unlock()

		// 发送到 channel
		select {
		case ch <- line:
		case <-ctx.Done():
			return
		}
	}
}

// Manager 管理多个子进程
type Manager struct {
	mu        sync.RWMutex
	processes map[string]*Process
	logger    *slog.Logger
}

// NewManager 创建子进程管理器
func NewManager(logger *slog.Logger) *Manager {
	return &Manager{
		processes: make(map[string]*Process),
		logger:    logger,
	}
}

// Create 创建并注册一个子进程（不启动）
func (m *Manager) Create(id string, cfg Config) *Process {
	p := NewProcess(cfg, m.logger)
	m.mu.Lock()
	m.processes[id] = p
	m.mu.Unlock()
	return p
}

// Get 获取已注册的子进程
func (m *Manager) Get(id string) *Process {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.processes[id]
}

// StopAll 停止所有子进程
func (m *Manager) StopAll() {
	m.mu.RLock()
	ps := make([]*Process, 0, len(m.processes))
	for _, p := range m.processes {
		ps = append(ps, p)
	}
	m.mu.RUnlock()

	for _, p := range ps {
		if p.State() == StateRunning {
			_ = p.Stop()
		}
	}
}

// Remove 移除已停止的子进程
func (m *Manager) Remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.processes[id]; ok {
		if p.State() == StateRunning {
			_ = p.Stop()
		}
		delete(m.processes, id)
	}
}

// All 返回所有子进程状态
func (m *Manager) All() map[string]State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]State, len(m.processes))
	for id, p := range m.processes {
		result[id] = p.State()
	}
	return result
}
