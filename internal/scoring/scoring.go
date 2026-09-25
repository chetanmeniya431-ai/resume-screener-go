// Package scoring turns what the AI pulled out of a resume into a 0–100 score.
// The AI reads the resume; plain Go code does the maths, so the same resume
// always gets the same score and every point can be explained.
package scoring

import (
	"regexp"
	"strings"
	"sync"

	"github.com/chetanmeniya431-ai/resume-screener-go/internal/store"
)

// Weights add up to 100.
const (
	MustHaveWeight   = 50.0
	NiceToHaveWeight = 15.0
	ExperienceWeight = 15.0
	SemanticWeight   = 20.0

	// Cosine similarity between a resume and a job rarely goes below or above
	// these values with nomic-embed-text, so they map to 0 and full points.
	simFloor = 0.35
	simCeil  = 0.80
)

// Other names people use for the same skill.
var aliases = map[string][]string{
	"go":          {"golang"},
	"golang":      {"go"},
	"postgresql":  {"postgres"},
	"postgres":    {"postgresql"},
	"kubernetes":  {"k8s"},
	"javascript":  {"js"},
	"typescript":  {"ts"},
	"rest apis":   {"rest api", "restful"},
	"rest api":    {"rest apis", "restful"},
	"ci/cd":       {"jenkins", "github actions", "gitlab ci"},
	"aws":         {"amazon web services"},
	"mysql":       {"mariadb"},
	"power bi":    {"powerbi"},
	"machine learning": {"ml"},
}

// HasSkill reports whether the skill appears in the extracted skills or anywhere
// in the resume text, as a whole word (so "Go" does not match "Google").
func HasSkill(skill string, extracted []string, text string) bool {
	names := append([]string{strings.ToLower(strings.TrimSpace(skill))}, aliases[strings.ToLower(strings.TrimSpace(skill))]...)
	for _, n := range names {
		if n == "" {
			continue
		}
		for _, e := range extracted {
			if strings.EqualFold(strings.TrimSpace(e), n) {
				return true
			}
		}
		if wordMatch(n, text) {
			return true
		}
	}
	return false
}

// reCache is shared by all pipeline workers, so it must be safe for concurrent use.
var reCache sync.Map // term -> *regexp.Regexp

func wordMatch(term, text string) bool {
	v, ok := reCache.Load(term)
	if !ok {
		// \b does not work next to symbols like "+" or "#", so use explicit edges.
		v, _ = reCache.LoadOrStore(term, regexp.MustCompile(`(?i)(^|[^a-z0-9+#])`+regexp.QuoteMeta(term)+`($|[^a-z0-9+#])`))
	}
	return v.(*regexp.Regexp).MatchString(text)
}

type Input struct {
	Job        store.Job
	Skills     []string
	Years      float64
	Text       string
	Similarity float64 // cosine between resume and job embeddings
}

type Result struct {
	Score       int
	Breakdown   store.ScoreBreakdown
	Matched     []string
	Missing     []string
	NiceMatched []string
}

func Score(in Input) Result {
	var r Result
	for _, s := range in.Job.MustHave {
		if HasSkill(s, in.Skills, in.Text) {
			r.Matched = append(r.Matched, s)
		} else {
			r.Missing = append(r.Missing, s)
		}
	}
	for _, s := range in.Job.NiceToHave {
		if HasSkill(s, in.Skills, in.Text) {
			r.NiceMatched = append(r.NiceMatched, s)
		}
	}

	r.Breakdown.MustHave = ratio(len(r.Matched), len(in.Job.MustHave)) * MustHaveWeight
	r.Breakdown.NiceToHave = ratio(len(r.NiceMatched), len(in.Job.NiceToHave)) * NiceToHaveWeight

	switch {
	case in.Job.MinYears <= 0 || in.Years >= float64(in.Job.MinYears):
		r.Breakdown.Experience = ExperienceWeight
	case in.Years > 0:
		r.Breakdown.Experience = ExperienceWeight * in.Years / float64(in.Job.MinYears)
	}

	sim := (in.Similarity - simFloor) / (simCeil - simFloor)
	r.Breakdown.Semantic = clamp(sim, 0, 1) * SemanticWeight

	total := r.Breakdown.MustHave + r.Breakdown.NiceToHave + r.Breakdown.Experience + r.Breakdown.Semantic
	r.Score = int(clamp(total, 0, 100) + 0.5)
	r.Breakdown.MustHave = round1(r.Breakdown.MustHave)
	r.Breakdown.NiceToHave = round1(r.Breakdown.NiceToHave)
	r.Breakdown.Experience = round1(r.Breakdown.Experience)
	r.Breakdown.Semantic = round1(r.Breakdown.Semantic)
	return r
}

// ratio is found/total; a job with no requirements in a group gives full points.
func ratio(found, total int) float64 {
	if total == 0 {
		return 1
	}
	return float64(found) / float64(total)
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }
