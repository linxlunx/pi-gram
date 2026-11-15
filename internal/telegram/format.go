// Package telegram implements the Telegram webhook integration:
// request validation, update routing, and message delivery.
package telegram

import (
	"strings"
)

// maxMessageLen is Telegram's message length limit.
const maxMessageLen = 4096

// splitPlainText splits text into chunks of at most limit runes,
// preferring line breaks and spaces as split points.
func splitPlainText(text string, limit int) []string {
	if limit <= 0 {
		limit = maxMessageLen
	}
	runes := []rune(text)
	if len(runes) <= limit {
		if len(runes) == 0 {
			return []string{""}
		}
		return []string{text}
	}
	var out []string
	start := 0
	for start < len(runes) {
		end := start + limit
		if end >= len(runes) {
			out = append(out, string(runes[start:]))
			break
		}
		window := runes[start:end]
		cut := -1
		for i := len(window) - 1; i > limit/4; i-- {
			if window[i] == '\n' {
				cut = i + 1
				break
			}
		}
		if cut < 0 {
			for i := len(window) - 1; i > limit/4; i-- {
				if window[i] == ' ' {
					cut = i + 1
					break
				}
			}
		}
		if cut < 0 {
			cut = limit
		}
		out = append(out, string(runes[start:start+cut]))
		start += cut
	}
	return out
}

// mdSegment is a run of text inside or outside a code fence.
type mdSegment struct {
	code bool
	text string
}

// splitFences splits text into plain and code-fenced segments. Code
// segments include their ``` markers so fences survive chunking.
func splitFences(text string) []mdSegment {
	var segs []mdSegment
	rest := text
	for {
		i := strings.Index(rest, "```")
		if i < 0 {
			break
		}
		if i > 0 {
			segs = append(segs, mdSegment{code: false, text: rest[:i]})
		}
		closeIdx := strings.Index(rest[i+3:], "```")
		if closeIdx < 0 {
			// Unterminated fence: treat the remainder as code.
			segs = append(segs, mdSegment{code: true, text: rest[i:]})
			return segs
		}
		closePos := i + 3 + closeIdx
		segs = append(segs, mdSegment{code: true, text: rest[i : closePos+3]})
		rest = rest[closePos+3:]
	}
	if rest != "" {
		segs = append(segs, mdSegment{code: false, text: rest})
	}
	return segs
}

// escapeMarkdownV2Text escapes all characters that must be escaped in
// MarkdownV2 plain text.
func escapeMarkdownV2Text(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case '\\', '_', '*', '[', ']', '(', ')', '~', '`', '>', '#', '+', '-', '=', '|', '{', '}', '.', '!':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// escapeMarkdownV2Code escapes characters that must be escaped inside
// MarkdownV2 code blocks.
func escapeMarkdownV2Code(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, "`", "\\`")
}

// SplitAndEscapeMarkdownV2 renders text as MarkdownV2-safe chunks of at
// most limit runes. Code fences are preserved across chunk boundaries:
// every chunk that ends inside a fence is closed and the next chunk
// reopens it.
func SplitAndEscapeMarkdownV2(text string, limit int) []string {
	if limit < 16 {
		// Degenerate limit; return everything escaped as one chunk.
		return []string{escapeMarkdownV2Text(text)}
	}
	c := &mdChunker{limit: limit}
	for _, seg := range splitFences(text) {
		c.addSegment(seg)
	}
	c.flush()
	if len(c.chunks) == 0 {
		c.chunks = append(c.chunks, "")
	}
	return c.chunks
}

// mdChunker accumulates escaped MarkdownV2 text into chunks of at most
// limit runes, tracking code-fence state across chunk boundaries.
// Invariant: curLen plus 3 (when inside a fence, for its closing
// marker) never exceeds limit.
type mdChunker struct {
	limit  int
	chunks []string
	cur    strings.Builder
	curLen int
	inCode bool
}

func (c *mdChunker) flush() {
	s := c.cur.String()
	if c.inCode {
		s += "```"
		c.inCode = false
	}
	if s != "" {
		c.chunks = append(c.chunks, s)
	}
	c.cur.Reset()
	c.curLen = 0
}

// setCode opens or closes a code fence in the current chunk.
func (c *mdChunker) setCode(on bool) {
	if on == c.inCode {
		return
	}
	c.cur.WriteString("```")
	c.curLen += 3
	c.inCode = on
}

func (c *mdChunker) escapeSegment(seg mdSegment) string {
	if seg.code {
		return escapeMarkdownV2Code(seg.text)
	}
	return escapeMarkdownV2Text(seg.text)
}

// addSegment appends a whole segment when it fits, otherwise it flushes
// the current chunk and hard-splits the segment.
func (c *mdChunker) addSegment(seg mdSegment) {
	escaped := c.escapeSegment(seg)
	erunes := len([]rune(escaped))
	need := erunes
	if seg.code != c.inCode {
		need += 3 // fence transition written immediately
	}
	if seg.code {
		need += 3 // reserve for the closing fence
	}
	if c.curLen+need <= c.limit {
		c.setCode(seg.code)
		c.cur.WriteString(escaped)
		c.curLen += erunes
		return
	}
	if c.curLen > 0 {
		c.flush()
	}
	c.hardSplit(seg)
}

// hardSplit appends an oversized segment piece by piece, reopening
// code fences at chunk boundaries. Escaping can expand text, so the
// piece size is shrunk until the escaped form fits.
func (c *mdChunker) hardSplit(seg mdSegment) {
	runes := []rune(seg.text)
	i := 0
	for i < len(runes) {
		if seg.code != c.inCode {
			// Need room for the fence plus at least one rune plus the
			// closing reserve.
			if c.limit-c.curLen < 3+1+3 {
				c.flush()
				continue
			}
			c.setCode(seg.code)
		}
		avail := c.limit - c.curLen
		if seg.code {
			avail -= 3 // reserve for the closing fence
		}
		if avail <= 0 {
			c.flush()
			continue
		}
		n := avail
		if n > len(runes)-i {
			n = len(runes) - i
		}
		escaped := c.escapeSegment(mdSegment{code: seg.code, text: string(runes[i : i+n])})
		for er := len([]rune(escaped)); er > avail && n > 0; er = len([]rune(escaped)) {
			shrink := n * avail / er
			if shrink >= n {
				shrink = n - 1
			}
			if shrink < 1 {
				shrink = 1
			}
			n = shrink
			escaped = c.escapeSegment(mdSegment{code: seg.code, text: string(runes[i : i+n])})
			if n == 1 && len([]rune(escaped)) > avail {
				// A single rune still does not fit (absurdly small
				// limit); flush and retry with a fresh chunk.
				c.flush()
				break
			}
		}
		if escaped == "" {
			continue // flushed mid-shrink; retry with empty chunk
		}
		c.cur.WriteString(escaped)
		c.curLen += len([]rune(escaped))
		i += n
	}
}
