package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// EvalQuestion is one retrieval test. Source is nil when the answer is not in
// the docs (the correct outcome is then "not found").
type EvalQuestion struct {
	Question string  `json:"question"`
	Source   *string `json:"source"`
	Heading  *string `json:"heading"`
}

// EvalReport holds the numbers one run produced.
type EvalReport struct {
	Answerable       int
	Unanswerable     int
	RecallAt5        float64 // expected source among the top 5
	HeadingRecallAt5 float64 // expected source and heading among the top 5
	NotFoundCorrect  float64 // unanswerable questions below the similarity threshold
	MeanRetrievalMS  float64
	P95RetrievalMS   float64
	Misses           []string
	FalseAnswerables []string // unanswerable questions that cleared the threshold
	Threshold        float64
	AnswerableTop    []float64 // best similarity per answerable question
	UnanswerableTop  []float64 // best similarity per not-in-the-docs question
}

// LoadEvalQuestions reads the JSON question file.
func LoadEvalQuestions(path string) ([]EvalQuestion, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var qs []EvalQuestion
	if err := json.Unmarshal(raw, &qs); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return qs, nil
}

// EvaluateRetrieval runs retrieval only (embedding + vector search); it does
// not call the chat model. "Correct not found" means the best match for an
// unanswerable question is below MinSimilarity, which is exactly the rule
// AskDocs uses to skip the chat model.
func (s *Service) EvaluateRetrieval(ctx context.Context, qs []EvalQuestion) (EvalReport, error) {
	r := EvalReport{Threshold: s.minSimilarity()}
	var latencies []float64
	var srcHit, headHit, nfOK int
	for _, q := range qs {
		hits, took, err := s.retrieve(ctx, uuid.Nil, q.Question, true)
		if err != nil {
			return r, fmt.Errorf("question %q: %w", q.Question, err)
		}
		latencies = append(latencies, float64(took.Microseconds())/1000)
		top := 0.0
		if len(hits) > 0 {
			top = hits[0].Score
		}
		if q.Source == nil {
			r.UnanswerableTop = append(r.UnanswerableTop, top)
			r.Unanswerable++
			if len(hits) == 0 || hits[0].Score < s.minSimilarity() {
				nfOK++
			} else {
				r.FalseAnswerables = append(r.FalseAnswerables, q.Question)
			}
			continue
		}
		r.Answerable++
		r.AnswerableTop = append(r.AnswerableTop, top)
		src, head := false, false
		for _, h := range hits {
			if h.SourcePath == *q.Source {
				src = true
				if q.Heading != nil && strings.HasSuffix(h.HeadingPath, *q.Heading) {
					head = true
				}
			}
		}
		if src {
			srcHit++
		} else {
			r.Misses = append(r.Misses, q.Question)
		}
		if head {
			headHit++
		}
	}
	if r.Answerable > 0 {
		r.RecallAt5 = float64(srcHit) / float64(r.Answerable)
		r.HeadingRecallAt5 = float64(headHit) / float64(r.Answerable)
	}
	if r.Unanswerable > 0 {
		r.NotFoundCorrect = float64(nfOK) / float64(r.Unanswerable)
	}
	if n := len(latencies); n > 0 {
		sort.Float64s(latencies)
		var sum float64
		for _, l := range latencies {
			sum += l
		}
		r.MeanRetrievalMS = sum / float64(n)
		r.P95RetrievalMS = latencies[min(n-1, int(float64(n)*0.95))]
	}
	return r, nil
}

// Markdown renders the report as docs/ai-evaluation.md.
func (r EvalReport) Markdown(date time.Time, chatModel, embedModel string, dims int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# AI documentation retrieval evaluation\n\nRun on %s.\n\n", date.Format("2006-01-02"))
	fmt.Fprintf(&b, "- Embedding model: `%s` (%d dimensions)\n- Chat model configured: `%s` (not called by this evaluation)\n", embedModel, dims, chatModel)
	fmt.Fprintf(&b, "- Similarity threshold for \"not found\": %.2f (cosine, `EM_AI_MIN_SIMILARITY`)\n", r.Threshold)
	fmt.Fprintf(&b, "- Questions: %d answerable, %d not in the docs\n\n", r.Answerable, r.Unanswerable)
	b.WriteString("| Metric | Value |\n| --- | --- |\n")
	fmt.Fprintf(&b, "| recall@5 (expected source file among top 5) | %.0f%% |\n", r.RecallAt5*100)
	fmt.Fprintf(&b, "| recall@5 (expected source and heading) | %.0f%% |\n", r.HeadingRecallAt5*100)
	fmt.Fprintf(&b, "| correct \"not found\" rate | %.0f%% |\n", r.NotFoundCorrect*100)
	fmt.Fprintf(&b, "| mean retrieval latency (embed question + vector search) | %.0f ms |\n", r.MeanRetrievalMS)
	fmt.Fprintf(&b, "| p95 retrieval latency | %.0f ms |\n\n", r.P95RetrievalMS)
	b.WriteString(scoreSummary("best similarity, answerable questions", r.AnswerableTop))
	b.WriteString(scoreSummary("best similarity, questions not in the docs", r.UnanswerableTop))
	b.WriteString("\n")
	if len(r.Misses) > 0 {
		b.WriteString("Questions whose expected source was not in the top 5:\n\n")
		for _, m := range r.Misses {
			fmt.Fprintf(&b, "- %s\n", m)
		}
		b.WriteString("\n")
	}
	if len(r.FalseAnswerables) > 0 {
		b.WriteString("Questions not in the docs that still cleared the threshold:\n\n")
		for _, m := range r.FalseAnswerables {
			fmt.Fprintf(&b, "- %s\n", m)
		}
		b.WriteString("\n")
	}
	b.WriteString("What this shows: retrieval quality on this fixed question set over this repository's own docs, one run. " +
		"It does not measure answer quality (the chat model is not called), does not generalize to other documents, and a " +
		"single run with ~20 questions cannot support fine comparisons.\n")
	return b.String()
}

// scoreSummary renders min / median / max of the best-match similarity scores.
func scoreSummary(label string, scores []float64) string {
	if len(scores) == 0 {
		return ""
	}
	s := append([]float64(nil), scores...)
	sort.Float64s(s)
	return fmt.Sprintf("- %s: min %.3f, median %.3f, max %.3f (n=%d)\n", label, s[0], s[len(s)/2], s[len(s)-1], len(s))
}
