//go:build ignore

package main

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"time"

	"github.com/DreamCats/coco-acp-sdk/daemon"
)

func main() {
	repoPath := "."
	if len(os.Args) > 1 {
		repoPath = os.Args[1]
	}

	usr, _ := user.Current()
	configDir := filepath.Join(usr.HomeDir, ".config", "coco-ext")

	conn, err := daemon.Dial(repoPath, &daemon.DialOption{
		ConfigDir: configDir,
	})
	if err != nil {
		fmt.Printf("连接 daemon 失败: %v\n", err)
		return
	}
	defer conn.Close()

	// 获取状态信息和可用模型
	status, err := conn.Status()
	if err != nil {
		fmt.Printf("获取状态失败: %v\n", err)
		return
	}

	fmt.Printf("=== Coco 模型测速 ===\n")
	fmt.Printf("Daemon PID: %d\n", status.PID)
	fmt.Printf("当前模型: %s\n", status.ModelID)
	fmt.Printf("运行时长: %s\n", status.Uptime)
	fmt.Println()

	// 创建新 session 来获取可用模型列表
	sess, err := conn.NewSession(repoPath)
	if err != nil {
		fmt.Printf("创建 session 失败: %v\n", err)
		return
	}
	conn.UseSession(sess.SessionID)

	// 获取可用模型（通过 session/new 响应）
	// 注意：实际可用模型由 coco 服务端决定，这里我们测试几个常见模型

	testModels := []string{
		// 字节系
		"Doubao-Seed-2.0-Code",
		// GPT 系列
		"GPT-5",
		"GPT-4o",
		// Gemini
		"Gemini-2.5-Pro",
		"Gemini-2.0-Flash",
		// DeepSeek
		"DeepSeek-Coder-V3",
		// Kimi
		"Kimi-k2",
		// Qwen
		"Qwen-2.5-Coder",
		// Claude (如果可用)
		"Claude-3.5-Sonnet",
		"Claude-3-Opus",
	}

	testPrompt := "用一句话解释 Go 语言中 sync.Map 的适用场景"

	fmt.Printf("测试 Prompt: %s\n", testPrompt)
	fmt.Printf("\n=== 测速结果 ===\n\n")

	for _, modelID := range testModels {
		start := time.Now()

		var result string
		var stopReason string
		var lastErr error

		// 创建独立 session 测试每个模型
		testSess, _ := conn.NewSession(repoPath)
		conn.UseSession(testSess.SessionID)

		stopReason, lastErr = conn.Prompt(
			testPrompt,
			modelID,
			"",
			func(text string) {
				result += text
			},
			nil,
		)

		elapsed := time.Since(start)

		if lastErr != nil {
			fmt.Printf("%-25s ❌ 失败: %v\n", modelID, lastErr)
			continue
		}

		// 计算每秒处理的字符数
		charsPerSec := float64(len(result)) / elapsed.Seconds()

		fmt.Printf("%-25s ✅ %v (%.0f chars/s, stopReason: %s)\n",
			modelID, elapsed.Round(time.Millisecond), charsPerSec, stopReason)

		// 关闭测试 session
		conn.CloseSession(testSess.SessionID)
	}

	fmt.Println("\n=== 测试完成 ===")
}

// Run with:
// go run tools/model_benchmark.go
