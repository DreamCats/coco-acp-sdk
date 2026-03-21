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
	conn    net.Conn
	scanner *bufio.Scanner
}

// DialOption 连接配置
type DialOption struct {
	ConfigDir  string // 自定义配置目录
	DaemonCmd  string // 自定义 daemon 启动命令（可执行文件路径）
	DaemonArgs []string // daemon 启动参数
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

// Prompt 发送 prompt 请求
func (c *Conn) Prompt(text, modelID, cwd string, onChunk func(string), onToolCall func(kind, title, status string)) (stopReason string, err error) {
	req := Request{
		Type:    ReqPrompt,
		Text:    text,
		ModelID: modelID,
		Cwd:     cwd,
	}
	if err := c.send(req); err != nil {
		return "", err
	}
	return c.readUntilDone(onChunk, onToolCall)
}

// Compact 发送 compact 请求
func (c *Conn) Compact() error {
	if err := c.send(Request{Type: ReqCompact}); err != nil {
		return err
	}
	_, err := c.readUntilDone(nil, nil)
	return err
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
	for {
		resp, err := c.readOne()
		if err != nil {
			return "", err
		}
		switch resp.Type {
		case RespChunk:
			if onChunk != nil {
				onChunk(resp.Text)
			}
		case RespToolCall:
			if onToolCall != nil {
				onToolCall(resp.ToolKind, resp.ToolTitle, resp.ToolStatus)
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
