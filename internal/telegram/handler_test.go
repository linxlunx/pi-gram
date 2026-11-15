package telegram

import (
	"testing"
)

func TestParseCommand(t *testing.T) {
	cases := []struct {
		text string
		cmd  string
		args string
	}{
		{"/ask check nginx", "ask", "check nginx"},
		{"/help", "help", ""},
		{"/status@mybot", "status", ""},
		{"/ask@mybot  hello  ", "ask", "hello"},
		{"/bash df -h", "bash", "df -h"},
		{"plain text message", "", "plain text message"},
		{"/", "", ""}, // bare "/" degrades to a plain prompt
	}
	for _, tc := range cases {
		cmd, args := parseCommand(tc.text)
		if cmd != tc.cmd || args != tc.args {
			t.Errorf("parseCommand(%q) = (%q, %q), want (%q, %q)", tc.text, cmd, args, tc.cmd, tc.args)
		}
	}
}

func TestParseCommandCaseInsensitive(t *testing.T) {
	cmd, _ := parseCommand("/STATUS")
	if cmd != "status" {
		t.Fatalf("cmd = %q, want status", cmd)
	}
}
