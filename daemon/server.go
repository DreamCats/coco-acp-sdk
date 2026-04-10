package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/DreamCats/coco-acp-sdk/acp"
)

const (
	DefaultIdleTimeout = 10 * time.Minute
)

// Server 是 daemon 的核心，监听 Unix socket 并转发请求给 ACP client
type Server struct {
	configDir   string
	cwd         string
	idleTimeout time.Duration    // 空闲超时时间
	serveFlags  *acp.ServeFlags  // 传给 coco acp serve 的命令行参数

	listener net.Listener
	sessions SessionManager

	sessionsMu   sync.RWMutex // 保护 sessions map
	promptMu     sync.Mutex   // 串行化 prompt 请求（可按 session 细化）
	idleTimer    *time.Timer
	startTime    time.Time
	shutdownOnce sync.Once

	ctx    context.Context
	cancel context.CancelFunc
}

// ServerOption 配置 Server 的选项
type ServerOption func(*Server)

// WithServerServeFlags 设置传给 coco acp serve 的命令行参数
func WithServerServeFlags(flags *acp.ServeFlags) ServerOption {
	return func(s *Server) { s.serveFlags = flags }
}

// NewServer 创建 daemon server
// idleTimeout 为 0 时使用默认值 DefaultIdleTimeout
func NewServer(configDir, cwd string, idleTimeout time.Duration, opts ...ServerOption) *Server {
	if idleTimeout == 0 {
		idleTimeout = DefaultIdleTimeout
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		configDir:    configDir,
		cwd:          cwd,
		idleTimeout:  idleTimeout,
		startTime:    time.Now(),
		ctx:          ctx,
		cancel:       cancel,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// SetCommandFactoryForTest 设置命令工厂（仅用于测试）
func (s *Server) SetCommandFactoryForTest(f acp.CommandFactory) {
	// 延迟初始化 sessions（因为 Run 之前 sessions 为 nil）
	if s.sessions == nil {
		s.sessions = newSessionManager(s.idleTimeout, nil)
	}
	if sm, ok := s.sessions.(*sessionManager); ok {
		sm.SetCommandFactory(f)
	}
}

// Run 启动 daemon：初始化 SessionManager → 监听 socket → 处理连接
func (s *Server) Run() error {
	if err := os.MkdirAll(s.configDir, 0700); err != nil {
		return fmt.Errorf("daemon: 创建配置目录失败: %w", err)
	}

	// 初始化 SessionManager 和持久化存储
	store := NewSessionsStore(s.configDir)
	if err := store.Load(); err != nil {
		log.Printf("daemon: 加载 session 历史失败: %v\n", err)
	}
	s.sessions = newSessionManager(s.idleTimeout, store)

	// 透传 ServeFlags 给 SessionManager
	if s.serveFlags != nil {
		if sm, ok := s.sessions.(*sessionManager); ok {
			sm.SetACPOptions([]acp.Option{acp.WithServeFlags(s.serveFlags)})
		}
	}

	// 创建默认 session
	if _, err := s.sessions.CreateSession(s.ctx, s.cwd); err != nil {
		return fmt.Errorf("daemon: 创建默认 session 失败: %w", err)
	}

	// 启动空闲检查 goroutine
	go s.sessions.(*sessionManager).runIdleChecker()

	// 监听 Unix socket
	sockPath := s.sockPath()
	_ = os.Remove(sockPath)

	var err error
	s.listener, err = net.Listen("unix", sockPath)
	if err != nil {
		s.sessions.Close()
		return fmt.Errorf("daemon: 监听 socket 失败: %w", err)
	}
	_ = os.Chmod(sockPath, 0600)

	// 写 PID 文件
	if err := s.writePID(); err != nil {
		s.shutdown()
		return fmt.Errorf("daemon: 写 PID 文件失败: %w", err)
	}

	// 空闲超时
	s.idleTimer = time.AfterFunc(s.idleTimeout, func() {
		log.Println("daemon: 空闲超时，自动退出")
		s.shutdown()
	})

	log.Printf("daemon: 已启动 (pid=%d, sock=%s)\n", os.Getpid(), sockPath)

	s.acceptLoop()
	return nil
}

// acceptLoop 接受新连接并处理
func (s *Server) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.ctx.Done():
				return
			default:
				log.Printf("daemon: accept 错误: %v\n", err)
				continue
			}
		}
		go s.handleConn(conn)
	}
}

// handleConn 处理一个 CLI 连接
func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()

	s.idleTimer.Reset(s.idleTimeout)

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		var req Request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			s.writeResponse(conn, Response{Type: RespError, Error: "无效的请求格式"})
			return
		}

		switch req.Type {
		case ReqPrompt:
			s.handlePrompt(conn, &req)
		case ReqCompact:
			s.handleCompact(conn, &req)
		case ReqStatus:
			s.handleStatus(conn)
		case ReqShutdown:
			s.writeResponse(conn, Response{Type: RespDone, Text: "daemon 正在关闭"})
			go s.shutdown()
			return
		case ReqSessionNew:
			s.handleSessionNew(conn, &req)
		case ReqSessionClose:
			s.handleSessionClose(conn, &req)
		case ReqSessionList:
			s.handleSessionList(conn)
		default:
			s.writeResponse(conn, Response{Type: RespError, Error: fmt.Sprintf("未知请求类型: %s", req.Type)})
		}
	}
}

// handlePrompt 转发 prompt 请求给 ACP client，流式回传结果
func (s *Server) handlePrompt(conn net.Conn, req *Request) {
	sessionID := req.SessionID
	if sessionID == "" {
		// 兼容旧行为：使用默认 session
		sessionID = s.sessions.GetDefaultSessionID()
	}

	s.sessionsMu.RLock()
	sess := s.sessions.Get(sessionID)
	s.sessionsMu.RUnlock()

	if sess == nil {
		s.writeResponse(conn, Response{Type: RespError, Error: fmt.Sprintf("session not found: %s", sessionID)})
		return
	}

	// 设置通知回调
	sess.NotifyHandler = func(method string, update *acp.SessionUpdate) {
		s.writeSessionResponse(conn, sessionID, update)
	}

	stopReason, err := sess.Prompt(s.ctx, req.Text, req.ModelID)

	sess.NotifyHandler = nil

	if err != nil {
		s.writeResponse(conn, Response{Type: RespError, Error: err.Error()})
		return
	}

	s.writeResponse(conn, Response{
		Type:       RespDone,
		StopReason: stopReason,
		SessionID:  sessionID,
		ModelID:    sess.ModelID,
	})
}

// handleCompact 执行上下文压缩
func (s *Server) handleCompact(conn net.Conn, req *Request) {
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = s.sessions.GetDefaultSessionID()
	}

	s.sessionsMu.RLock()
	sess := s.sessions.Get(sessionID)
	s.sessionsMu.RUnlock()

	if sess == nil {
		s.writeResponse(conn, Response{Type: RespError, Error: fmt.Sprintf("session not found: %s", sessionID)})
		return
	}

	if err := sess.Compact(s.ctx); err != nil {
		s.writeResponse(conn, Response{Type: RespError, Error: err.Error()})
		return
	}
	s.writeResponse(conn, Response{Type: RespDone, Text: "上下文已压缩"})
}

// handleStatus 返回 daemon 状态
func (s *Server) handleStatus(conn net.Conn) {
	sessionID := s.sessions.GetDefaultSessionID()
	sess := s.sessions.Get(sessionID)

	resp := Response{
		Type:   RespStatus,
		PID:    os.Getpid(),
		Uptime: time.Since(s.startTime).Truncate(time.Second).String(),
	}
	if sess != nil {
		resp.SessionID = sessionID
		resp.ModelID = sess.ModelID
	}
	s.writeResponse(conn, resp)
}

// handleSessionNew 创建新 session
func (s *Server) handleSessionNew(conn net.Conn, req *Request) {
	cwd := req.Cwd
	if cwd == "" {
		cwd = s.cwd
	}

	sessionID, err := s.sessions.CreateSession(s.ctx, cwd)
	if err != nil {
		s.writeResponse(conn, Response{Type: RespError, Error: fmt.Sprintf("创建 session 失败: %v", err)})
		return
	}

	sess := s.sessions.Get(sessionID)
	s.writeResponse(conn, Response{
		Type:   RespSessionNew,
		Text:   sessionID,
		ModelID: sess.ModelID,
	})
}

// handleSessionClose 关闭指定 session
func (s *Server) handleSessionClose(conn net.Conn, req *Request) {
	sessionID := req.SessionID
	if sessionID == "" {
		s.writeResponse(conn, Response{Type: RespError, Error: "session ID 不能为空"})
		return
	}

	// 不允许关闭最后一个 session
	if s.sessions.Len() <= 1 {
		s.writeResponse(conn, Response{Type: RespError, Error: "不能关闭最后一个 session"})
		return
	}

	if err := s.sessions.CloseSession(s.ctx, sessionID); err != nil {
		s.writeResponse(conn, Response{Type: RespError, Error: err.Error()})
		return
	}

	s.writeResponse(conn, Response{Type: RespDone, Text: fmt.Sprintf("session %s 已关闭", sessionID)})
}

// handleSessionList 列出所有 session
func (s *Server) handleSessionList(conn net.Conn) {
	ids := s.sessions.ListSessions()
	data, _ := json.Marshal(ids)
	s.writeResponse(conn, Response{
		Type: RespSessionList,
		Text: string(data),
	})
}

// shutdown 优雅关闭 daemon，可安全重复调用
func (s *Server) shutdown() {
	s.shutdownOnce.Do(func() {
		s.cancel()
		if s.idleTimer != nil {
			s.idleTimer.Stop()
		}
		if s.listener != nil {
			_ = s.listener.Close()
		}
		if s.sessions != nil {
			// 持久化 session 列表
			if sm, ok := s.sessions.(*sessionManager); ok && sm.store != nil {
				_ = sm.store.Persist()
			}
			s.sessions.Close()
		}
		_ = os.Remove(s.sockPath())
		_ = os.Remove(s.pidPath())
		log.Println("daemon: 已关闭")
	})
}

func (s *Server) writeResponse(conn net.Conn, resp Response) {
	data, err := Encode(resp)
	if err != nil {
		return
	}
	_, _ = conn.Write(data)
}

func (s *Server) writeSessionResponse(conn net.Conn, sessionID string, update *acp.SessionUpdate) {
	switch update.SessionUpdate {
	case acp.UpdateAgentMessageChunk:
		if update.Content != nil {
			s.writeResponse(conn, Response{
				Type:      RespChunk,
				Text:      update.Content.Text,
				SessionID: sessionID,
			})
		}
	case acp.UpdateAgentThoughtChunk:
		if update.Content != nil {
			s.writeResponse(conn, Response{
				Type:      RespThought,
				Text:      update.Content.Text,
				SessionID: sessionID,
			})
		}
	case acp.UpdateToolCall:
		s.writeResponse(conn, Response{
			Type:       RespToolCall,
			SessionID:  sessionID,
			ToolCallID: update.ToolCallID,
			ToolKind:   update.Kind,
			ToolTitle:  update.Title,
			ToolStatus: update.Status,
		})
	case acp.UpdateToolCallUpdate:
		s.writeResponse(conn, Response{
			Type:       RespToolResult,
			SessionID:  sessionID,
			ToolCallID: update.ToolCallID,
			ToolStatus: update.Status,
			Text:       update.ToolResultText(),
		})
	case acp.UpdateAvailableCommands:
		cmds := make([]CommandInfo, 0, len(update.AvailableCommands))
		for _, c := range update.AvailableCommands {
			cmds = append(cmds, CommandInfo{Name: c.Name, Description: c.Description})
		}
		s.writeResponse(conn, Response{
			Type:      RespCommands,
			SessionID: sessionID,
			Commands:  cmds,
		})
	}
}

func (s *Server) writePID() error {
	return os.WriteFile(s.pidPath(), []byte(strconv.Itoa(os.Getpid())), 0600)
}

func (s *Server) sockPath() string {
	return filepath.Join(s.configDir, "daemon.sock")
}

func (s *Server) pidPath() string {
	return filepath.Join(s.configDir, "daemon.pid")
}
