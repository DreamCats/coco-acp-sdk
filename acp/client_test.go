package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

// =============================================
// TestHelperProcess: 模拟 coco acp serve 的假进程
// =============================================

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_TEST_ACP_MOCK") != "1" {
		return
	}

	mode := os.Getenv("GO_TEST_ACP_MODE")
	switch mode {
	case "normal":
		mockNormalServer()
	case "crash_after_handshake":
		mockCrashAfterHandshake()
	case "prompt_with_notifications":
		mockPromptWithNotifications()
	default:
		mockNormalServer()
	}

	os.Exit(0)
}

func mockNormalServer() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		var req Request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}

		switch req.Method {
		case "initialize":
			writeJSON(Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  mustMarshal(InitializeResult{ProtocolVersion: 1}),
			})
		case "session/new":
			writeJSON(Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: mustMarshal(SessionNewResult{
					SessionID: "test-session-001",
					Models: ModelsInfo{
						CurrentModelID:  "Doubao-Seed-2.0-Code",
						AvailableModels: []ModelInfo{{ID: "Doubao-Seed-2.0-Code", Name: "Doubao-Seed-2.0-Code"}},
					},
					Modes: ModesInfo{
						CurrentModeID:  "default",
						AvailableModes: []ModeInfo{{ID: "default", Name: "Default"}},
					},
				}),
			})
		case "session/prompt":
			writeJSON(Response{
				JSONRPC: "2.0",
				Method:  "session/update",
				Params: mustMarshal(SessionUpdateParams{
					SessionID: "test-session-001",
					Update: SessionUpdate{
						SessionUpdate: "agent_message_chunk",
						Content:       &TextContent{Type: "text", Text: "你好世界"},
					},
				}),
			})
			writeJSON(Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  mustMarshal(SessionPromptResult{StopReason: "end_turn"}),
			})
		}
	}
}

func mockCrashAfterHandshake() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		var req Request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}

		switch req.Method {
		case "initialize":
			writeJSON(Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  mustMarshal(InitializeResult{ProtocolVersion: 1}),
			})
		case "session/new":
			writeJSON(Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: mustMarshal(SessionNewResult{
					SessionID: "test-session-crash",
					Models: ModelsInfo{
						CurrentModelID:  "Doubao-Seed-2.0-Code",
						AvailableModels: []ModelInfo{{ID: "Doubao-Seed-2.0-Code", Name: "Doubao-Seed-2.0-Code"}},
					},
					Modes: ModesInfo{
						CurrentModeID:  "default",
						AvailableModes: []ModeInfo{{ID: "default", Name: "Default"}},
					},
				}),
			})
			return
		}
	}
}

func mockPromptWithNotifications() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		var req Request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}

		switch req.Method {
		case "initialize":
			writeJSON(Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  mustMarshal(InitializeResult{ProtocolVersion: 1}),
			})
		case "session/new":
			writeJSON(Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: mustMarshal(SessionNewResult{
					SessionID: "test-session-notify",
					Models: ModelsInfo{
						CurrentModelID:  "Doubao-Seed-2.0-Code",
						AvailableModels: []ModelInfo{{ID: "Doubao-Seed-2.0-Code", Name: "Doubao-Seed-2.0-Code"}},
					},
					Modes: ModesInfo{
						CurrentModeID:  "default",
						AvailableModes: []ModeInfo{{ID: "default", Name: "Default"}},
					},
				}),
			})
		case "session/prompt":
			writeJSON(Response{
				JSONRPC: "2.0",
				Method:  "session/update",
				Params: mustMarshal(SessionUpdateParams{
					SessionID: "test-session-notify",
					Update: SessionUpdate{
						SessionUpdate: "tool_call",
						Kind:          "read",
						Title:         "Read /tmp/test.go",
						Status:        "in_progress",
					},
				}),
			})
			writeJSON(Response{
				JSONRPC: "2.0",
				Method:  "session/update",
				Params: mustMarshal(SessionUpdateParams{
					SessionID: "test-session-notify",
					Update: SessionUpdate{
						SessionUpdate: "tool_call",
						Kind:          "read",
						Title:         "Read /tmp/test.go",
						Status:        "done",
					},
				}),
			})
			writeJSON(Response{
				JSONRPC: "2.0",
				Method:  "session/update",
				Params: mustMarshal(SessionUpdateParams{
					SessionID: "test-session-notify",
					Update: SessionUpdate{
						SessionUpdate: "agent_message_chunk",
						Content:       &TextContent{Type: "text", Text: "第一段"},
					},
				}),
			})
			writeJSON(Response{
				JSONRPC: "2.0",
				Method:  "session/update",
				Params: mustMarshal(SessionUpdateParams{
					SessionID: "test-session-notify",
					Update: SessionUpdate{
						SessionUpdate: "agent_message_chunk",
						Content:       &TextContent{Type: "text", Text: "第二段"},
					},
				}),
			})
			writeJSON(Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  mustMarshal(SessionPromptResult{StopReason: "end_turn"}),
			})
		}
	}
}

func writeJSON(v any) {
	data, _ := json.Marshal(v)
	fmt.Fprintf(os.Stdout, "%s\n", data)
}

func mustMarshal(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

func newMockClient(t *testing.T, mode string, opts ...Option) *Client {
	t.Helper()
	allOpts := []Option{
		WithTimeout(10 * time.Second),
		WithCommandFactory(func(ctx context.Context) *exec.Cmd {
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestHelperProcess")
			cmd.Env = append(os.Environ(),
				"GO_TEST_ACP_MOCK=1",
				"GO_TEST_ACP_MODE="+mode,
			)
			return cmd
		}),
	}
	allOpts = append(allOpts, opts...)
	return NewClient("/tmp", allOpts...)
}

// =============================================
// 测试用例
// =============================================

func TestStartAndHandshake(t *testing.T) {
	c := newMockClient(t, "normal")
	defer c.Close()

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	if c.SessionID() != "test-session-001" {
		t.Errorf("SessionID = %q, want %q", c.SessionID(), "test-session-001")
	}

	models := c.Models()
	if models == nil {
		t.Fatal("Models 为 nil")
	}
	if models.CurrentModelID != "Doubao-Seed-2.0-Code" {
		t.Errorf("CurrentModelID = %q, want %q", models.CurrentModelID, "Doubao-Seed-2.0-Code")
	}
	if len(models.AvailableModels) != 1 {
		t.Errorf("AvailableModels 数量 = %d, want 1", len(models.AvailableModels))
	}
}

func TestStartIdempotent(t *testing.T) {
	c := newMockClient(t, "normal")
	defer c.Close()

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("第一次 Start 失败: %v", err)
	}
	sid := c.SessionID()

	if err := c.Start(ctx); err != nil {
		t.Fatalf("第二次 Start 失败: %v", err)
	}
	if c.SessionID() != sid {
		t.Errorf("重复 Start 后 SessionID 变了: %q -> %q", sid, c.SessionID())
	}
}

func TestPrompt(t *testing.T) {
	c := newMockClient(t, "normal")
	defer c.Close()

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	stopReason, err := c.Prompt(ctx, "你好", "")
	if err != nil {
		t.Fatalf("Prompt 失败: %v", err)
	}
	if stopReason != "end_turn" {
		t.Errorf("stopReason = %q, want %q", stopReason, "end_turn")
	}
}

func TestPromptWithModelSwitch(t *testing.T) {
	c := newMockClient(t, "normal")
	defer c.Close()

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	stopReason, err := c.Prompt(ctx, "测试切模型", "GPT-5")
	if err != nil {
		t.Fatalf("Prompt 失败: %v", err)
	}
	if stopReason != "end_turn" {
		t.Errorf("stopReason = %q, want %q", stopReason, "end_turn")
	}
}

func TestPromptNotifications(t *testing.T) {
	var mu sync.Mutex
	var notifications []SessionUpdate

	c := newMockClient(t, "prompt_with_notifications",
		WithNotifyHandler(func(method string, update *SessionUpdate) {
			mu.Lock()
			defer mu.Unlock()
			notifications = append(notifications, *update)
		}),
	)
	defer c.Close()

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	stopReason, err := c.Prompt(ctx, "分析代码", "")
	if err != nil {
		t.Fatalf("Prompt 失败: %v", err)
	}
	if stopReason != "end_turn" {
		t.Errorf("stopReason = %q, want %q", stopReason, "end_turn")
	}

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if len(notifications) != 4 {
		t.Fatalf("收到 %d 条通知, want 4", len(notifications))
	}
	if notifications[0].SessionUpdate != "tool_call" || notifications[0].Status != "in_progress" {
		t.Errorf("通知[0] = %+v, want tool_call in_progress", notifications[0])
	}
	if notifications[1].SessionUpdate != "tool_call" || notifications[1].Status != "done" {
		t.Errorf("通知[1] = %+v, want tool_call done", notifications[1])
	}
	if notifications[2].SessionUpdate != "agent_message_chunk" || notifications[2].Content.Text != "第一段" {
		t.Errorf("通知[2] = %+v, want chunk '第一段'", notifications[2])
	}
	if notifications[3].SessionUpdate != "agent_message_chunk" || notifications[3].Content.Text != "第二段" {
		t.Errorf("通知[3] = %+v, want chunk '第二段'", notifications[3])
	}
}

func TestProcessCrashAndAutoRestart(t *testing.T) {
	callCount := 0
	c := NewClient("/tmp",
		WithTimeout(10*time.Second),
		WithCommandFactory(func(ctx context.Context) *exec.Cmd {
			callCount++
			var mode string
			if callCount == 1 {
				mode = "crash_after_handshake"
			} else {
				mode = "normal"
			}
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestHelperProcess")
			cmd.Env = append(os.Environ(),
				"GO_TEST_ACP_MOCK=1",
				"GO_TEST_ACP_MODE="+mode,
			)
			return cmd
		}),
	)
	defer c.Close()

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	if c.SessionID() != "test-session-crash" {
		t.Fatalf("SessionID = %q, want %q", c.SessionID(), "test-session-crash")
	}

	time.Sleep(200 * time.Millisecond)

	stopReason, err := c.Prompt(ctx, "重启后的消息", "")
	if err != nil {
		t.Fatalf("崩溃后 Prompt 失败: %v", err)
	}
	if stopReason != "end_turn" {
		t.Errorf("stopReason = %q, want %q", stopReason, "end_turn")
	}
	if c.SessionID() != "test-session-001" {
		t.Errorf("重启后 SessionID = %q, want %q", c.SessionID(), "test-session-001")
	}
	if callCount != 2 {
		t.Errorf("进程启动次数 = %d, want 2", callCount)
	}
}

func TestClose(t *testing.T) {
	c := newMockClient(t, "normal")

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	if c.SessionID() != "" {
		t.Errorf("Close 后 SessionID 应该为空, got %q", c.SessionID())
	}
	if c.Models() != nil {
		t.Error("Close 后 Models 应该为 nil")
	}
}

func TestContextCancel(t *testing.T) {
	c := newMockClient(t, "normal")
	defer c.Close()

	ctx, cancel := context.WithCancel(context.Background())
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	cancel()
	time.Sleep(100 * time.Millisecond)

	newCtx := context.Background()
	stopReason, err := c.Prompt(newCtx, "新消息", "")
	if err != nil {
		t.Fatalf("重启后 Prompt 失败: %v", err)
	}
	if stopReason != "end_turn" {
		t.Errorf("stopReason = %q, want %q", stopReason, "end_turn")
	}
}
