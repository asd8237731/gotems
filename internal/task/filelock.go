package task

import (
	"fmt"
	"sync"
)

// FileLock 管理多 Agent 并行修改代码时的文件锁
type FileLock struct {
	mu    sync.Mutex
	locks map[string]string // filePath -> agentID
}

// NewFileLock 创建文件锁管理器
func NewFileLock() *FileLock {
	return &FileLock{
		locks: make(map[string]string),
	}
}

// Acquire 尝试获取文件锁
func (fl *FileLock) Acquire(filePath, agentID string) error {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	if holder, ok := fl.locks[filePath]; ok {
		if holder != agentID {
			return fmt.Errorf("file %s is locked by agent %s", filePath, holder)
		}
		return nil // 已持有
	}
	fl.locks[filePath] = agentID
	return nil
}

// Release 释放文件锁
func (fl *FileLock) Release(filePath, agentID string) error {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	if holder, ok := fl.locks[filePath]; ok {
		if holder != agentID {
			return fmt.Errorf("file %s is locked by agent %s, cannot release", filePath, holder)
		}
		delete(fl.locks, filePath)
	}
	return nil
}

// ReleaseAll 释放指定 Agent 持有的所有锁
func (fl *FileLock) ReleaseAll(agentID string) {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	for path, holder := range fl.locks {
		if holder == agentID {
			delete(fl.locks, path)
		}
	}
}

// IsLocked 检查文件是否被锁定
func (fl *FileLock) IsLocked(filePath string) (bool, string) {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	holder, ok := fl.locks[filePath]
	return ok, holder
}

// HeldBy 返回指定 Agent 持有的所有文件锁
func (fl *FileLock) HeldBy(agentID string) []string {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	var files []string
	for path, holder := range fl.locks {
		if holder == agentID {
			files = append(files, path)
		}
	}
	return files
}
