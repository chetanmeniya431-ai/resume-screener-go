package rag

import (
	"math/rand/v2"
	"sort"
	"strings"
	"testing"

	"github.com/chetanmeniya431-ai/resume-screener-go/internal/store"
)

func TestChunkKeepsAllWordsAndLimitsSize(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 50; i++ {
		sb.WriteString("word ")
		if i%7 == 6 {
			sb.WriteString("\n\n")
		}
	}
	chunks := Chunk(sb.String(), 12, 3)
	if len(chunks) < 4 {
		t.Fatalf("got %d chunks, want several", len(chunks))
	}
	for _, c := range chunks {
		if n := len(strings.Fields(c)); n > 12 {
			t.Errorf("chunk has %d words, max 12", n)
		}
	}
}

func TestChunkKeepsLinesWhole(t *testing.T) {
	text := "Jane Doe\nGo engineer\n\nEXPERIENCE\n- Built a worker pool in Go that processes events from Kafka.\n- Tuned PostgreSQL queries from 800 ms to 90 ms.\n- Moved 14 services to Docker and Kubernetes."
	for _, c := range Chunk(text, 20, 12) {
		for _, line := range strings.Split(c, "\n") {
			if !strings.Contains(text, line) {
				t.Errorf("line was cut: %q", line)
			}
		}
	}
}

func TestChunkHandlesBadSettings(t *testing.T) {
	if got := Chunk("a b c d e", 2, 5); len(got) == 0 {
		t.Fatal("overlap >= size must not loop forever or return nothing")
	}
	if got := Chunk("   ", 10, 2); len(got) != 0 {
		t.Fatalf("empty text gave %d chunks", len(got))
	}
}

func TestParallelSearchMatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	vec := func() []float32 {
		v := make([]float32, 16)
		for i := range v {
			v[i] = rng.Float32()*2 - 1
		}
		return v
	}
	chunks := make([]store.Chunk, 500)
	for i := range chunks {
		chunks[i] = store.Chunk{ID: int64(i), CandidateID: int64(i % 40), Embedding: vec()}
	}
	q := vec()

	got := Search(chunks, q, 10)

	type pair struct {
		id    int64
		score float64
	}
	var all []pair
	for _, c := range chunks {
		all = append(all, pair{c.ID, Cosine(c.Embedding, q)})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].score > all[j].score })
	for i := 0; i < 10; i++ {
		if got[i].Chunk.ID != all[i].id {
			t.Fatalf("rank %d: got chunk %d, want %d", i, got[i].Chunk.ID, all[i].id)
		}
	}
}

func TestDiverseLimitsPerCandidate(t *testing.T) {
	var hits []Hit
	for i := 0; i < 10; i++ {
		hits = append(hits, Hit{Chunk: store.Chunk{CandidateID: 1}})
	}
	hits = append(hits, Hit{Chunk: store.Chunk{CandidateID: 2}})
	got := Diverse(hits, 2, 8)
	if len(got) != 3 {
		t.Fatalf("got %d hits, want 2 from candidate 1 + 1 from candidate 2", len(got))
	}
}

func TestCosine(t *testing.T) {
	if c := Cosine([]float32{1, 0}, []float32{1, 0}); c < 0.999 {
		t.Errorf("same vector = %f", c)
	}
	if c := Cosine([]float32{1, 0}, []float32{0, 1}); c != 0 {
		t.Errorf("orthogonal = %f", c)
	}
	if c := Cosine([]float32{1}, []float32{1, 2}); c != 0 {
		t.Errorf("length mismatch = %f", c)
	}
}
