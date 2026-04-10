package daemon

import "encoding/json"

// CLI <-> daemon 之间的通信协议，基于 Unix socket + 换行分隔 JSON

// --- 请求（CLI → daemon）---

type RequestType string

const (
	ReqPrompt        RequestType = "prompt"
	ReqCompact       RequestType = "compact"
	ReqStatus        RequestType = "status"
	ReqShutdown      RequestType = "shutdown"
	ReqSessionNew    RequestType = "session_new"    // 创建新 session
	ReqSessionClose  RequestType = "session_close"  // 关闭指定 session
	ReqSessionList   RequestType = "session_list"   // 列出所有 session
)

type Request struct {
	Type      RequestType `json:"type"`
	SessionID string     `json:"sessionId,omitempty"` // 路由到指定 session
	Cwd       string     `json:"cwd,omitempty"`
	Text      string     `json:"text,omitempty"`
	ModelID   string     `json:"modelId,omitempty"`
}

// SessionResponse 创建/查询 session 的响应
type SessionResponse struct {
	SessionID    string `json:"sessionId"`
	ACPSessionID string `json:"acpSessionId"`
	ModelID      string `json:"modelId,omitempty"`
	Cwd          string `json:"cwd,omitempty"`
	Uptime       string `json:"uptime,omitempty"`
}

// --- 响应（daemon → CLI）---

type ResponseType string

const (
	RespReady       ResponseType = "ready"
	RespChunk       ResponseType = "chunk"
	RespThought     ResponseType = "thought"       // 模型思考过程片段
	RespToolCall    ResponseType = "tool_call"
	RespToolResult  ResponseType = "tool_result"   // 工具调用结果
	RespCommands    ResponseType = "commands"       // 可用命令列表
	RespDone        ResponseType = "done"
	RespStatus      ResponseType = "status"
	RespError       ResponseType = "error"
	RespSessionNew  ResponseType = "session_new"   // 创建 session 成功
	RespSessionList ResponseType = "session_list"   // session 列表
)

type Response struct {
	Type       ResponseType `json:"type"`
	Text       string       `json:"text,omitempty"`
	StopReason string       `json:"stopReason,omitempty"`
	SessionID  string       `json:"sessionId,omitempty"`
	ModelID    string       `json:"modelId,omitempty"`
	ToolKind   string       `json:"toolKind,omitempty"`
	ToolTitle  string       `json:"toolTitle,omitempty"`
	ToolStatus string       `json:"toolStatus,omitempty"`
	ToolCallID string       `json:"toolCallId,omitempty"` // 工具调用 ID，用于关联 tool_call 和 tool_result
	Error      string       `json:"error,omitempty"`
	PID        int          `json:"pid,omitempty"`
	Uptime     string       `json:"uptime,omitempty"`
	Commands   []CommandInfo `json:"commands,omitempty"` // 可用命令列表
}

// CommandInfo daemon 层的命令信息（精简版）
type CommandInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Encode 将响应序列化为一行 JSON + 换行
func Encode(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Decode 从 JSON 反序列化
func Decode[T any](data []byte) (*T, error) {
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return &v, nil
}
