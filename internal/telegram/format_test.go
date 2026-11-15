package telegram

import (
	"strings"
	"testing"
)

func TestEscapeMarkdownV2Text(t *testing.T) {
	got := escapeMarkdownV2Text("a_b*c[d](e)~f`g>h#i+j-k=l|m{n}o.p!q\\r")
	want := `a\_b\*c\[d\]\(e\)\~f\` + "`" + `g\>h\#i\+j\-k\=l\|m\{n\}o\.p\!q\\r`
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestEscapeMarkdownV2Code(t *testing.T) {
	got := escapeMarkdownV2Code("a`b\\c")
	want := "a\\`b\\\\c"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSplitPlainText(t *testing.T) {
	text := strings.Repeat("word ", 3000) // 15000 runes
	chunks := splitPlainText(text, 1000)
	if len(chunks) < 10 {
		t.Fatalf("expected >=10 chunks, got %d", len(chunks))
	}
	for _, c := range chunks {
		if len([]rune(c)) > 1000 {
			t.Fatalf("chunk too long: %d", len([]rune(c)))
		}
	}
	var rebuilt strings.Builder
	for _, c := range chunks {
		rebuilt.WriteString(c)
	}
	if rebuilt.String() != text {
		t.Fatal("split lost content")
	}
}

func TestSplitPlainTextShort(t *testing.T) {
	if got := splitPlainText("hi", 4096); len(got) != 1 || got[0] != "hi" {
		t.Fatalf("got %v", got)
	}
	if got := splitPlainText("", 4096); len(got) != 1 {
		t.Fatalf("empty text should yield one chunk, got %v", got)
	}
}

func TestSplitPlainTextPrefersNewline(t *testing.T) {
	text := strings.Repeat("a", 999) + "\n" + strings.Repeat("b", 100)
	chunks := splitPlainText(text, 1000)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if strings.HasSuffix(chunks[0], "a") {
		t.Fatal("expected split at the newline")
	}
}

func TestSplitAndEscapeMarkdownV2(t *testing.T) {
	text := "Before\n```go\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n```\nAfter _end_."
	chunks := SplitAndEscapeMarkdownV2(text, 4096)
	if len(chunks) != 1 {
		t.Fatalf("expected single chunk, got %d: %q", len(chunks), chunks)
	}
	got := chunks[0]
	if strings.Count(got, "```") != 2 {
		t.Fatalf("fence markers lost: %q", got)
	}
	if !strings.Contains(got, `\_end\_\.`) {
		t.Fatalf("plain text not escaped: %q", got)
	}
	// Inside a fence only backslash and backtick are escaped.
	if !strings.Contains(got, `fmt.Println("hi")`) {
		t.Fatalf("code content not preserved: %q", got)
	}
}

func TestSplitAndEscapeMarkdownV2MultipleChunks(t *testing.T) {
	code := "x" // 1 rune per line
	var lines []string
	for i := 0; i < 200; i++ {
		lines = append(lines, code)
	}
	text := "```\n" + strings.Join(lines, "\n") + "\n```"
	chunks := SplitAndEscapeMarkdownV2(text, 100)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	fences := 0
	for _, c := range chunks {
		if len([]rune(c)) > 100 {
			t.Fatalf("chunk exceeds limit: %d", len([]rune(c)))
		}
		if strings.Count(c, "```") != 2 {
			t.Fatalf("chunk does not have balanced fences: %q", c)
		}
		fences++
	}
	_ = fences
}

func TestSplitAndEscapeMarkdownV2OddFence(t *testing.T) {
	// Unterminated fence: code segment includes the marker, so chunk
	// keeps a single fence that is closed at flush.
	text := "text\n```go\nunclosed code"
	chunks := SplitAndEscapeMarkdownV2(text, 4096)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if strings.Count(chunks[0], "```") != 2 {
		t.Fatalf("unterminated fence not closed: %q", chunks[0])
	}
}

func TestSplitAndEscapePreservesOrder(t *testing.T) {
	text := "one\n---\ntwo"
	chunks := SplitAndEscapeMarkdownV2(text, 4096)
	joined := strings.Join(chunks, "")
	if !strings.Contains(joined, "one") || !strings.Contains(joined, "two") {
		t.Fatalf("content lost: %q", joined)
	}
}
