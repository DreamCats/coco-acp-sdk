# coco-acp-sdk

Go SDK for coco ACP (Agent Communication Protocol) — 封装 `coco acp serve` 的进程管理、JSON-RPC 通信和 daemon 常驻服务。

## 为什么需要这个 SDK？

[coco](https://github.com/anthropics/coco) 是公司内部 AI 编码 CLI，通过 `coco acp serve` 提供 stdio JSON-RPC 接口，支持 20 个模型、工具调用、流式输出。

但直接使用有几个痛点：
- 每次启动冷启动 ~14s
- 需要自己管理子进程生命周期
- stdio JSON-RPC 多路复用（notification + result 交错）较复杂
- 进程崩溃后需要重建 session

**coco-acp-sdk 解决了这些问题**，让上层 agent 只关心"发什么 prompt、拿到结果做什么"。

## 架构

```
┌──────────────────────────────────────┐
│  上层 Agent（你的项目）               │
│  import "coco-acp-sdk/daemon"        │
│  daemon.Dial() → conn.Prompt()       │
└──────────────┬───────────────────────┘
               │  Unix socket
┌──────────────▼───────────────────────┐
│  daemon/  常驻服务                    │
│  - 10 分钟空闲超时自动退出            │
│  - 多 session 并发管理                │
│  - 流式 chunk 回传                    │
└──────────────┬───────────────────────┘
               │  stdio JSON-RPC
┌──────────────▼───────────────────────┐
│  acp/  进程管理                       │
│  - coco acp serve 子进程托管          │
│  - initialize + session/new 握手      │
│  - 崩溃检测 + 自动重启               │
└──────────────────────────────────────┘
```

## 快速开始

### 方式一：通过 daemon（推荐，跨命令复用）

```go
import "github.com/DreamCats/coco-acp-sdk/daemon"

// 连接 daemon（不在则自动拉起）
conn, err := daemon.Dial("/path/to/repo", nil)
if err != nil {
    log.Fatal(err)
}
defer conn.Close()

// 发送 prompt，流式接收
stopReason, err := conn.Prompt(
    "分析这个函数的复杂度",
    "",    // modelID，空则用默认模型
    "",    // cwd，空则用 daemon 当前目录
    func(text string) { fmt.Print(text) },          // onChunk
    func(kind, title, status string) {               // onToolCall
        fmt.Printf("[%s] %s\n", status, title)
    },
)
```

### 方式二：直接使用 acp.Client（单次调用）

```go
import "github.com/DreamCats/coco-acp-sdk/acp"

client := acp.NewClient("/path/to/repo",
    acp.WithNotifyHandler(func(method string, update *acp.SessionUpdate) {
        if update.Content != nil {
            fmt.Print(update.Content.Text)
        }
    }),
    acp.WithYolo(), // 跳过工具权限检查，agent 场景推荐
)
defer client.Close()

ctx := context.Background()
client.Start(ctx)
client.Prompt(ctx, "你好", "")
```

### daemon 管理

上层 agent 可以提供 daemon 管理命令：

```go
// 检查状态
daemon.IsRunning()

// 连接后查询
conn, _ := daemon.Dial(".", nil)
resp, _ := conn.Status()  // PID, SessionID, ModelID, Uptime

// 关闭
conn.Shutdown()
```

### 多 Session 管理

daemon 支持同时管理多个独立的 session，不同 cwd 的连接互不干扰：

```go
conn, _ := daemon.Dial(".", nil)

// 创建新 session（可指定不同 cwd）
sess1, _ := conn.NewSession("/path/to/project-a")

// 列出所有 session
ids, _ := conn.ListSessions()

// 切换当前使用的 session
conn.UseSession(sess1.SessionID)

// 关闭指定 session
conn.CloseSession(sess1.SessionID)
```

**行为说明：**
- 每个 session 独立对应一个 `coco acp serve` 子进程
- 不同 cwd 的连接会创建不同的 session，互不覆盖
- prompt 请求会路由到 `currentSession` 对应的 session
- 每个 session 有独立的空闲超时（默认 10 分钟）

## 自定义配置

```go
conn, err := daemon.Dial("/path/to/repo", &daemon.DialOption{
    ConfigDir:  "/custom/path",                    // 自定义 sock/pid 文件目录
    DaemonCmd:  "/usr/local/bin/my-agent",         // 自定义 daemon 启动命令
    DaemonArgs: []string{"daemon", "start"},       // 启动参数
})
```

## coco acp serve 参数透传

SDK 支持将 `coco acp` 的所有 CLI flags 透传给子进程，agent 自动化场景强烈建议开启 `--yolo`：

```go
// daemon 层：通过 DialOption.ServeFlags 透传
conn, err := daemon.Dial("/path/to/repo", &daemon.DialOption{
    ServeFlags: &acp.ServeFlags{
        Yolo:            true,                        // 跳过工具权限检查
        AllowedTools:    []string{"Bash", "Edit"},    // 自动批准指定工具
        DisallowedTools: []string{"Replace"},         // 自动拒绝指定工具
        QueryTimeout:    5 * time.Minute,             // 单次查询超时
        BashToolTimeout: 30 * time.Second,            // Bash 工具超时
        Configs:         []string{"model=gpt-5"},     // 覆盖配置
    },
})

// acp 层：通过 Option 直接设置
client := acp.NewClient(cwd, acp.WithServeFlags(&acp.ServeFlags{
    Yolo:         true,
    QueryTimeout: 5 * time.Minute,
}))

// 快捷方式：只开 yolo
client := acp.NewClient(cwd, acp.WithYolo())
```

| Flag | ServeFlags 字段 | 说明 |
|---|---|---|
| `-y, --yolo` | `Yolo` | 跳过工具权限检查 |
| `--allowed-tool` | `AllowedTools` | 自动批准的工具列表 |
| `--disallowed-tool` | `DisallowedTools` | 自动拒绝的工具列表 |
| `--bash-tool-timeout` | `BashToolTimeout` | Bash 工具执行超时 |
| `--query-timeout` | `QueryTimeout` | 单次查询超时 |
| `-c, --config` | `Configs` | 覆盖配置（k=v 格式） |

## 可用模型

SDK 透传 coco 的 20 个模型，包括 Doubao-Seed-2.0-Code（默认）、GPT-5 系列、Gemini 系列、DeepSeek、Kimi 等，全走公司免费额度。

## 测试

```bash
go test ./... -v
```

测试使用 Go 标准的 TestHelperProcess 模式，不依赖真实的 `coco` 二进制。

## License

Internal use only.
