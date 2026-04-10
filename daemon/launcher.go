package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/DreamCats/coco-acp-sdk/acp"
)

const (
	DefaultConfigDir = ".config/livecoding/coco-acp"
	DialTimeout      = 2 * time.Second
	StartupTimeout   = 30 * time.Second // 包含 coco 冷启动 ~14s
)

// ConfigDir 返回配置目录的完整路径
// 上层 agent 可以通过 WithConfigDir 覆盖
func ConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, DefaultConfigDir)
}

// Conn 封装与 daemon 的连接，提供高层方法
type Conn struct {
	conn           net.Conn
	scanner        *bufio.Scanner
	currentSession string // 当前使用的 session ID
}

// DialOption 连接配置
type DialOption struct {
	ConfigDir   string          // 自定义配置目录
	IdleTimeout time.Duration   // 空闲超时时间，0 表示使用默认值
	DaemonCmd   string          // 自定义 daemon 启动命令（可执行文件路径）
	DaemonArgs  []string        // daemon 启动参数
	ServeFlags  *acp.ServeFlags // 传给 coco acp serve 的命令行参数
}

// Dial 连接到 daemon，如果 daemon 没在运行则自动拉起
func Dial(cwd string, opt *DialOption) (*Conn, error) {
	configDir := ConfigDir()
	if opt != nil && opt.ConfigDir != "" {
		configDir = opt.ConfigDir
	}
	sockPath := filepath.Join(configDir, "daemon.sock")

	// 尝试直连
	conn, err := tryDial(sockPath)
	if err == nil {
		return newConn(conn), nil
	}

	// 连不上 → 检查是否有残留
	cleanStale(configDir)

	// 拉起 daemon
	if err := startDaemon(cwd, opt); err != nil {
		return nil, fmt.Errorf("启动 daemon 失败: %w", err)
	}

	// 等待 daemon ready
	conn, err = waitAndDial(sockPath, StartupTimeout)
	if err != nil {
		return nil, fmt.Errorf("连接 daemon 超时: %w", err)
	}

	return newConn(conn), nil
}

// PromptHandler 处理 prompt 响应的回调集合
type PromptHandler struct {
	OnChunk      func(text string)                              // 模型输出文本片段
	OnThought    func(text string)                              // 模型思考过程片段
	OnToolCall   func(id, kind, title, status string)           // 工具调用开始
	OnToolResult func(id, status, text string)                  // 工具调用结果
	OnCommands   func(commands []CommandInfo)                   // 可用命令列表
}

// Prompt 发送 prompt 请求
func (c *Conn) Prompt(text, modelID, cwd string, onChunk func(string), onToolCall func(kind, title, status string)) (stopReason string, err error) {
	return c.PromptWithHandler(text, modelID, cwd, &PromptHandler{
		OnChunk: onChunk,
		OnToolCall: func(id, kind, title, status string) {
			if onToolCall != nil {
				onToolCall(kind, title, status)
			}
		},
	})
}

// PromptWithHandler 发送 prompt 请求，使用完整的回调处理器
func (c *Conn) PromptWithHandler(text, modelID, cwd string, handler *PromptHandler) (stopReason string, err error) {
	req := Request{
		Type:      ReqPrompt,
		SessionID: c.currentSession,
		Text:      text,
		ModelID:   modelID,
		Cwd:       cwd,
	}
	if err := c.send(req); err != nil {
		return "", err
	}
	return c.readUntilDoneWithHandler(handler)
}

// Compact 发送 compact 请求
func (c *Conn) Compact() error {
	req := Request{
		Type:      ReqCompact,
		SessionID: c.currentSession,
	}
	if err := c.send(req); err != nil {
		return err
	}
	_, err := c.readUntilDone(nil, nil)
	return err
}

// NewSession 创建新 session
func (c *Conn) NewSession(cwd string) (*SessionResponse, error) {
	req := Request{
		Type: ReqSessionNew,
		Cwd:  cwd,
	}
	if err := c.send(req); err != nil {
		return nil, err
	}

	resp, err := c.readOne()
	if err != nil {
		return nil, err
	}
	if resp.Type == RespError {
		return nil, fmt.Errorf("daemon: %s", resp.Error)
	}

	// 解析 session 信息
	var result SessionResponse
	result.SessionID = resp.Text
	result.ModelID = resp.ModelID
	return &result, nil
}

// CloseSession 关闭指定 session
func (c *Conn) CloseSession(sessionID string) error {
	req := Request{
		Type:      ReqSessionClose,
		SessionID: sessionID,
	}
	if err := c.send(req); err != nil {
		return err
	}

	resp, err := c.readOne()
	if err != nil {
		return err
	}
	if resp.Type == RespError {
		return fmt.Errorf("daemon: %s", resp.Error)
	}
	return nil
}

// ListSessions 列出所有 session
func (c *Conn) ListSessions() ([]string, error) {
	req := Request{Type: ReqSessionList}
	if err := c.send(req); err != nil {
		return nil, err
	}

	resp, err := c.readOne()
	if err != nil {
		return nil, err
	}
	if resp.Type == RespError {
		return nil, fmt.Errorf("daemon: %s", resp.Error)
	}

	var ids []string
	if err := json.Unmarshal([]byte(resp.Text), &ids); err != nil {
		return nil, fmt.Errorf("解析 session 列表失败: %w", err)
	}
	return ids, nil
}

// UseSession 设置当前使用的 session（兼容旧行为）
func (c *Conn) UseSession(sessionID string) {
	c.currentSession = sessionID
}

// Status 查询 daemon 状态
func (c *Conn) Status() (*Response, error) {
	if err := c.send(Request{Type: ReqStatus}); err != nil {
		return nil, err
	}

	resp, err := c.readOne()
	if err != nil {
		return nil, err
	}
	if resp.Type == RespError {
		return nil, fmt.Errorf("daemon: %s", resp.Error)
	}
	return resp, nil
}

// Shutdown 请求 daemon 关闭
func (c *Conn) Shutdown() error {
	if err := c.send(Request{Type: ReqShutdown}); err != nil {
		return err
	}
	_, _ = c.readOne()
	return nil
}

// Close 关闭连接
func (c *Conn) Close() error {
	return c.conn.Close()
}

// --- 内部方法 ---

func newConn(conn net.Conn) *Conn {
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return &Conn{conn: conn, scanner: scanner}
}

func (c *Conn) send(req Request) error {
	data, err := Encode(req)
	if err != nil {
		return fmt.Errorf("序列化请求失败: %w", err)
	}
	_, err = c.conn.Write(data)
	return err
}

func (c *Conn) readOne() (*Response, error) {
	if !c.scanner.Scan() {
		if err := c.scanner.Err(); err != nil {
			return nil, fmt.Errorf("读取响应失败: %w", err)
		}
		return nil, fmt.Errorf("daemon 连接已关闭")
	}
	var resp Response
	if err := json.Unmarshal(c.scanner.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}
	return &resp, nil
}

func (c *Conn) readUntilDone(onChunk func(string), onToolCall func(kind, title, status string)) (string, error) {
	return c.readUntilDoneWithHandler(&PromptHandler{
		OnChunk: onChunk,
		OnToolCall: func(id, kind, title, status string) {
			if onToolCall != nil {
				onToolCall(kind, title, status)
			}
		},
	})
}

func (c *Conn) readUntilDoneWithHandler(h *PromptHandler) (string, error) {
	if h == nil {
		h = &PromptHandler{}
	}
	for {
		resp, err := c.readOne()
		if err != nil {
			return "", err
		}
		switch resp.Type {
		case RespChunk:
			if h.OnChunk != nil {
				h.OnChunk(resp.Text)
			}
		case RespThought:
			if h.OnThought != nil {
				h.OnThought(resp.Text)
			}
		case RespToolCall:
			if h.OnToolCall != nil {
				h.OnToolCall(resp.ToolCallID, resp.ToolKind, resp.ToolTitle, resp.ToolStatus)
			}
		case RespToolResult:
			if h.OnToolResult != nil {
				h.OnToolResult(resp.ToolCallID, resp.ToolStatus, resp.Text)
			}
		case RespCommands:
			if h.OnCommands != nil {
				h.OnCommands(resp.Commands)
			}
		case RespDone:
			return resp.StopReason, nil
		case RespError:
			return "", fmt.Errorf("daemon: %s", resp.Error)
		}
	}
}

func tryDial(sockPath string) (net.Conn, error) {
	return net.DialTimeout("unix", sockPath, DialTimeout)
}

func waitAndDial(sockPath string, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := tryDial(sockPath)
		if err == nil {
			return conn, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil, fmt.Errorf("等待 %v 后仍无法连接 %s", timeout, sockPath)
}

func cleanStale(configDir string) {
	pidPath := filepath.Join(configDir, "daemon.pid")
	sockPath := filepath.Join(configDir, "daemon.sock")

	data, err := os.ReadFile(pidPath)
	if err != nil {
		_ = os.Remove(sockPath)
		return
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		_ = os.Remove(pidPath)
		_ = os.Remove(sockPath)
		return
	}

	proc, err := os.FindProcess(pid)
	if err != nil || proc.Signal(syscall.Signal(0)) != nil {
		_ = os.Remove(pidPath)
		_ = os.Remove(sockPath)
	}
}

// execCommand 是 exec.Command 的包装，方便测试
var execCommand = exec.Command

func startDaemon(cwd string, opt *DialOption) error {
	var exe string
	var args []string

	if opt != nil && opt.DaemonCmd != "" {
		exe = opt.DaemonCmd
		args = opt.DaemonArgs
	} else {
		var err error
		exe, err = os.Executable()
		if err != nil {
			return fmt.Errorf("获取可执行文件路径失败: %w", err)
		}
		args = []string{"daemon", "start", "--cwd", cwd}
		// 添加空闲超时参数
		if opt != nil && opt.IdleTimeout > 0 {
			args = append(args, "--idle-timeout", opt.IdleTimeout.String())
		}
		// 添加 coco acp serve 的参数
		if opt != nil && opt.ServeFlags != nil {
			if opt.ServeFlags.Yolo {
				args = append(args, "--yolo")
			}
			for _, t := range opt.ServeFlags.AllowedTools {
				args = append(args, "--allowed-tool", t)
			}
			for _, t := range opt.ServeFlags.DisallowedTools {
				args = append(args, "--disallowed-tool", t)
			}
			if opt.ServeFlags.BashToolTimeout > 0 {
				args = append(args, "--bash-tool-timeout", opt.ServeFlags.BashToolTimeout.String())
			}
			if opt.ServeFlags.QueryTimeout > 0 {
				args = append(args, "--query-timeout", opt.ServeFlags.QueryTimeout.String())
			}
			for _, c := range opt.ServeFlags.Configs {
				args = append(args, "--config", c)
			}
		}
	}

	cmd := execCommand(exe, args...)
	cmd.Dir = cwd
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 daemon 进程失败: %w", err)
	}

	_ = cmd.Process.Release()
	return nil
}

// IsRunning 检查 daemon 是否在运行
func IsRunning() bool {
	return IsRunningAt(ConfigDir())
}

// IsRunningAt 检查指定配置目录下的 daemon 是否在运行
func IsRunningAt(configDir string) bool {
	sockPath := filepath.Join(configDir, "daemon.sock")
	conn, err := tryDial(sockPath)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
