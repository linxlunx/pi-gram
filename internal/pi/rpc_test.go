package pi

import (
	"encoding/json"
	"testing"
)

func TestCommandSerialization(t *testing.T) {
	cases := []struct {
		name string
		cmd  Command
		want string
	}{
		{"prompt", Command{ID: "req-1", Type: "prompt", Message: "Check whether Nginx is running."},
			`{"id":"req-1","type":"prompt","message":"Check whether Nginx is running."}`},
		{"bash", Command{ID: "req-2", Type: "bash", Command: "df -h"},
			`{"id":"req-2","type":"bash","command":"df -h"}`},
		{"get_state", Command{ID: "req-3", Type: "get_state"},
			`{"id":"req-3","type":"get_state"}`},
		{"abort", Command{ID: "req-4", Type: "abort"},
			`{"id":"req-4","type":"abort"}`},
		{"no_id", Command{Type: "abort"}, `{"type":"abort"}`},
		{"switch", Command{ID: "req-5", Type: "switch_session", SessionPath: "/tmp/s.jsonl"},
			`{"id":"req-5","type":"switch_session","sessionPath":"/tmp/s.jsonl"}`},
		{"new_session", Command{Type: "new_session", ParentSession: "/tmp/p.jsonl"},
			`{"type":"new_session","parentSession":"/tmp/p.jsonl"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.cmd)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestResponseDeserialization(t *testing.T) {
	raw := []byte(`{"id":"req-1","type":"response","command":"get_state","success":true,"data":{` +
		`"sessionId":"abc","sessionFile":"/var/lib/pi/sessions/abc.jsonl","isStreaming":false,` +
		`"messageCount":5,"pendingMessageCount":0,"model":{"id":"m1","provider":"opencode"}}}`)
	var resp Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ID != "req-1" || resp.Command != "get_state" || !resp.Success {
		t.Fatalf("unexpected response: %+v", resp)
	}
	var st SessionState
	if err := json.Unmarshal(resp.Data, &st); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if st.SessionFile != "/var/lib/pi/sessions/abc.jsonl" || st.SessionID != "abc" || st.IsStreaming {
		t.Fatalf("unexpected state: %+v", st)
	}
	if st.MessageCount != 5 || st.PendingMessageCount != 0 {
		t.Fatalf("unexpected counts: %+v", st)
	}
	if got := st.ModelString(); got != "opencode/m1" {
		t.Fatalf("model = %q, want opencode/m1", got)
	}
}

func TestResponseFailureDeserialization(t *testing.T) {
	raw := []byte(`{"id":"req-9","type":"response","command":"set_model","success":false,"error":"Model not found: invalid/model"}`)
	var resp Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Success || resp.Error != "Model not found: invalid/model" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestBashResultParsing(t *testing.T) {
	raw := []byte(`{"output":"total 48","exitCode":3,"cancelled":false,"truncated":true,"fullOutputPath":"/tmp/pi-bash.log"}`)
	var d BashResultData
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.Output != "total 48" || d.ExitCode != 3 || !d.Truncated || d.FullOutputPath != "/tmp/pi-bash.log" {
		t.Fatalf("unexpected bash data: %+v", d)
	}
}

func TestPromptDispositionParsing(t *testing.T) {
	for _, disp := range []string{"started", "queued", "handled"} {
		raw, _ := json.Marshal(map[string]string{"disposition": disp})
		var d PromptDispositionData
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if d.Disposition != disp {
			t.Fatalf("disposition = %q, want %q", d.Disposition, disp)
		}
	}
}

func TestEventDeserialization(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		check func(t *testing.T, ev Event)
	}{
		{"settled", `{"type":"agent_settled","aborted":true}`, func(t *testing.T, ev Event) {
			if ev.Type != "agent_settled" || !ev.Aborted {
				t.Fatalf("unexpected event: %+v", ev)
			}
		}},
		{"bash_update", `{"type":"bash_execution_update","id":"req-1","delta":"total 48\n"}`, func(t *testing.T, ev Event) {
			if ev.ID != "req-1" || ev.Delta != "total 48\n" {
				t.Fatalf("unexpected event: %+v", ev)
			}
		}},
		{"tool_start", `{"type":"tool_execution_start","toolCallId":"c1","toolName":"bash","args":{"command":"ls"}}`, func(t *testing.T, ev Event) {
			if ev.ToolCallID != "c1" || ev.ToolName != "bash" {
				t.Fatalf("unexpected event: %+v", ev)
			}
		}},
		{"tool_end", `{"type":"tool_execution_end","toolCallId":"c1","toolName":"bash","result":{"content":[{"type":"text","text":"hi"}]},"isError":true}`, func(t *testing.T, ev Event) {
			if !ev.IsError {
				t.Fatalf("expected isError")
			}
			if got := ToolResultText(ev.Result); got != "hi" {
				t.Fatalf("tool result text = %q, want hi", got)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ev Event
			if err := json.Unmarshal([]byte(tc.raw), &ev); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			tc.check(t, ev)
		})
	}
}

func TestAssistantMessageText(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "blocks",
			raw:  `{"role":"assistant","content":[{"type":"text","text":"Line one"},{"type":"toolCall","id":"c1"},{"type":"text","text":"Line two"}]}`,
			want: "Line one\n\nLine two",
		},
		{name: "plain string", raw: `{"role":"assistant","content":"hello"}`, want: "hello"},
		// messageText filters by role; Text() itself is role-agnostic.
		{name: "user filtered", raw: `{"role":"user","content":"hi"}`, want: "hi"},
		{name: "no text blocks", raw: `{"role":"assistant","content":[{"type":"thinking","thinking":"hmm"}]}`, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var msg AgentMessage
			if err := json.Unmarshal([]byte(tc.raw), &msg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := msg.Text(); got != tc.want {
				t.Fatalf("text = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMessageEndEventExtractsAssistantText(t *testing.T) {
	raw := []byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Final answer"}],"stopReason":"stop"}}`)
	var ev Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var msg AgentMessage
	if err := json.Unmarshal(ev.Message, &msg); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}
	if msg.Role != "assistant" {
		t.Fatalf("role = %q", msg.Role)
	}
	if got := msg.Text(); got != "Final answer" {
		t.Fatalf("text = %q", got)
	}
}

func TestMessageTextFiltersRole(t *testing.T) {
	if got := messageText([]byte(`{"role":"user","content":"hi"}`)); got != "" {
		t.Fatalf("user message text = %q, want empty", got)
	}
	if got := messageText([]byte(`{"role":"assistant","content":"hi"}`)); got != "hi" {
		t.Fatalf("assistant message text = %q, want hi", got)
	}
}
