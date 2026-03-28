package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/DreamCats/coco-acp-sdk/acp"
)

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_TEST_ACP_MOCK") != "1" {
		return
	}

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}

		switch req.Method {
		case "initialize":
			writeJSON(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  map[string]any{"protocolVersion": 1},
			})
		case "session/new":
			writeJSON(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"sessionId": "daemon-test-session",
					"models": map[string]any{
						"currentModelId":  "Doubao-Seed-2.0-Code",
						"availableModels": []map[string]string{{"id": "Doubao-Seed-2.0-Code", "name": "Doubao-Seed-2.0-Code"}},
					},
					"modes": map[string]any{
						"currentModeId":  "default",
						"availableModes": []map[string]string{{"id": "default", "name": "Default"}},
					},
				},
			})
		case "session/prompt":
			writeJSON(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": "daemon-test-session",
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]string{"type": "text", "text": "daemon 回复"},
					},
				},
			})
			writeJSON(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  map[string]string{"stopReason": "end_turn"},
			})
		}
	}
}

func writeJSON(v any) {
	data, _ := json.Marshal(v)
	fmt.Fprintf(os.Stdout, "%s\n", data)
}

func mockCommandFactory() acp.CommandFactory {
	return func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestHelperProcess")
		cmd.Env = append(os.Environ(), "GO_TEST_ACP_MOCK=1")
		return cmd
	}
}

func startTestServer(t *testing.T) (string, func()) {
	t.Helper()

	tmpDir := t.TempDir()
	server := NewServer(tmpDir, "/tmp", 0)
	server.SetCommandFactoryForTest(mockCommandFactory())

	ready := make(chan error, 1)

	go func() {
		if err := os.MkdirAll(tmpDir, 0700); err != nil {
			ready <- err
			return
		}

		// 创建默认 session
		if _, err := server.sessions.CreateSession(server.ctx, "/tmp"); err != nil {
			ready <- err
			return
		}

		sockPath := filepath.Join(tmpDir, "daemon.sock")
		_ = os.Remove(sockPath)

		var err error
		server.listener, err = net.Listen("unix", sockPath)
		if err != nil {
			ready <- err
			return
		}
		_ = os.Chmod(sockPath, 0600)

		server.idleTimer = time.AfterFunc(30*time.Second, func() {
			server.shutdown()
		})

		ready <- nil
		server.acceptLoop()
	}()

	if err := <-ready; err != nil {
		t.Fatalf("启动测试 server 失败: %v", err)
	}

	sockPath := filepath.Join(tmpDir, "daemon.sock")
	return sockPath, func() { server.shutdown() }
}

func dialTest(t *testing.T, sockPath string) *Conn {
	t.Helper()
	conn, err := net.DialTimeout("unix", sockPath, 2*time.Second)
	if err != nil {
		t.Fatalf("连接 daemon 失败: %v", err)
	}
	return newConn(conn)
}

func TestServerPrompt(t *testing.T) {
	sockPath, cleanup := startTestServer(t)
	defer cleanup()

	c := dialTest(t, sockPath)
	defer c.Close()

	var chunks []string
	stopReason, err := c.Prompt("你好", "", "", func(text string) {
		chunks = append(chunks, text)
	}, nil)

	if err != nil {
		t.Fatalf("Prompt 失败: %v", err)
	}
	if stopReason != "end_turn" {
		t.Errorf("stopReason = %q, want %q", stopReason, "end_turn")
	}
	if len(chunks) == 0 {
		t.Error("没有收到任何 chunk")
	}
	if len(chunks) > 0 && chunks[0] != "daemon 回复" {
		t.Errorf("chunk[0] = %q, want %q", chunks[0], "daemon 回复")
	}
}

func TestServerStatus(t *testing.T) {
	sockPath, cleanup := startTestServer(t)
	defer cleanup()

	c := dialTest(t, sockPath)
	defer c.Close()

	resp, err := c.Status()
	if err != nil {
		t.Fatalf("Status 失败: %v", err)
	}
	if resp.Type != RespStatus {
		t.Errorf("Type = %q, want %q", resp.Type, RespStatus)
	}
	if resp.PID == 0 {
		t.Error("PID 不应为 0")
	}
	if resp.SessionID != "daemon-test-session" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "daemon-test-session")
	}
}

func TestServerShutdown(t *testing.T) {
	sockPath, cleanup := startTestServer(t)
	defer cleanup()

	c := dialTest(t, sockPath)
	defer c.Close()

	if err := c.Shutdown(); err != nil {
		t.Fatalf("Shutdown 失败: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	_, err := net.DialTimeout("unix", sockPath, 1*time.Second)
	if err == nil {
		t.Error("daemon 关闭后仍可连接")
	}
}

func TestServerSerialPrompt(t *testing.T) {
	sockPath, cleanup := startTestServer(t)
	defer cleanup()

	var wg sync.WaitGroup
	errors := make(chan error, 3)

	for i := range 3 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			c := dialTest(t, sockPath)
			defer c.Close()

			_, err := c.Prompt(fmt.Sprintf("并发测试 %d", idx), "", "", nil, nil)
			if err != nil {
				errors <- fmt.Errorf("prompt %d: %w", idx, err)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Error(err)
	}
}
