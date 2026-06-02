// Package llm provides a provider-neutral interface for LLM interactions
// across Anthropic, OpenAI (Responses API), OpenRouter, MiniMax, and Xiaomi
// MiMo.
package llm

// ProviderID identifies one of the supported upstream providers.
type ProviderID string

const (
	ProviderAnthropic  ProviderID = "anthropic"
	ProviderOpenAI     ProviderID = "openai"
	ProviderOpenRouter ProviderID = "openrouter"
	ProviderMiniMax    ProviderID = "minimax"
	ProviderMiMo       ProviderID = "mimo"
	ProviderCopilot    ProviderID = "github-copilot"
)

// CredentialName returns the name used for credential resolution and
// keyring lookups for this provider.
func (p ProviderID) CredentialName() string {
	return string(p)
}

// UsesOAuth reports whether this provider's credential comes from an
// interactive OAuth login (see internal/auth) rather than an API key.
// Credential resolution allows OAuth tokens for these providers.
func (p ProviderID) UsesOAuth() bool {
	return p == ProviderAnthropic || p == ProviderCopilot
}

// Role identifies the author of a message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// StopReason is the normalized reason the model stopped producing output.
// Adapters map provider-specific values into this enum.
type StopReason string

const (
	StopEndTurn       StopReason = "end_turn"
	StopToolUse       StopReason = "tool_use"
	StopMaxTokens     StopReason = "max_tokens"
	StopStopSequence  StopReason = "stop_sequence"
	StopContentFilter StopReason = "content_filter"
	StopError         StopReason = "error"
	StopOther         StopReason = "other"
)

// ContentBlockType discriminates the union shape of ContentBlock.
type ContentBlockType string

const (
	BlockText       ContentBlockType = "text"
	BlockThinking   ContentBlockType = "thinking"
	BlockToolUse    ContentBlockType = "tool_use"
	BlockToolResult ContentBlockType = "tool_result"
	BlockImage      ContentBlockType = "image"
)

// CacheControl marks a block as eligible for the provider's prompt cache.
// Currently only Anthropic honors these; OpenAI/MiniMax do passive caching
// and adapters drop the marker. OpenRouter forwards it when routing to
// Anthropic-family models.
type CacheControl struct {
	Type string // currently always "ephemeral"
}

// ContentBlock is one element of message content. The fields used depend
// on Type — see the const docs for each variant.
type ContentBlock struct {
	Type         ContentBlockType
	Text         string         // BlockText, BlockThinking
	ID           string         // BlockToolUse
	Name         string         // BlockToolUse
	Input        map[string]any // BlockToolUse — already-parsed; never a raw JSON string
	ToolUseID    string         // BlockToolResult
	Output       string         // BlockToolResult
	IsError      bool           // BlockToolResult
	MediaType    string         // BlockImage (e.g. "image/png")
	Data         string         // BlockImage (base64-encoded payload)
	Signature    string         // BlockThinking — Anthropic signature or OpenAI reasoning-item ID
	CacheControl *CacheControl
}

// SystemBlock is one block of the system prompt.
type SystemBlock struct {
	Text         string
	CacheControl *CacheControl
}

// MessageParam is one turn in the conversation history.
type MessageParam struct {
	Role    Role
	Content []ContentBlock
}

// ToolParam describes one tool exposed to the model.
type ToolParam struct {
	Name        string
	Description string
	InputSchema map[string]any // raw JSON Schema object
}

// ToolCall is one tool invocation extracted from the model's response.
// Duplicates the BlockToolUse entries in Message.Content for convenience.
type ToolCall struct {
	ID    string
	Name  string
	Input map[string]any
}

// Usage holds token counts and provider extras from one LLM response.
type Usage struct {
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	CacheReadTokens     int64
	ReasoningTokens     int64          // openai o-series, gpt-5-thinking
	CostUSD             float64        // openrouter when usage.include=true
	ProviderExtra       map[string]any // raw provider blob for future fields
}

// Message is the provider-neutral result of one LLM turn.
type Message struct {
	StopReason  StopReason
	TextContent string         // concatenated text blocks (convenience for extractTextFromMessage)
	Content     []ContentBlock // full ordered content for replay
	ToolCalls   []ToolCall     // convenience extraction; duplicates Content's tool_use blocks
	Usage       Usage
	Raw         any // raw provider response, retained for LogLLMCall debugging
}

// ToParam reconstructs an assistant MessageParam from this Message so the
// turn can be appended to a conversation history and re-sent on the next
// turn. Preserves all content (text, thinking with signature, tool_use) so
// providers that need full round-trip (Anthropic, OpenAI Responses) keep
// working across turns.
func (m *Message) ToParam() MessageParam {
	return MessageParam{
		Role:    RoleAssistant,
		Content: m.Content,
	}
}

// NewUserMessage builds a MessageParam with role=user from the given blocks.
func NewUserMessage(blocks ...ContentBlock) MessageParam {
	return MessageParam{Role: RoleUser, Content: blocks}
}

// NewAssistantMessage builds a MessageParam with role=assistant from the given blocks.
func NewAssistantMessage(blocks ...ContentBlock) MessageParam {
	return MessageParam{Role: RoleAssistant, Content: blocks}
}

// NewTextBlock builds a text content block.
func NewTextBlock(text string) ContentBlock {
	return ContentBlock{Type: BlockText, Text: text}
}

// NewImageBlock builds an image content block. data is the base64-encoded payload.
func NewImageBlock(mediaType, data string) ContentBlock {
	return ContentBlock{Type: BlockImage, MediaType: mediaType, Data: data}
}

// NewToolUseBlock builds an assistant tool_use block.
func NewToolUseBlock(id, name string, input map[string]any) ContentBlock {
	return ContentBlock{Type: BlockToolUse, ID: id, Name: name, Input: input}
}

// NewToolResultBlock builds a user tool_result block.
func NewToolResultBlock(toolUseID, output string, isError bool) ContentBlock {
	return ContentBlock{Type: BlockToolResult, ToolUseID: toolUseID, Output: output, IsError: isError}
}

// NewThinkingBlock builds an assistant thinking block. signature is the
// Anthropic block signature (or the OpenAI reasoning-item ID) used to
// re-feed the block on the next turn.
func NewThinkingBlock(text, signature string) ContentBlock {
	return ContentBlock{Type: BlockThinking, Text: text, Signature: signature}
}
