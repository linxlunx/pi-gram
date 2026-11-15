package pi

import "encoding/json"

// Command is a record sent to Pi on stdin. Field names follow the
// official Pi RPC protocol (see pi.dev/docs/latest/rpc-commands).
// Only the commands used by this service are modelled.
type Command struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
	// Bash command fields.
	Command            string `json:"command,omitempty"`
	ExcludeFromContext bool   `json:"excludeFromContext,omitempty"`
	// new_session field.
	ParentSession string `json:"parentSession,omitempty"`
	// switch_session field.
	SessionPath string `json:"sessionPath,omitempty"`
}

// RPC command type constants.
const (
	CmdPrompt        = "prompt"
	CmdAbort         = "abort"
	CmdAbortBash     = "abort_bash"
	CmdBash          = "bash"
	CmdGetState      = "get_state"
	CmdNewSession    = "new_session"
	CmdSwitchSession = "switch_session"
	CmdLastAssistant = "get_last_assistant_text"
)

// Response is a record Pi sends on stdout to acknowledge a command
// identified by its ID. A successful response means the command was
// accepted/queued/handled — not that the work has finished.
type Response struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Command string          `json:"command,omitempty"`
	Success bool            `json:"success"`
	Error   string          `json:"error,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// PromptDispositionData is the data payload of a prompt response.
// Disposition is "started" when a run started, "queued" when the
// prompt waits in a queue, and "handled" when an extension consumed
// it (no run started, so agent_settled will not fire for it).
type PromptDispositionData struct {
	Disposition string `json:"disposition"`
}

// BashResultData is the data payload of a bash command response.
type BashResultData struct {
	Output         string `json:"output"`
	ExitCode       int    `json:"exitCode"`
	Cancelled      bool   `json:"cancelled"`
	Truncated      bool   `json:"truncated"`
	FullOutputPath string `json:"fullOutputPath,omitempty"`
}

// NewSessionData is the data payload of a new_session response.
type NewSessionData struct {
	Cancelled bool `json:"cancelled"`
}

// LastAssistantData is the data payload of get_last_assistant_text.
type LastAssistantData struct {
	Text string `json:"text"`
}

// SessionState is the data payload of a get_state response.
type SessionState struct {
	SessionID           string          `json:"sessionId,omitempty"`
	SessionFile         string          `json:"sessionFile,omitempty"`
	SessionName         string          `json:"sessionName,omitempty"`
	Model               json.RawMessage `json:"model,omitempty"`
	ThinkingLevel       string          `json:"thinkingLevel,omitempty"`
	IsStreaming         bool            `json:"isStreaming"`
	IsCompacting        bool            `json:"isCompacting"`
	AutoCompaction      bool            `json:"autoCompactionEnabled"`
	MessageCount        int             `json:"messageCount"`
	PendingMessageCount int             `json:"pendingMessageCount"`
}

// ModelString renders the model field as "provider/id" when possible.
func (s *SessionState) ModelString() string {
	if len(s.Model) == 0 {
		return ""
	}
	var m struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(s.Model, &m); err != nil {
		return ""
	}
	if m.ID == "" {
		return ""
	}
	if m.Provider != "" {
		return m.Provider + "/" + m.ID
	}
	return m.ID
}
