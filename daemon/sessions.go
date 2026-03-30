package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SessionRecord 持久化的 session 记录
type SessionRecord struct {
	SessionID  string    `json:"sessionID"`
	Cwd        string    `json:"cwd"`
	ModelID    string    `json:"modelID,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	LastActive time.Time `json:"lastActive"`
}

// SessionsStore session 持久化存储
type SessionsStore struct {
	filePath string
	mu       sync.RWMutex
	sessions map[string]*SessionRecord // key: sessionID
}

// NewSessionsStore 创建 SessionsStore
func NewSessionsStore(configDir string) *SessionsStore {
	return &SessionsStore{
		filePath: filepath.Join(configDir, "sessions.json"),
		sessions: make(map[string]*SessionRecord),
	}
}

// Load 从磁盘加载 session 列表
func (s *SessionsStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 文件不存在不算错误
		}
		return fmt.Errorf("读取 sessions.json 失败: %w", err)
	}

	var records []*SessionRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return fmt.Errorf("解析 sessions.json 失败: %w", err)
	}

	s.sessions = make(map[string]*SessionRecord)
	for _, r := range records {
		s.sessions[r.SessionID] = r
	}

	return nil
}

// Persist 将当前 session 列表写入磁盘
func (s *SessionsStore) Persist() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	records := make([]*SessionRecord, 0, len(s.sessions))
	for _, r := range s.sessions {
		records = append(records, r)
	}

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 sessions.json 失败: %w", err)
	}

	if err := os.WriteFile(s.filePath, data, 0600); err != nil {
		return fmt.Errorf("写入 sessions.json 失败: %w", err)
	}

	return nil
}

// Get 获取指定 session 的记录
func (s *SessionsStore) Get(sessionID string) *SessionRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[sessionID]
}

// List 返回所有 session 记录
func (s *SessionsStore) List() []*SessionRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	records := make([]*SessionRecord, 0, len(s.sessions))
	for _, r := range s.sessions {
		records = append(records, r)
	}
	return records
}

// Add 添加或更新 session 记录
func (s *SessionsStore) Add(sessionID, cwd, modelID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if existing, ok := s.sessions[sessionID]; ok {
		existing.Cwd = cwd
		existing.ModelID = modelID
		existing.LastActive = now
	} else {
		s.sessions[sessionID] = &SessionRecord{
			SessionID:  sessionID,
			Cwd:        cwd,
			ModelID:    modelID,
			CreatedAt:  now,
			LastActive: now,
		}
	}
}

// Remove 删除 session 记录
func (s *SessionsStore) Remove(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
}

// UpdateLastActive 更新 lastActive 时间
func (s *SessionsStore) UpdateLastActive(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.sessions[sessionID]; ok {
		r.LastActive = time.Now()
	}
}
