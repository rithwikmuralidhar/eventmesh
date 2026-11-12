package ai

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Chunk is one indexable piece of documentation.
type Chunk struct {
	SourcePath  string // slash-separated, relative to the repo root
	HeadingPath string // "Title > Section > Subsection"
	Content     string
	StartLine   int // 1-based, inclusive
	EndLine     int
	Hash        string // sha256 of source, heading path and content
}

// LineRange renders the chunk's lines for citations ("12-40").
func (c Chunk) LineRange() string { return fmt.Sprintf("%d-%d", c.StartLine, c.EndLine) }

// ChunkMarkdown splits Markdown by heading. Each chunk keeps its heading path
// and line range; a section longer than maxChars is split on paragraph
// boundaries (a single paragraph far longer than maxChars is cut on a line
// boundary). Fenced code blocks are never mistaken for headings.
func ChunkMarkdown(path, text string, maxChars int) []Chunk {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var (
		chunks   []Chunk
		stack    []string
		secStart int
		inFence  bool
		cur      []string
		curHead  string
	)
	flush := func() {
		if strings.TrimSpace(strings.Join(cur, "\n")) != "" {
			chunks = append(chunks, splitSection(path, curHead, cur, secStart, maxChars)...)
		}
		cur = nil
	}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
		} else if !inFence {
			if level, title := parseHeading(line); level > 0 {
				flush()
				for len(stack) >= level {
					stack = stack[:len(stack)-1]
				}
				for len(stack) < level-1 {
					stack = append(stack, "")
				}
				stack = append(stack, title)
				curHead = joinHeadings(stack)
				secStart = i
				cur = []string{line}
				continue
			}
		}
		cur = append(cur, line)
	}
	flush()
	return chunks
}

func parseHeading(line string) (int, string) {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || len(line) <= n || line[n] != ' ' {
		return 0, ""
	}
	return n, strings.TrimSpace(line[n:])
}

func joinHeadings(stack []string) string {
	parts := make([]string, 0, len(stack))
	for _, s := range stack {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " > ")
}

// splitSection turns one heading's lines into chunks of roughly maxChars,
// breaking at blank lines once the budget is reached.
func splitSection(path, head string, lines []string, startIdx, maxChars int) []Chunk {
	var out []Chunk
	var buf []string
	bufStart, size := startIdx, 0
	emit := func(endIdx int) {
		if body := strings.TrimSpace(strings.Join(buf, "\n")); body != "" {
			out = append(out, newChunk(path, head, body, bufStart+1, endIdx+1))
		}
		buf, size = nil, 0
	}
	for i, line := range lines {
		abs := startIdx + i
		blank := strings.TrimSpace(line) == ""
		if size >= maxChars && (blank || size >= 2*maxChars) {
			emit(abs - 1)
			bufStart = abs
		}
		buf = append(buf, line)
		size += len(line) + 1
	}
	emit(startIdx + len(lines) - 1)
	return out
}

func newChunk(path, head, body string, start, end int) Chunk {
	sum := sha256.Sum256([]byte(path + "\x00" + head + "\x00" + body))
	return Chunk{SourcePath: path, HeadingPath: head, Content: body, StartLine: start, EndLine: end, Hash: hex.EncodeToString(sum[:])}
}
