// Package rag splits resumes into chunks and finds the chunks closest to a question.
// RAG (retrieval-augmented generation) = search the resumes first, then let the AI
// answer using only what was found.
package rag

import (
	"container/heap"
	"math"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/chetanmeniya431-ai/resume-screener-go/internal/store"
)

// Chunk splits text into pieces of up to maxWords words. It never cuts inside a
// line (a resume bullet stays whole) unless one line alone is too long. A new
// chunk repeats the previous line when it is short enough (overlap), so a fact
// on a border is still found. Blank lines (resume sections) always start a new chunk
// once the current one is half full.
func Chunk(text string, maxWords, overlap int) []string {
	if maxWords < 1 {
		maxWords = 1
	}
	if overlap >= maxWords {
		overlap = maxWords / 4
	}
	var out []string
	var cur []string // lines in the current chunk
	words := 0
	flush := func(keepLast bool) {
		if len(cur) == 0 {
			return
		}
		out = append(out, strings.Join(cur, "\n"))
		last := cur[len(cur)-1]
		cur, words = nil, 0
		if n := len(strings.Fields(last)); keepLast && n <= overlap {
			cur, words = []string{last}, n
		}
	}
	for _, para := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		if words >= maxWords/2 {
			flush(false)
		}
		for _, line := range strings.Split(para, "\n") {
			f := strings.Fields(line)
			if len(f) == 0 {
				continue
			}
			// A single very long line is split by words.
			for len(f) > maxWords {
				flush(false)
				out = append(out, strings.Join(f[:maxWords], " "))
				f = f[maxWords:]
			}
			if words+len(f) > maxWords {
				flush(true)
			}
			cur = append(cur, strings.Join(f, " "))
			words += len(f)
		}
	}
	flush(false)
	return out
}

// Cosine similarity: 1 = same meaning, 0 = unrelated.
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// Mean averages vectors; used to get one vector per resume.
func Mean(vs [][]float32) []float32 {
	if len(vs) == 0 {
		return nil
	}
	out := make([]float32, len(vs[0]))
	for _, v := range vs {
		for i := range out {
			if i < len(v) {
				out[i] += v[i]
			}
		}
	}
	for i := range out {
		out[i] /= float32(len(vs))
	}
	return out
}

type Hit struct {
	Chunk store.Chunk
	Score float64
}

// Search returns the k chunks most similar to query. The chunks are split into
// shards and scored in parallel, one goroutine per CPU; each goroutine keeps its
// own top-k in a small heap, and the results are merged at the end.
func Search(chunks []store.Chunk, query []float32, k int) []Hit {
	if k <= 0 || len(chunks) == 0 {
		return nil
	}
	shards := min(runtime.GOMAXPROCS(0), len(chunks))
	size := (len(chunks) + shards - 1) / shards

	results := make([][]Hit, shards)
	var wg sync.WaitGroup
	for s := 0; s < shards; s++ {
		lo, hi := s*size, min((s+1)*size, len(chunks))
		if lo >= hi {
			continue
		}
		wg.Add(1)
		go func(s int, part []store.Chunk) {
			defer wg.Done()
			h := &minHeap{}
			for _, c := range part {
				score := Cosine(c.Embedding, query)
				if h.Len() < k {
					heap.Push(h, Hit{Chunk: c, Score: score})
				} else if score > (*h)[0].Score {
					(*h)[0] = Hit{Chunk: c, Score: score}
					heap.Fix(h, 0)
				}
			}
			results[s] = *h
		}(s, chunks[lo:hi])
	}
	wg.Wait()

	var all []Hit
	for _, r := range results {
		all = append(all, r...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Score > all[j].Score })
	if len(all) > k {
		all = all[:k]
	}
	return all
}

// Diverse keeps at most perCandidate hits for any one person, so one long resume
// cannot fill the whole answer context.
func Diverse(hits []Hit, perCandidate, k int) []Hit {
	seen := map[int64]int{}
	var out []Hit
	for _, h := range hits {
		if seen[h.Chunk.CandidateID] >= perCandidate {
			continue
		}
		seen[h.Chunk.CandidateID]++
		out = append(out, h)
		if len(out) == k {
			break
		}
	}
	return out
}

type minHeap []Hit

func (h minHeap) Len() int           { return len(h) }
func (h minHeap) Less(i, j int) bool { return h[i].Score < h[j].Score }
func (h minHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *minHeap) Push(x any)        { *h = append(*h, x.(Hit)) }
func (h *minHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}
