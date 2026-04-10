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

// SessionUpdate 类型常量
const (
	UpdateAgentMessageChunk = "agent_message_chunk"  // 模型输出文本片段
	UpdateAgentThoughtChunk = "agent_thought_chunk"  // 模型思考过程片段
	UpdateToolCall          = "tool_call"             // 工具调用开始
	UpdateToolCallUpdate    = "tool_call_update"      // 工具调用结果
	UpdateAvailableCommands = "available_commands_update" // 可用命令列表
)

type SessionUpdateParams struct {
	SessionID string        `json:"sessionId"`
	Update    SessionUpdate `json:"update"`
}

type SessionUpdate struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Kind          string          `json:"kind,omitempty"`
	Title         string          `json:"title,omitempty"`
	Status        string          `json:"status,omitempty"`
	ToolCallID    string          `json:"toolCallId,omitempty"`
	RawInput      json.RawMessage `json:"rawInput,omitempty"`
	Locations     []Location      `json:"locations,omitempty"`
	Meta          *UpdateMeta     `json:"_meta,omitempty"`

	// Content: agent_message_chunk / agent_thought_chunk 使用（单个文本对象）
	Content *TextContent `json:"-"`
	// ToolResults: tool_call_update 使用（工具输出内容数组）
	ToolResults []ToolResultItem `json:"-"`
	// AvailableCommands: available_commands_update 使用
	AvailableCommands []Command `json:"availableCommands,omitempty"`
}

// UnmarshalJSON 自定义反序列化，处理 content 字段的多态（对象 vs 数组）
func (u *SessionUpdate) UnmarshalJSON(data []byte) error {
	// 用 alias 避免无限递归
	type Alias SessionUpdate
	aux := &struct {
		Content json.RawMessage `json:"content,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(u),
	}

	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	if len(aux.Content) == 0 {
		return nil
	}

	// 根据 JSON 首字符判断是对象还是数组
	switch aux.Content[0] {
	case '{':
		// 单个 TextContent（agent_message_chunk / agent_thought_chunk）
		var tc TextContent
		if err := json.Unmarshal(aux.Content, &tc); err == nil {
			u.Content = &tc
		}
	case '[':
		// ToolResultItem 数组（tool_call_update）
		var items []ToolResultItem
		if err := json.Unmarshal(aux.Content, &items); err == nil {
			u.ToolResults = items
		}
	}

	return nil
}

// MarshalJSON 自定义序列化，将 Content 或 ToolResults 写入 content 字段
func (u SessionUpdate) MarshalJSON() ([]byte, error) {
	type Alias SessionUpdate
	aux := &struct {
		Content any `json:"content,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(&u),
	}

	if u.Content != nil {
		aux.Content = u.Content
	} else if len(u.ToolResults) > 0 {
		aux.Content = u.ToolResults
	}

	return json.Marshal(aux)
}

// UpdateMeta 通知的元信息
type UpdateMeta struct {
	ID        string `json:"id,omitempty"`
	MessageID string `json:"messageId,omitempty"`
	Type      string `json:"type,omitempty"`     // "partial", "builtin"
	LastChunk *bool  `json:"lastChunk,omitempty"`
}

// Location 工具调用涉及的文件路径
type Location struct {
	Path string `json:"path"`
}

// Command 可用的 slash 命令
type Command struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Input       CommandInput `json:"input,omitempty"`
}

// CommandInput 命令输入提示
type CommandInput struct {
	Hint string `json:"hint"`
}

// ToolResultItem 工具调用结果的单个条目
type ToolResultItem struct {
	Type    string       `json:"type"`              // "content"
	Content *TextContent `json:"content,omitempty"`
}

type TextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ToolResultText 返回工具执行结果的合并文本
func (u *SessionUpdate) ToolResultText() string {
	var texts []string
	for _, item := range u.ToolResults {
		if item.Content != nil && item.Content.Text != "" {
			texts = append(texts, item.Content.Text)
		}
	}
	if len(texts) == 0 {
		return ""
	}
	result := texts[0]
	for i := 1; i < len(texts); i++ {
		result += "\n" + texts[i]
	}
	return result
}
