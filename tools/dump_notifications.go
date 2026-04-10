// dump_notifications 诊断工具：启动 coco acp serve --yolo，发送 prompt，
// 打印所有 notification 的原始 JSON，用于观察底层实际返回了哪些字段。
//
// 用法: go run tools/dump_notifications.go "你的prompt"
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/DreamCats/coco-acp-sdk/acp"
)

func main() {
	prompt := "列出当前目录的文件"
	if len(os.Args) > 1 {
		prompt = os.Args[1]
	}

	cwd, _ := os.Getwd()

	client := acp.NewClient(cwd,
		acp.WithYolo(),
		acp.WithTimeout(120*time.Second),
		// 原始 JSON dump —— 看底层到底返回了什么
		acp.WithRawNotifyHandler(func(method string, raw json.RawMessage) {
			var pretty json.RawMessage
			if json.Unmarshal(raw, &pretty) == nil {
				indented, _ := json.MarshalIndent(pretty, "", "  ")
				fmt.Printf("=== RAW [%s] ===\n%s\n\n", method, string(indented))
			} else {
				fmt.Printf("=== RAW [%s] ===\n%s\n\n", method, string(raw))
			}
		}),
		// 结构化回调 —— 看我们解析出了什么
		acp.WithNotifyHandler(func(method string, update *acp.SessionUpdate) {
			parsed, _ := json.MarshalIndent(update, "", "  ")
			fmt.Printf("--- PARSED [%s] ---\n%s\n\n", method, string(parsed))
		}),
	)
	defer client.Close()

	ctx := context.Background()
	fmt.Println("=== 启动 coco acp serve --yolo ===")
	if err := client.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "启动失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("=== session: %s ===\n", client.SessionID())
	fmt.Printf("=== 发送 prompt: %s ===\n\n", prompt)

	stopReason, err := client.Prompt(ctx, prompt, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "prompt 失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n=== 完成, stopReason: %s ===\n", stopReason)
}
