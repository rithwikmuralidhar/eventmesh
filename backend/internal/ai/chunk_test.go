package ai

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChunkMarkdownHeadingsAndLines(t *testing.T) {
	md := "# Title\nintro\n\n## Setup\nstep one\n\n### Deep\ndeep text\n\n## Other\nother text\n"
	chunks := ChunkMarkdown("docs/a.md", md, 1000)
	require.Len(t, chunks, 4)
	require.Equal(t, "Title", chunks[0].HeadingPath)
	require.Equal(t, "Title > Setup", chunks[1].HeadingPath)
	require.Equal(t, "Title > Setup > Deep", chunks[2].HeadingPath)
	require.Equal(t, "Title > Other", chunks[3].HeadingPath)
	require.Equal(t, 4, chunks[1].StartLine)
	require.Equal(t, "4-6", chunks[1].LineRange())
	require.Contains(t, chunks[3].Content, "other text")
}

func TestChunkIgnoresHeadingsInCodeFences(t *testing.T) {
	md := "# A\n```\n# not a heading\n```\ntext\n"
	chunks := ChunkMarkdown("a.md", md, 1000)
	require.Len(t, chunks, 1)
	require.Contains(t, chunks[0].Content, "# not a heading")
}

func TestChunkSplitsLongSectionOnParagraphs(t *testing.T) {
	para := strings.Repeat("word ", 20) // 100 chars
	md := "# Big\n" + para + "\n\n" + para + "\n\n" + para + "\n"
	chunks := ChunkMarkdown("big.md", md, 150)
	require.Greater(t, len(chunks), 1)
	for _, c := range chunks {
		require.LessOrEqual(t, len(c.Content), 2*150)
		require.Equal(t, "Big", c.HeadingPath)
	}
}

func TestChunkHashStableAndContentSensitive(t *testing.T) {
	a := ChunkMarkdown("a.md", "# T\nhello\n", 1000)
	b := ChunkMarkdown("a.md", "# T\nhello\n", 1000)
	c := ChunkMarkdown("a.md", "# T\nhello!\n", 1000)
	require.Equal(t, a[0].Hash, b[0].Hash)
	require.NotEqual(t, a[0].Hash, c[0].Hash)
}
