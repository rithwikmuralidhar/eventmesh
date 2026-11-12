package ai

import (
	"context"
	"hash/fnv"
	"strings"
	"sync"
)

// Fake is a deterministic Provider for tests; it never touches the network.
// Embeddings are bag-of-words hashes, so texts sharing words are close.
type Fake struct {
	mu           sync.Mutex
	GenerateFunc func(GenerateRequest) (GenerateResponse, error)
	Calls        int
	Prompts      []string // every prompt received, for "was X sent" assertions
}

// Generate implements Provider.
func (f *Fake) Generate(_ context.Context, req GenerateRequest) (GenerateResponse, error) {
	f.mu.Lock()
	f.Calls++
	f.Prompts = append(f.Prompts, req.Prompt)
	fn := f.GenerateFunc
	f.mu.Unlock()
	if fn == nil {
		return GenerateResponse{Text: "fake answer", Model: "fake"}, nil
	}
	return fn(req)
}

// FakeDims is the embedding size of Fake.
const FakeDims = 64

// Embed implements Provider.
func (f *Fake) Embed(_ context.Context, texts []string, _ EmbedTask) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = fakeEmbed(t)
	}
	return out, nil
}

func fakeEmbed(text string) []float32 {
	v := make([]float32, FakeDims)
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		if len(w) < 3 {
			continue
		}
		h := fnv.New32a()
		_, _ = h.Write([]byte(w))
		v[h.Sum32()%FakeDims]++
	}
	return normalize(v)
}
