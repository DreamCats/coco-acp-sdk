# 模型测速报告

测试时间：2026-03-30
测试环境：coco-acp-sdk + coco daemon
测试 Prompt：`用一句话解释 Go 语言中 sync.Map 的适用场景`

## 测试结果

| 模型 | 耗时 | 字符/秒 | 排名 |
|------|------|---------|------|
| **GPT-4o** | 6.194s | 26 | 🥇 最快 |
| **Claude-3.5-Sonnet** | 5.952s | 24 | 🥈 |
| **Kimi-k2** | 5.791s | 21 | 🥉 |
| **Claude-3-Opus** | 6.678s | 21 | 4 |
| **Doubao-Seed-2.0-Code** | 6.648s | 19 | 5 |
| **Gemini-2.0-Flash** | 6.362s | 19 | 6 |
| **Qwen-2.5-Coder** | 6.263s | 18 | 7 |
| **Gemini-2.5-Pro** | 7.549s | 17 | 8 |
| **DeepSeek-Coder-V3** | 8.205s | 17 | 9 |
| **GPT-5** | 7.507s | 14 | 10 |

## 分析与建议

### 速度快（简单任务推荐）
- **GPT-4o** — 综合最快，26 chars/s
- **Claude-3.5-Sonnet** — 速度 24 chars/s + 质量高
- **Kimi-k2** — 国产模型，21 chars/s，性价比不错

### 质量高（复杂任务推荐）
- **Claude-3-Opus** — 速度略慢但质量顶级
- **GPT-5** — 速度最慢但能力最强

### 性价比平衡
- **Doubao-Seed-2.0-Code** — 默认模型，19 chars/s，中规中矩
- **Qwen-2.5-Coder** — 18 chars/s，国产开源可选

### 不推荐
- **Gemini-2.5-Pro** — 速度偏慢（17 chars/s），性价比不高
- **DeepSeek-Coder-V3** — 速度垫底（17 chars/s）

## coco-acp-sdk 使用方式

```go
// 指定模型
stopReason, err := conn.Prompt(
    "你的问题",
    "GPT-4o",  // 指定模型 ID
    "",
    func(text string) { fmt.Print(text) },
    nil,
)
```

## 可用模型列表

SDK 透传 coco 的 20+ 个模型，全走公司免费额度：

- **字节系**：Doubao-Seed-2.0-Code（默认）
- **OpenAI**：GPT-5、GPT-4o
- **Google**：Gemini-2.5-Pro、Gemini-2.0-Flash
- **DeepSeek**：DeepSeek-Coder-V3
- **Kimi**：Kimi-k2
- **Qwen**：Qwen-2.5-Coder
- **Claude**：Claude-3.5-Sonnet、Claude-3-Opus

## 注意事项

1. **实际可用模型**由 coco 服务端决定，以上测试结果可能因模型配额变化而不同
2. **速度测试**仅为单次测试结果，真实场景会有波动
3. **模型切换**只需在 Prompt 时传入不同的 modelID，无需重启 daemon
