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
	configDir    string
	cwd          string
	idleTimeout  time.Duration // 空闲超时时间

	listener net.Listener
	client   *acp.Client

	mu           sync.Mutex // 串行化 prompt 请求
	idleTimer    *time.Timer
	startTime    time.Time
	shutdownOnce sync.Once

	ctx    context.Context
	cancel context.CancelFunc
}

// NewServer 创建 daemon server
// idleTimeout 为 0 时使用默认值 DefaultIdleTimeout
func NewServer(configDir, cwd string, idleTimeout time.Duration) *Server {
	if idleTimeout == 0 {
		idleTimeout = DefaultIdleTimeout
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		configDir:    configDir,
		cwd:          cwd,
		idleTimeout:  idleTimeout,
		startTime:    time.Now(),
		ctx:          ctx,
		cancel:       cancel,
	}
}

// Run 启动 daemon：初始化 ACP client → 监听 socket → 处理连接
func (s *Server) Run() error {
	if err := os.MkdirAll(s.configDir, 0700); err != nil {
		return fmt.Errorf("daemon: 创建配置目录失败: %w", err)
	}

	// 启动 ACP client
	s.client = acp.NewClient(s.cwd)
	if err := s.client.Start(s.ctx); err != nil {
		return fmt.Errorf("daemon: 启动 ACP client 失败: %w", err)
	}

	// 监听 Unix socket
	sockPath := s.sockPath()
	_ = os.Remove(sockPath)

	var err error
	s.listener, err = net.Listen("unix", sockPath)
	if err != nil {
		s.client.Close()
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

	log.Printf("daemon: 已启动 (pid=%d, sock=%s, session=%s)\n",
		os.Getpid(), sockPath, s.client.SessionID())

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
			s.handleCompact(conn)
		case ReqStatus:
			s.handleStatus(conn)
		case ReqShutdown:
			s.writeResponse(conn, Response{Type: RespDone, Text: "daemon 正在关闭"})
			go s.shutdown()
			return
		default:
			s.writeResponse(conn, Response{Type: RespError, Error: fmt.Sprintf("未知请求类型: %s", req.Type)})
		}
	}
}

// handlePrompt 转发 prompt 请求给 ACP client，流式回传结果
func (s *Server) handlePrompt(conn net.Conn, req *Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 如果 CLI 传了新 cwd 且与当前不同，需要重建 session
	if req.Cwd != "" && req.Cwd != s.cwd {
		s.cwd = req.Cwd
		_ = s.client.Close()
		s.client = acp.NewClient(s.cwd)
		if err := s.client.Start(s.ctx); err != nil {
			s.writeResponse(conn, Response{Type: RespError, Error: fmt.Sprintf("重建 session 失败: %v", err)})
			return
		}
	}

	// 临时设置 notification handler，将通知流式写回当前连接
	s.client.SetNotifyHandler(func(method string, update *acp.SessionUpdate) {
		switch update.SessionUpdate {
		case "agent_message_chunk":
			if update.Content != nil {
				s.writeResponse(conn, Response{Type: RespChunk, Text: update.Content.Text})
			}
		case "tool_call":
			s.writeResponse(conn, Response{
				Type:       RespToolCall,
				ToolKind:   update.Kind,
				ToolTitle:  update.Title,
				ToolStatus: update.Status,
			})
		}
	})

	stopReason, err := s.client.Prompt(s.ctx, req.Text, req.ModelID)

	s.client.SetNotifyHandler(nil)

	if err != nil {
		s.writeResponse(conn, Response{Type: RespError, Error: err.Error()})
		return
	}

	s.writeResponse(conn, Response{
		Type:       RespDone,
		StopReason: stopReason,
		SessionID:  s.client.SessionID(),
		ModelID:    s.client.Models().CurrentModelID,
	})
}

// handleCompact 执行上下文压缩
func (s *Server) handleCompact(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.client.Compact(s.ctx); err != nil {
		s.writeResponse(conn, Response{Type: RespError, Error: err.Error()})
		return
	}
	s.writeResponse(conn, Response{Type: RespDone, Text: "上下文已压缩"})
}

// handleStatus 返回 daemon 状态
func (s *Server) handleStatus(conn net.Conn) {
	s.writeResponse(conn, Response{
		Type:      RespStatus,
		PID:       os.Getpid(),
		SessionID: s.client.SessionID(),
		ModelID:   s.client.Models().CurrentModelID,
		Uptime:    time.Since(s.startTime).Truncate(time.Second).String(),
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
		if s.client != nil {
			_ = s.client.Close()
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

func (s *Server) writePID() error {
	return os.WriteFile(s.pidPath(), []byte(strconv.Itoa(os.Getpid())), 0600)
}

func (s *Server) sockPath() string {
	return filepath.Join(s.configDir, "daemon.sock")
}

func (s *Server) pidPath() string {
	return filepath.Join(s.configDir, "daemon.pid")
}
