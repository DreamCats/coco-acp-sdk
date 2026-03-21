package acp

import "encoding/json"

// --- JSON-RPC 基础结构 ---

// Request 是发给 coco acp serve 的 JSON-RPC 请求
type Request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// Response 是从 coco acp serve 收到的 JSON-RPC 消息（result 或 notification）
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// IsResult 判断是否为请求的响应（有 id）
func (r *Response) IsResult() bool {
	return r.ID != 0
}

// IsNotification 判断是否为通知（无 id，有 method）
func (r *Response) IsNotification() bool {
	return r.ID == 0 && r.Method != ""
}

// RPCError JSON-RPC 错误
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return e.Message
}

// --- initialize ---

type InitializeParams struct {
	ProtocolVersion int        `json:"protocolVersion"`
	Capabilities    struct{}   `json:"capabilities"`
	ClientInfo      ClientInfo `json:"clientInfo"`
}

type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type InitializeResult struct {
	Meta              map[string]any `json:"_meta,omitempty"`
	AgentCapabilities any            `json:"agentCapabilities,omitempty"`
	ProtocolVersion   int            `json:"protocolVersion"`
}

// --- session/new ---

type SessionNewParams struct {
	Cwd        string `json:"cwd"`
	McpServers []any  `json:"mcpServers"`
}

type SessionNewResult struct {
	Meta      map[string]any `json:"_meta,omitempty"`
	SessionID string         `json:"sessionId"`
	Models    ModelsInfo     `json:"models"`
	Modes     ModesInfo      `json:"modes"`
}

type ModelsInfo struct {
	AvailableModels []ModelInfo `json:"availableModels"`
	CurrentModelID  string      `json:"currentModelId"`
}

type ModelInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type ModesInfo struct {
	AvailableModes []ModeInfo `json:"availableModes"`
	CurrentModeID  string     `json:"currentModeId"`
}

type ModeInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// --- session/prompt ---

type SessionPromptParams struct {
	SessionID string       `json:"sessionId"`
	Prompt    []PromptPart `json:"prompt"`
	ModelID   string       `json:"modelId,omitempty"`
}

type PromptPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type SessionPromptResult struct {
	StopReason string `json:"stopReason"`
}

// --- session/update notification ---

type SessionUpdateParams struct {
	SessionID string        `json:"sessionId"`
	Update    SessionUpdate `json:"update"`
}

type SessionUpdate struct {
	SessionUpdate string       `json:"sessionUpdate"` // "tool_call" | "agent_message_chunk"
	Kind          string       `json:"kind,omitempty"`
	Title         string       `json:"title,omitempty"`
	Status        string       `json:"status,omitempty"`
	Content       *TextContent `json:"content,omitempty"`
}

type TextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
