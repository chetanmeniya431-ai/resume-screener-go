package seed

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func TestGenerateIsStableAndComplete(t *testing.T) {
	a, b := Generate(2026), Generate(2026)
	total := 0
	names := map[string]bool{}
	for j := range a {
		total += len(a[j])
		for k := range a[j] {
			if a[j][k].Text != b[j][k].Text {
				t.Fatal("same seed must give the same resumes")
			}
			first := strings.SplitN(a[j][k].Text, "\n", 2)[0]
			if names[first] {
				t.Errorf("duplicate person %q", first)
			}
			names[first] = true
			if !strings.Contains(a[j][k].Text, "@example.com") {
				t.Error("emails must use example.com")
			}
		}
	}
	if total != 40 {
		t.Fatalf("got %d resumes, want 40", total)
	}
}

func TestSampleZip(t *testing.T) {
	b, err := SampleZip()
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 12 {
		t.Fatalf("zip has %d files, want 12", len(zr.File))
	}
}
