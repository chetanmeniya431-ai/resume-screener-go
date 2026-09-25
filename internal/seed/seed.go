// Package seed makes synthetic demo data: 3 jobs and 40 made-up resumes.
// Every person, company, school and email here is invented. No real data is used.
package seed

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"strings"

	"github.com/chetanmeniya431-ai/resume-screener-go/internal/store"
)

const currentYear = 2026

// Jobs are the three demo job openings.
var Jobs = []store.Job{
	{
		Title:       "Senior Laravel Developer",
		Description: "We run a B2B ordering platform used by 900 shops. You will build new features in Laravel, design MySQL tables, write REST APIs for our mobile app, and help junior developers. Remote, full time.",
		MustHave:    []string{"PHP", "Laravel", "MySQL", "REST APIs"},
		NiceToHave:  []string{"Vue.js", "Docker", "Redis", "AWS"},
		MinYears:    5,
	},
	{
		Title:       "Go Backend Engineer",
		Description: "We process 2 million delivery events a day. You will build fast Go services, tune PostgreSQL queries, run everything in Docker, and keep our APIs reliable. Remote, full time.",
		MustHave:    []string{"Go", "PostgreSQL", "Docker", "REST APIs"},
		NiceToHave:  []string{"Kubernetes", "Kafka", "gRPC", "Redis"},
		MinYears:    4,
	},
	{
		Title:       "Data Analyst",
		Description: "Help our sales and operations teams understand their numbers. You will write SQL, clean data in Python, and build clear Power BI dashboards that managers use every week.",
		MustHave:    []string{"SQL", "Python", "Excel", "Power BI"},
		NiceToHave:  []string{"Tableau", "Statistics", "Pandas"},
		MinYears:    2,
	},
}

type archetype struct {
	titles  []string // oldest to newest
	skills  []string
	extras  []string // some people have a few of these
	bullets []string
	degree  string
}

var archetypes = map[string]archetype{
	"laravel-strong": {
		titles:  []string{"PHP Developer", "Laravel Developer", "Senior Laravel Developer"},
		skills:  []string{"PHP", "Laravel", "MySQL", "REST APIs", "Git"},
		extras:  []string{"Vue.js", "Docker", "Redis", "AWS", "Livewire", "PHPUnit", "Tailwind CSS"},
		bullets: []string{"Built REST APIs in Laravel for a mobile ordering app used by 12,000 customers.", "Designed MySQL tables and indexes that cut report load time from 9 seconds to 1 second.", "Moved background emails to Laravel queues with Redis, removing timeouts at checkout.", "Wrote PHPUnit tests and raised coverage from 20% to 70%.", "Led code reviews for a team of 4 developers.", "Built an admin panel with roles and permissions for 300 staff users.", "Upgraded a large app from Laravel 8 to Laravel 11 with no downtime."},
		degree:  "B.Tech in Computer Science",
	},
	"laravel-partial": {
		titles:  []string{"Web Developer", "PHP Developer"},
		skills:  []string{"PHP", "MySQL", "WordPress", "jQuery", "HTML", "CSS"},
		extras:  []string{"CodeIgniter", "Bootstrap", "Laravel"},
		bullets: []string{"Built and maintained 30+ WordPress sites for small businesses.", "Wrote custom PHP plugins for booking and payments.", "Improved page speed scores from 45 to 85 on client sites.", "Worked directly with clients to gather requirements."},
		degree:  "BCA (Bachelor of Computer Applications)",
	},
	"go-strong": {
		titles:  []string{"Software Engineer", "Backend Engineer", "Senior Backend Engineer"},
		skills:  []string{"Go", "PostgreSQL", "Docker", "REST APIs", "Git", "Linux"},
		extras:  []string{"Kubernetes", "Kafka", "gRPC", "Redis", "Prometheus", "AWS", "Terraform"},
		bullets: []string{"Built Go services that handle 3,000 requests per second with p95 latency under 40 ms.", "Designed a worker pool in Go that processes 1 million events per day from Kafka.", "Tuned PostgreSQL queries and cut a key API from 800 ms to 90 ms.", "Moved 14 services to Docker and Kubernetes.", "Added tracing and Prometheus metrics, cutting incident debug time by half.", "Wrote gRPC APIs between internal services.", "Mentored 3 engineers moving from Python to Go."},
		degree:  "B.E. in Information Technology",
	},
	"go-partial": {
		titles:  []string{"Python Developer", "Backend Developer"},
		skills:  []string{"Python", "Django", "PostgreSQL", "REST APIs", "Docker"},
		extras:  []string{"Go", "Celery", "Redis", "AWS"},
		bullets: []string{"Built Django REST APIs for a logistics dashboard.", "Wrote a small Go tool to import CSV files 10 times faster than the old Python script.", "Set up Docker for local development for the whole team.", "Wrote PostgreSQL reports for the finance team."},
		degree:  "B.Sc. in Computer Science",
	},
	"data-strong": {
		titles:  []string{"Junior Analyst", "Data Analyst", "Senior Data Analyst"},
		skills:  []string{"SQL", "Python", "Excel", "Power BI", "Pandas"},
		extras:  []string{"Tableau", "Statistics", "Google Analytics", "DAX", "Looker Studio"},
		bullets: []string{"Built 15 Power BI dashboards used by 60 managers every week.", "Wrote SQL that joins sales, stock and returns data into one daily report.", "Cleaned messy sales data in Python and Pandas, saving 6 hours of manual work each week.", "Found a pricing error that was losing 2% of monthly revenue.", "Ran A/B test analysis for the marketing team using basic statistics.", "Trained 20 staff to read and filter dashboards on their own."},
		degree:  "B.Com with Statistics",
	},
	"data-partial": {
		titles:  []string{"Operations Executive", "MIS Executive"},
		skills:  []string{"Excel", "Google Sheets", "SQL"},
		extras:  []string{"Power BI", "VBA", "Python"},
		bullets: []string{"Prepared daily and monthly MIS reports in Excel for the operations head.", "Built Excel macros that cut report time from 3 hours to 40 minutes.", "Wrote basic SQL queries to pull order data.", "Tracked delivery delays and shared weekly summaries."},
		degree:  "BBA (Bachelor of Business Administration)",
	},
	"off-target": {
		titles:  []string{"Graphic Designer", "UI Designer"},
		skills:  []string{"Figma", "Adobe Photoshop", "Illustrator", "HTML", "CSS"},
		extras:  []string{"JavaScript", "Webflow"},
		bullets: []string{"Designed app screens and design systems in Figma.", "Created brand kits for 25 small businesses.", "Worked with developers to hand over clean designs."},
		degree:  "Diploma in Visual Design",
	},
}

var (
	firstNames = []string{"Aarav", "Meera", "Rohan", "Sara", "Kabir", "Ananya", "Vikram", "Leila", "Daniel", "Priyanka", "Tomasz", "Nadia", "Arjun", "Fatima", "Lucas", "Ishita", "Omar", "Grace", "Rahul", "Sofia", "Hiro", "Neha", "Mateo", "Zara", "Karan", "Elena", "Samuel", "Tara", "Yusuf", "Kavya", "Noah", "Riya", "Ethan", "Amara", "Dev", "Chloe", "Imran", "Asha", "Leo", "Maya", "Nikhil", "Aisha", "Jonas", "Pooja"}
	lastNames  = []string{"Rao", "Kapoor", "Fernandes", "Iyer", "Novak", "Khan", "Mehta", "Silva", "Okafor", "Banerjee", "Schmidt", "Haddad", "Nair", "Costa", "Patel", "Kowalski", "Das", "Moreau", "Sethi", "Tanaka", "Joshi", "Mendes", "Bose", "Reyes", "Malik", "Varga", "Pillai", "Andersen", "Chopra", "Ibrahim"}
	companies  = []string{"Brightlane Logistics", "Northwind Retail", "Bluepeak Software", "Cedar Health Systems", "Orbit Payments", "Greenfield Foods", "Harbor Freight Tech", "Lumen Analytics", "Pinecrest Media", "Quarry Labs", "Redwood Insurance", "Silverline Travel", "Tidewater Energy", "Urban Nest Homes", "Vertex Manufacturing", "Willow Education"}
	cities     = []string{"Pune, India", "Bengaluru, India", "Ahmedabad, India", "Lisbon, Portugal", "Warsaw, Poland", "Nairobi, Kenya", "Manila, Philippines", "Cairo, Egypt", "Hyderabad, India", "Kraków, Poland"}
	schools    = []string{"Westbrook Institute of Technology", "Lakeside University", "Riverdale College of Engineering", "Northgate University", "Eastfield Institute of Science"}
)

// plan says which kinds of people apply to each job.
var plan = [][]struct {
	kind  string
	years [2]int // min, max total years
	count int
}{
	{{"laravel-strong", [2]int{5, 11}, 7}, {"laravel-strong", [2]int{2, 4}, 2}, {"laravel-partial", [2]int{3, 8}, 3}, {"go-partial", [2]int{3, 6}, 1}, {"off-target", [2]int{2, 6}, 1}},
	{{"go-strong", [2]int{4, 10}, 6}, {"go-strong", [2]int{1, 3}, 2}, {"go-partial", [2]int{3, 7}, 3}, {"laravel-strong", [2]int{5, 9}, 1}, {"off-target", [2]int{2, 5}, 1}},
	{{"data-strong", [2]int{2, 7}, 6}, {"data-partial", [2]int{1, 5}, 4}, {"go-partial", [2]int{2, 4}, 1}, {"off-target", [2]int{1, 4}, 2}},
}

type Resume struct {
	FileName string
	Text     string
}

// Generate returns the resumes for each job. The same seed always gives the same people.
func Generate(seed uint64) [][]Resume {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	used := map[string]bool{}
	out := make([][]Resume, len(plan))
	for j, groups := range plan {
		for _, g := range groups {
			for i := 0; i < g.count; i++ {
				out[j] = append(out[j], makeResume(rng, archetypes[g.kind], g.years, used))
			}
		}
		rng.Shuffle(len(out[j]), func(a, b int) { out[j][a], out[j][b] = out[j][b], out[j][a] })
	}
	return out
}

func makeResume(rng *rand.Rand, a archetype, yr [2]int, used map[string]bool) Resume {
	var name string
	for {
		name = pick(rng, firstNames) + " " + pick(rng, lastNames)
		if !used[name] {
			used[name] = true
			break
		}
	}
	years := yr[0] + rng.IntN(yr[1]-yr[0]+1)
	skills := append([]string{}, a.skills...)
	for _, i := range rng.Perm(len(a.extras))[:rng.IntN(len(a.extras)+1)] {
		skills = append(skills, a.extras[i])
	}

	// Split the career into 1–3 jobs, newest first.
	nJobs := min(len(a.titles), 1+years/3)
	cuts := splitYears(rng, years, nJobs)
	end := currentYear
	var exp strings.Builder
	for k := 0; k < nJobs; k++ {
		title := a.titles[len(a.titles)-1-k]
		start := end - cuts[k]
		endText := fmt.Sprint(end)
		if k == 0 {
			endText = "Present"
		}
		fmt.Fprintf(&exp, "%s — %s (%d – %s)\n", title, pick(rng, companies), start, endText)
		for _, b := range pickN(rng, a.bullets, 2+rng.IntN(2)) {
			exp.WriteString("- " + b + "\n")
		}
		exp.WriteString("\n")
		end = start
	}

	email := strings.ToLower(strings.ReplaceAll(name, " ", ".")) + "@example.com"
	gradYear := currentYear - years
	text := fmt.Sprintf(`%s
%s
%s | %s

SUMMARY
%s with %d years of experience. %s

EXPERIENCE
%s
SKILLS
%s

EDUCATION
%s, %s (%d)
`, name, a.titles[len(a.titles)-1], email, pick(rng, cities),
		a.titles[len(a.titles)-1], years, pick(rng, summaries),
		exp.String(), strings.Join(skills, ", "), a.degree, pick(rng, schools), gradYear)

	file := strings.ToLower(strings.ReplaceAll(name, " ", "_")) + ".txt"
	return Resume{FileName: file, Text: text}
}

var summaries = []string{
	"I like clean, simple code and clear communication.",
	"I enjoy turning messy problems into simple tools.",
	"Comfortable working remotely with teams across time zones.",
	"I care about fast, reliable software and good documentation.",
	"I enjoy mentoring and sharing what I learn.",
}

func splitYears(rng *rand.Rand, total, parts int) []int {
	if parts <= 1 || total < parts {
		return []int{max(total, 1)}
	}
	out := make([]int, parts)
	left := total
	for i := 0; i < parts-1; i++ {
		maxHere := left - (parts - 1 - i)
		out[i] = 1 + rng.IntN(max(1, maxHere))
		if out[i] > maxHere {
			out[i] = maxHere
		}
		left -= out[i]
	}
	out[parts-1] = left
	return out
}

func pick(rng *rand.Rand, s []string) string { return s[rng.IntN(len(s))] }

func pickN(rng *rand.Rand, s []string, n int) []string {
	n = min(n, len(s))
	var out []string
	for _, i := range rng.Perm(len(s))[:n] {
		out = append(out, s[i])
	}
	return out
}

// Seeder is the part of the store seeding needs.
type Seeder interface {
	CountJobs(ctx context.Context) (int, error)
	CreateJob(ctx context.Context, j store.Job) (int64, error)
	InsertCandidates(ctx context.Context, jobID int64, list []store.NewCandidate) ([]int64, error)
	SetStatus(ctx context.Context, id int64, status string) error
}

// Run fills an empty database. If autoScreen is false the resumes wait as "cancelled"
// until someone presses "Screen again".
func Run(ctx context.Context, s Seeder, autoScreen bool) (bool, error) {
	n, err := s.CountJobs(ctx)
	if err != nil || n > 0 {
		return false, err
	}
	people := Generate(2026)
	for i, j := range Jobs {
		id, err := s.CreateJob(ctx, j)
		if err != nil {
			return false, err
		}
		list := make([]store.NewCandidate, len(people[i]))
		for k, r := range people[i] {
			list[k] = store.NewCandidate{FileName: r.FileName, RawText: r.Text}
		}
		ids, err := s.InsertCandidates(ctx, id, list)
		if err != nil {
			return false, err
		}
		if !autoScreen {
			for _, cid := range ids {
				if err := s.SetStatus(ctx, cid, store.StatusCancelled); err != nil {
					return false, err
				}
			}
		}
	}
	return true, nil
}

// SampleZip builds a zip of fresh made-up resumes that visitors can download and upload.
func SampleZip() ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i, group := range Generate(7) {
		for k, r := range group {
			if k >= 4 {
				break
			}
			f, err := zw.Create(fmt.Sprintf("%s/%s", folder(i), r.FileName))
			if err != nil {
				return nil, err
			}
			if _, err := f.Write([]byte(r.Text)); err != nil {
				return nil, err
			}
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func folder(i int) string {
	return []string{"laravel-developer", "go-backend-engineer", "data-analyst"}[i]
}
