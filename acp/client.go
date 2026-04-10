package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrNotRunning  = errors.New("acp: 进程未运行")
	ErrTimeout     = errors.New("acp: 请求超时")
	ErrProcessDied = errors.New("acp: 进程已退出")
)

// NotifyHandler 处理 coco acp 推送的通知
type NotifyHandler func(method string, update *SessionUpdate)

// ServeFlags 是传给 coco acp serve 的命令行参数
type ServeFlags struct {
	Yolo             bool          // -y, --yolo: 跳过工具权限检查
	AllowedTools     []string      // --allowed-tool: 自动批准的工具列表
	DisallowedTools  []string      // --disallowed-tool: 自动拒绝的工具列表
	BashToolTimeout  time.Duration // --bash-tool-timeout: Bash 工具超时
	QueryTimeout     time.Duration // --query-timeout: 单次查询超时
	Configs          []string      // -c, --config: 覆盖配置 k=v
}

// toArgs 将 ServeFlags 转换为命令行参数
func (f *ServeFlags) toArgs() []string {
	if f == nil {
		return nil
	}
	var args []string
	if f.Yolo {
		args = append(args, "--yolo")
	}
	for _, t := range f.AllowedTools {
		args = append(args, "--allowed-tool", t)
	}
	for _, t := range f.DisallowedTools {
		args = append(args, "--disallowed-tool", t)
	}
	if f.BashToolTimeout > 0 {
		args = append(args, "--bash-tool-timeout", f.BashToolTimeout.String())
	}
	if f.QueryTimeout > 0 {
		args = append(args, "--query-timeout", f.QueryTimeout.String())
	}
	for _, c := range f.Configs {
		args = append(args, "--config", c)
	}
	return args
}

// CommandFactory 创建子进程的工厂函数，测试时可替换
type CommandFactory func(ctx context.Context) *exec.Cmd

// Client 管理 coco acp serve 子进程的完整生命周期
type Client struct {
	cwd        string
	timeout    time.Duration
	clientName string
	serveFlags *ServeFlags
	newCommand CommandFactory

	mu     sync.Mutex // 保护 proc / session 等状态
	proc   *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	writeMu sync.Mutex // 保护 stdin 写入

	nextID     atomic.Int64
	pending    sync.Map // map[int64]chan *Response
	readerDone atomic.Bool
	done       chan struct{} // readLoop 退出时关闭
	waitDone   chan struct{} // proc.Wait() 完成时关闭

	sessionID string
	models    *ModelsInfo

	onNotify NotifyHandler
}

// Option 配置选项
type Option func(*Client)

func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.timeout = d }
}

func WithNotifyHandler(h NotifyHandler) Option {
	return func(c *Client) { c.onNotify = h }
}

func WithCommandFactory(f CommandFactory) Option {
	return func(c *Client) { c.newCommand = f }
}

// WithServeFlags 设置传给 coco acp serve 的命令行参数
func WithServeFlags(flags *ServeFlags) Option {
	return func(c *Client) { c.serveFlags = flags }
}

// WithYolo 开启 yolo 模式（跳过工具权限检查）
func WithYolo() Option {
	return func(c *Client) {
		if c.serveFlags == nil {
			c.serveFlags = &ServeFlags{}
		}
		c.serveFlags.Yolo = true
	}
}

// WithClientName 设置握手时的客户端名称（默认 "coco-acp-sdk"）
func WithClientName(name string) Option {
	return func(c *Client) { c.clientName = name }
}

// NewClient 创建 ACP 客户端，不会立即启动进程
func NewClient(cwd string, opts ...Option) *Client {
	c := &Client{
		cwd:        cwd,
		timeout:    120 * time.Second,
		clientName: "coco-acp-sdk",
		done:       make(chan struct{}),
	}
	for _, o := range opts {
		o(c)
	}
	if c.newCommand == nil {
		c.newCommand = func(ctx context.Context) *exec.Cmd {
			args := []string{"acp", "serve"}
			args = append(args, c.serveFlags.toArgs()...)
			return exec.CommandContext(ctx, "coco", args...)
		}
	}
	return c
}

// Start 启动 coco acp serve 子进程并完成握手（initialize + session/new）
func (c *Client) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.proc != nil && !c.readerDone.Load() {
		return nil // 已经在运行
	}

	// 清理可能残留的旧进程
	c.cleanupLocked()

	return c.startLocked(ctx)
}

// startLocked 启动子进程 + 握手，调用方必须持有 c.mu
func (c *Client) startLocked(ctx context.Context) error {
	c.done = make(chan struct{})
	c.readerDone.Store(false)

	proc := c.newCommand(ctx)
	var err error

	c.stdin, err = proc.StdinPipe()
	if err != nil {
		return fmt.Errorf("acp: 创建 stdin pipe 失败: %w", err)
	}
	c.stdout, err = proc.StdoutPipe()
	if err != nil {
		return fmt.Errorf("acp: 创建 stdout pipe 失败: %w", err)
	}
	c.stderr, err = proc.StderrPipe()
	if err != nil {
		return fmt.Errorf("acp: 创建 stderr pipe 失败: %w", err)
	}

	if err := proc.Start(); err != nil {
		return fmt.Errorf("acp: 启动 coco acp serve 失败: %w", err)
	}
	c.proc = proc

	// 启动 stdout 读取 goroutine
	go c.readLoop()

	// 启动 stderr 消费 goroutine（防止 pipe 阻塞）
	go c.drainStderr()

	// 后台等待进程退出
	c.waitDone = make(chan struct{})
	go func() {
		_ = proc.Wait()
		close(c.waitDone)
	}()

	// 握手
	if err := c.handshake(ctx); err != nil {
		c.cleanupLocked()
		return fmt.Errorf("acp: 握手失败: %w", err)
	}

	return nil
}

// handshake 执行 initialize + session/new
func (c *Client) handshake(ctx context.Context) error {
	// 1. initialize
	initParams := InitializeParams{
		ProtocolVersion: 1,
		Capabilities:    struct{}{},
		ClientInfo:      ClientInfo{Name: c.clientName, Version: "0.1.0"},
	}
	resp, err := c.call(ctx, "initialize", initParams)
	if err != nil {
		return fmt.Errorf("initialize 失败: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("initialize 返回错误: %w", resp.Error)
	}

	// 2. session/new
	sessionParams := SessionNewParams{
		Cwd:        c.cwd,
		McpServers: []any{},
	}
	resp, err = c.call(ctx, "session/new", sessionParams)
	if err != nil {
		return fmt.Errorf("session/new 失败: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("session/new 返回错误: %w", resp.Error)
	}

	var result SessionNewResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("解析 session/new 响应失败: %w", err)
	}
	c.sessionID = result.SessionID
	c.models = &result.Models

	return nil
}

// Prompt 发送消息，流式回调通知，返回 stopReason
func (c *Client) Prompt(ctx context.Context, text string, modelID string) (string, error) {
	if err := c.ensureRunning(ctx); err != nil {
		return "", err
	}

	params := SessionPromptParams{
		SessionID: c.sessionID,
		Prompt:    []PromptPart{{Type: "text", Text: text}},
	}
	if modelID != "" {
		params.ModelID = modelID
	}

	id := c.nextID.Add(1)
	ch := make(chan *Response, 1)
	c.pending.Store(id, ch)
	defer c.pending.Delete(id)

	if err := c.send(id, "session/prompt", params); err != nil {
		return "", err
	}

	// 等待最终 result
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return "", resp.Error
		}
		var result SessionPromptResult
		_ = json.Unmarshal(resp.Result, &result)
		return result.StopReason, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-c.done:
		return "", ErrProcessDied
	}
}

// Compact 执行上下文压缩
func (c *Client) Compact(ctx context.Context) error {
	_, err := c.Prompt(ctx, "/compact", "")
	return err
}

// SessionID 返回当前会话 ID
func (c *Client) SessionID() string {
	return c.sessionID
}

// Models 返回可用模型列表
func (c *Client) Models() *ModelsInfo {
	return c.models
}

// SetNotifyHandler 动态更换通知回调
func (c *Client) SetNotifyHandler(h NotifyHandler) {
	c.onNotify = h
}

// Close 关闭客户端，终止子进程
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked()
	return nil
}

// --- 内部方法 ---

// ensureRunning 确保进程在运行，如果崩了就重启
func (c *Client) ensureRunning(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.proc != nil && !c.readerDone.Load() {
		return nil
	}

	c.cleanupLocked()
	return c.startLocked(ctx)
}

// call 发送 JSON-RPC 请求并等待对应 id 的响应
func (c *Client) call(ctx context.Context, method string, params any) (*Response, error) {
	id := c.nextID.Add(1)
	ch := make(chan *Response, 1)
	c.pending.Store(id, ch)
	defer c.pending.Delete(id)

	if err := c.send(id, method, params); err != nil {
		return nil, err
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(c.timeout):
		return nil, ErrTimeout
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, ErrProcessDied
	}
}

// send 将请求写入 stdin
func (c *Client) send(id int64, method string, params any) error {
	req := Request{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("acp: 序列化请求失败: %w", err)
	}
	data = append(data, '\n')

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if c.stdin == nil {
		return ErrNotRunning
	}
	_, err = c.stdin.Write(data)
	if err != nil {
		return fmt.Errorf("acp: 写入 stdin 失败: %w", err)
	}
	return nil
}

// readLoop 持续读取 stdout，分发 result 和 notification
func (c *Client) readLoop() {
	defer func() {
		c.readerDone.Store(true)
		close(c.done)
		// 唤醒所有等待中的 call
		c.pending.Range(func(key, value any) bool {
			if ch, ok := value.(chan *Response); ok {
				select {
				case ch <- &Response{Error: &RPCError{Code: -1, Message: "进程已退出"}}:
				default:
				}
			}
			return true
		})
	}()

	decoder := json.NewDecoder(c.stdout)
	for {
		var msg Response
		if err := decoder.Decode(&msg); err != nil {
			return // EOF 或解析错误 → 进程挂了
		}

		if msg.IsResult() {
			if val, ok := c.pending.Load(msg.ID); ok {
				if ch, ok := val.(chan *Response); ok {
					ch <- &msg
				}
			}
		} else if msg.IsNotification() {
			c.handleNotification(&msg)
		}
	}
}

// handleNotification 处理 session/update 通知
func (c *Client) handleNotification(msg *Response) {
	if c.onNotify == nil || msg.Method != "session/update" {
		return
	}

	var params SessionUpdateParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return
	}
	c.onNotify(msg.Method, &params.Update)
}

// drainStderr 消费 stderr 防止 pipe buffer 满导致子进程阻塞
func (c *Client) drainStderr() {
	if c.stderr == nil {
		return
	}
	buf := make([]byte, 4096)
	for {
		_, err := c.stderr.Read(buf)
		if err != nil {
			return
		}
	}
}

// cleanupLocked 清理子进程资源，调用方必须持有 c.mu
func (c *Client) cleanupLocked() {
	if c.stdin != nil {
		_ = c.stdin.Close()
		c.stdin = nil
	}
	if c.proc != nil && c.proc.Process != nil {
		_ = c.proc.Process.Kill()
		if c.waitDone != nil {
			<-c.waitDone
		}
		c.proc = nil
	}
	c.sessionID = ""
	c.models = nil
}
