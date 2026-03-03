package workspace

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Workspace 管理 Agent 的隔离工作空间（基于 git worktree）
type Workspace struct {
	mu       sync.RWMutex
	baseDir  string            // 主仓库路径
	worktrees map[string]*Worktree // agentID -> worktree
	logger   *slog.Logger
}

// Worktree 一个 git worktree 实例
type Worktree struct {
	AgentID string `json:"agent_id"`
	Branch  string `json:"branch"`
	Path    string `json:"path"`
	Active  bool   `json:"active"`
}

// NewWorkspace 创建工作空间管理器
func NewWorkspace(baseDir string, logger *slog.Logger) *Workspace {
	return &Workspace{
		baseDir:   baseDir,
		worktrees: make(map[string]*Worktree),
		logger:    logger,
	}
}

// CreateWorktree 为指定 Agent 创建隔离的 git worktree
func (w *Workspace) CreateWorktree(agentID string) (*Worktree, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// 检查 baseDir 是否是 git 仓库
	if !w.isGitRepo() {
		return nil, fmt.Errorf("base directory %s is not a git repository", w.baseDir)
	}

	// 已存在则返回
	if wt, ok := w.worktrees[agentID]; ok && wt.Active {
		return wt, nil
	}

	branch := fmt.Sprintf("gotems/%s", agentID)
	worktreePath := filepath.Join(w.baseDir, ".gotems-worktrees", agentID)

	// 确保父目录存在
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0755); err != nil {
		return nil, fmt.Errorf("create worktree parent dir: %w", err)
	}

	// 清理可能残留的旧 worktree
	_ = w.removeWorktreeDir(worktreePath, branch)

	// 创建新分支（基于当前 HEAD）
	createBranch := exec.Command("git", "branch", branch, "HEAD")
	createBranch.Dir = w.baseDir
	_ = createBranch.Run() // 分支可能已存在，忽略错误

	// 创建 worktree
	cmd := exec.Command("git", "worktree", "add", worktreePath, branch)
	cmd.Dir = w.baseDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("create worktree: %s: %w", string(output), err)
	}

	wt := &Worktree{
		AgentID: agentID,
		Branch:  branch,
		Path:    worktreePath,
		Active:  true,
	}
	w.worktrees[agentID] = wt

	w.logger.Info("worktree created",
		"agent_id", agentID,
		"branch", branch,
		"path", worktreePath,
	)

	return wt, nil
}

// RemoveWorktree 移除指定 Agent 的 worktree
func (w *Workspace) RemoveWorktree(agentID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	wt, ok := w.worktrees[agentID]
	if !ok {
		return nil
	}

	if err := w.removeWorktreeDir(wt.Path, wt.Branch); err != nil {
		return err
	}

	wt.Active = false
	delete(w.worktrees, agentID)

	w.logger.Info("worktree removed", "agent_id", agentID)
	return nil
}

// GetWorktree 获取 Agent 的 worktree
func (w *Workspace) GetWorktree(agentID string) *Worktree {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.worktrees[agentID]
}

// WorkDir 返回 Agent 应使用的工作目录
// 如果有 worktree 则返回 worktree 路径，否则返回 baseDir
func (w *Workspace) WorkDir(agentID string) string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if wt, ok := w.worktrees[agentID]; ok && wt.Active {
		return wt.Path
	}
	return w.baseDir
}

// MergeWorktree 将 Agent 的 worktree 分支合并回主分支
func (w *Workspace) MergeWorktree(agentID string) error {
	w.mu.RLock()
	wt, ok := w.worktrees[agentID]
	w.mu.RUnlock()

	if !ok {
		return fmt.Errorf("no worktree found for agent %s", agentID)
	}

	// 先在 worktree 中 commit 未提交的变更
	if err := w.commitChanges(wt); err != nil {
		w.logger.Warn("failed to commit changes in worktree", "agent_id", agentID, "error", err)
	}

	// 获取当前主分支名
	mainBranch, err := w.currentBranch()
	if err != nil {
		return fmt.Errorf("get main branch: %w", err)
	}

	// 合并
	cmd := exec.Command("git", "merge", "--no-ff", wt.Branch, "-m",
		fmt.Sprintf("Merge gotems agent %s work", agentID))
	cmd.Dir = w.baseDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("merge branch %s into %s: %s: %w", wt.Branch, mainBranch, string(output), err)
	}

	w.logger.Info("worktree merged",
		"agent_id", agentID,
		"branch", wt.Branch,
		"into", mainBranch,
	)

	return nil
}

// MergeAll 合并所有 Agent 的 worktree
func (w *Workspace) MergeAll() []error {
	w.mu.RLock()
	ids := make([]string, 0, len(w.worktrees))
	for id := range w.worktrees {
		ids = append(ids, id)
	}
	w.mu.RUnlock()

	var errs []error
	for _, id := range ids {
		if err := w.MergeWorktree(id); err != nil {
			errs = append(errs, fmt.Errorf("merge %s: %w", id, err))
		}
	}
	return errs
}

// Cleanup 清理所有 worktree
func (w *Workspace) Cleanup() {
	w.mu.Lock()
	defer w.mu.Unlock()

	for id, wt := range w.worktrees {
		_ = w.removeWorktreeDir(wt.Path, wt.Branch)
		w.logger.Info("worktree cleaned up", "agent_id", id)
	}
	w.worktrees = make(map[string]*Worktree)

	// 清理 worktree 根目录
	wtDir := filepath.Join(w.baseDir, ".gotems-worktrees")
	_ = os.RemoveAll(wtDir)
}

// ChangedFiles 获取 worktree 中变更的文件列表
func (w *Workspace) ChangedFiles(agentID string) ([]string, error) {
	w.mu.RLock()
	wt, ok := w.worktrees[agentID]
	w.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("no worktree for agent %s", agentID)
	}

	cmd := exec.Command("git", "diff", "--name-only", "HEAD")
	cmd.Dir = wt.Path
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var files []string
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// --- 内部方法 ---

func (w *Workspace) isGitRepo() bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = w.baseDir
	return cmd.Run() == nil
}

func (w *Workspace) currentBranch() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = w.baseDir
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func (w *Workspace) commitChanges(wt *Worktree) error {
	// 检查是否有未提交的变更
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = wt.Path
	output, err := statusCmd.Output()
	if err != nil {
		return err
	}

	if strings.TrimSpace(string(output)) == "" {
		return nil // 无变更
	}

	// git add -A
	addCmd := exec.Command("git", "add", "-A")
	addCmd.Dir = wt.Path
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("git add: %w", err)
	}

	// git commit
	commitCmd := exec.Command("git", "commit", "-m",
		fmt.Sprintf("[gotems] Agent %s auto-commit", wt.AgentID))
	commitCmd.Dir = wt.Path
	if err := commitCmd.Run(); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}

	return nil
}

func (w *Workspace) removeWorktreeDir(path string, branch string) error {
	// 移除 worktree
	removeCmd := exec.Command("git", "worktree", "remove", path, "--force")
	removeCmd.Dir = w.baseDir
	_ = removeCmd.Run()

	// 清理目录
	_ = os.RemoveAll(path)

	// prune worktree 元数据
	pruneCmd := exec.Command("git", "worktree", "prune")
	pruneCmd.Dir = w.baseDir
	_ = pruneCmd.Run()

	// 删除分支
	delBranch := exec.Command("git", "branch", "-D", branch)
	delBranch.Dir = w.baseDir
	_ = delBranch.Run()

	return nil
}
