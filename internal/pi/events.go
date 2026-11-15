package pi

import (
	"encoding/json"
	"strings"
)

// Event is a session event streamed by Pi on stdout. Only the fields
// this service consumes are decoded; the rest is ignored.
type Event struct {
	Type string `json:"type"`
	// bash_execution_update events repeat the originating command ID.
	ID    string `json:"id,omitempty"`
	Delta string `json:"delta,omitempty"`

	// message_start / message_end carry the full message.
	Message json.RawMessage `json:"message,omitempty"`

	// tool_execution_* events.
	ToolCallID string          `json:"toolCallId,omitempty"`
	ToolName   string          `json:"toolName,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	IsError    bool            `json:"isError,omitempty"`

	// agent_settled.
	Aborted bool `json:"aborted,omitempty"`
}

// AgentMessage is a message carried by message_start/message_end.
type AgentMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// Text extracts the text content of the message. Content can be a
// plain string or an array of content blocks; only text blocks are
// returned.
func (m *AgentMessage) Text() string {
	if m == nil || len(m.Content) == 0 {
		return ""
	}
	var plain string
	if err := json.Unmarshal(m.Content, &plain); err == nil {
		return plain
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// ToolResultText extracts the text of a tool result, if present.
func ToolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var res struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return ""
	}
	msg := &AgentMessage{Content: res.Content}
	return msg.Text()
}

// Event type constants.
const (
	EventAgentStart    = "agent_start"
	EventAgentEnd      = "agent_end"
	EventAgentSettled  = "agent_settled"
	EventTurnStart     = "turn_start"
	EventTurnEnd       = "turn_end"
	EventMessageStart  = "message_start"
	EventMessageUpdate = "message_update"
	EventMessageEnd    = "message_end"
	EventToolStart     = "tool_execution_start"
	EventToolUpdate    = "tool_execution_update"
	EventToolEnd       = "tool_execution_end"
	EventBashUpdate    = "bash_execution_update"
	EventExtensionErr  = "extension_error"
)
