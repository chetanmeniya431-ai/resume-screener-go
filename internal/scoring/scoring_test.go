package scoring

import (
	"testing"

	"github.com/chetanmeniya431-ai/resume-screener-go/internal/store"
)

func TestHasSkill(t *testing.T) {
	cases := []struct {
		skill, text string
		want        bool
	}{
		{"Go", "Built services in Golang.", true},
		{"Go", "Worked at Google on search.", false},
		{"PostgreSQL", "Tuned Postgres queries.", true},
		{"C++", "Wrote C++ drivers.", true},
		{"C", "Wrote C++ drivers.", false},
		{"REST APIs", "Designed RESTful services.", true},
		{"Power BI", "Built PowerBI dashboards.", true},
	}
	for _, c := range cases {
		if got := HasSkill(c.skill, nil, c.text); got != c.want {
			t.Errorf("HasSkill(%q, %q) = %v, want %v", c.skill, c.text, got, c.want)
		}
	}
	if !HasSkill("docker", []string{"Docker"}, "") {
		t.Error("should match extracted skills case-insensitively")
	}
}

func TestScore(t *testing.T) {
	job := store.Job{MustHave: []string{"Go", "Docker"}, NiceToHave: []string{"Kafka", "gRPC"}, MinYears: 4}

	perfect := Score(Input{Job: job, Skills: []string{"Go", "Docker", "Kafka", "gRPC"}, Years: 6, Similarity: 0.9})
	if perfect.Score != 100 {
		t.Errorf("perfect match = %d, want 100", perfect.Score)
	}

	half := Score(Input{Job: job, Skills: []string{"Go"}, Years: 2, Similarity: 0.35})
	// must 25 + nice 0 + experience 7.5 + semantic 0 = 32.5 -> 33
	if half.Score != 33 {
		t.Errorf("partial match = %d, want 33 (%+v)", half.Score, half.Breakdown)
	}
	if len(half.Missing) != 1 || half.Missing[0] != "Docker" {
		t.Errorf("missing = %v, want [Docker]", half.Missing)
	}

	noReq := Score(Input{Job: store.Job{}, Similarity: 0.8})
	if noReq.Score != 100 {
		t.Errorf("job with no requirements = %d, want 100", noReq.Score)
	}
}
