package daemon

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/DreamCats/coco-acp-sdk/acp"
)

// SessionManager 管理所有逻辑 Session
type SessionManager interface {
	// CreateSession 创建一个新 Session，返回其 sessionID
	CreateSession(ctx context.Context, cwd string) (sessionID string, err error)

	// CloseSession 关闭指定 Session
	CloseSession(ctx context.Context, sessionID string) error

	// GetSession 获取 Session，不存在返回 nil
	GetSession(sessionID string) *Session

	// Get 获取 Session（别名，用于兼容）
	Get(sessionID string) *Session

	// ListSessions 返回所有活跃 Session 的 ID
	ListSessions() []string

	// ForEach 遍历所有 Session（用于全局操作如 Shutdown）
	ForEach(fn func(sessionID string, s *Session) bool)

	// Len 返回当前 Session 数量
	Len() int

	// GetDefaultSessionID 返回默认 session ID（最早创建的一个）
	GetDefaultSessionID() string

	// Close 关闭 SessionManager 及其所有 Session
	Close()
}

// Session 代表一个逻辑 Session（对应一个 ACP session）
type Session struct {
	SessionID    string // daemon 层逻辑 ID
	ACPSessionID string // ACP 层 session ID
	Cwd          string
	ModelID      string
	Client       *acp.Client
	NotifyHandler acp.NotifyHandler

	mu         sync.Mutex
	createdAt  time.Time
	lastActive time.Time
	idleTimeout time.Duration
	done       chan struct{}
}

// NewSession 创建新 Session
func NewSession(sessionID string, client *acp.Client) *Session {
	return &Session{
		SessionID:    sessionID,
		ACPSessionID: client.SessionID(),
		Cwd:          "", // TODO: 从 client 获取
		Client:       client,
		createdAt:    time.Now(),
		lastActive:   time.Now(),
		idleTimeout:  DefaultIdleTimeout,
		done:         make(chan struct{}),
	}
}

// Prompt 向此 Session 发送 prompt
func (s *Session) Prompt(ctx context.Context, text, modelID string) (string, error) {
	s.mu.Lock()
	s.lastActive = time.Now()
	s.mu.Unlock()

	s.Client.SetNotifyHandler(s.NotifyHandler)
	return s.Client.Prompt(ctx, text, modelID)
}

// Compact 执行上下文压缩
func (s *Session) Compact(ctx context.Context) error {
	s.mu.Lock()
	s.lastActive = time.Now()
	s.mu.Unlock()

	return s.Client.Compact(ctx)
}

// Close 关闭 Session
func (s *Session) Close() error {
	select {
	case <-s.done:
		return nil // 已关闭
	default:
	}
	close(s.done)
	return s.Client.Close()
}

// IsIdle 检查 Session 是否已空闲超时
func (s *Session) IsIdle() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.lastActive) > s.idleTimeout
}

// SetIdleTimeout 设置空闲超时时间
func (s *Session) SetIdleTimeout(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.idleTimeout = d
}

// IsClosed 检查 Session 是否已关闭
func (s *Session) IsClosed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// sessionManager 实现 SessionManager 接口
type sessionManager struct {
	sessions       sync.Map // map[string]*Session
	idleTimeout    time.Duration
	ctx            context.Context
	cancel         context.CancelFunc
	commandFactory acp.CommandFactory // 可选的命令工厂，用于测试
	store          *SessionsStore     // session 持久化存储
}

// newSessionManager 创建 SessionManager
func newSessionManager(idleTimeout time.Duration, store *SessionsStore) *sessionManager {
	ctx, cancel := context.WithCancel(context.Background())
	if idleTimeout == 0 {
		idleTimeout = DefaultIdleTimeout
	}
	return &sessionManager{
		idleTimeout: idleTimeout,
		ctx:         ctx,
		cancel:      cancel,
		store:       store,
	}
}

// SetCommandFactory 设置命令工厂（用于测试）
func (sm *sessionManager) SetCommandFactory(f acp.CommandFactory) {
	sm.commandFactory = f
}

// CreateSession 创建一个新 Session
func (sm *sessionManager) CreateSession(ctx context.Context, cwd string) (string, error) {
	// 创建新的 ACP client
	var client *acp.Client
	if sm.commandFactory != nil {
		// 测试模式：使用注入的 commandFactory
		client = acp.NewClient(cwd, acp.WithCommandFactory(sm.commandFactory))
	} else {
		client = acp.NewClient(cwd)
	}
	if err := client.Start(ctx); err != nil {
		return "", fmt.Errorf("创建 ACP client 失败: %w", err)
	}

	// 生成 session ID（使用 ACP 返回的 session ID）
	sessionID := client.SessionID()
	if sessionID == "" {
		client.Close()
		return "", fmt.Errorf("未获取到有效的 session ID")
	}

	// 创建 Session 并存储
	sess := NewSession(sessionID, client)
	sess.Cwd = cwd
	sess.ModelID = client.Models().CurrentModelID
	sess.idleTimeout = sm.idleTimeout
	sm.sessions.Store(sessionID, sess)

	// 持久化
	if sm.store != nil {
		sm.store.Add(sessionID, cwd, sess.ModelID)
		_ = sm.store.Persist()
	}

	return sessionID, nil
}

// CloseSession 关闭指定 Session
func (sm *sessionManager) CloseSession(ctx context.Context, sessionID string) error {
	val, ok := sm.sessions.Load(sessionID)
	if !ok {
		return fmt.Errorf("session 不存在: %s", sessionID)
	}
	sess := val.(*Session)
	sm.sessions.Delete(sessionID)

	// 持久化
	if sm.store != nil {
		sm.store.Remove(sessionID)
		_ = sm.store.Persist()
	}

	return sess.Close()
}

// GetSession 获取 Session
func (sm *sessionManager) GetSession(sessionID string) *Session {
	val, ok := sm.sessions.Load(sessionID)
	if !ok {
		return nil
	}
	return val.(*Session)
}

// Get 获取 Session（别名）
func (sm *sessionManager) Get(sessionID string) *Session {
	return sm.GetSession(sessionID)
}

// ListSessions 返回所有活跃 Session 的 ID
func (sm *sessionManager) ListSessions() []string {
	var ids []string
	sm.sessions.Range(func(key, value any) bool {
		if id, ok := key.(string); ok {
			ids = append(ids, id)
		}
		return true
	})
	return ids
}

// ForEach 遍历所有 Session
func (sm *sessionManager) ForEach(fn func(sessionID string, s *Session) bool) {
	sm.sessions.Range(func(key, value any) bool {
		id, _ := key.(string)
		sess, _ := value.(*Session)
		return fn(id, sess)
	})
}

// Len 返回当前 Session 数量
func (sm *sessionManager) Len() int {
	count := 0
	sm.sessions.Range(func(any, any) bool {
		count++
		return true
	})
	return count
}

// GetDefaultSessionID 返回最早创建的 session ID
func (sm *sessionManager) GetDefaultSessionID() string {
	var defaultID string
	var oldestTime time.Time
	sm.sessions.Range(func(key, value any) bool {
		id, _ := key.(string)
		sess, _ := value.(*Session)
		if defaultID == "" || sess.createdAt.Before(oldestTime) {
			defaultID = id
			oldestTime = sess.createdAt
		}
		return true
	})
	return defaultID
}

// Close 关闭 SessionManager 及其所有 Session
func (sm *sessionManager) Close() {
	sm.cancel()
	sm.ForEach(func(id string, s *Session) bool {
		s.Close()
		return true
	})
}

// runIdleChecker 启动空闲检查 goroutine
func (sm *sessionManager) runIdleChecker() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-sm.ctx.Done():
			return
		case <-ticker.C:
			sm.checkIdleSessions()
		}
	}
}

// checkIdleSessions 检查并关闭空闲超时的 Session
func (sm *sessionManager) checkIdleSessions() {
	sm.sessions.Range(func(key, value any) bool {
		id, _ := key.(string)
		sess, _ := value.(*Session)
		if sess.IsIdle() {
			sm.sessions.Delete(id)
			go sess.Close() // 异步关闭
		}
		return true
	})
}
