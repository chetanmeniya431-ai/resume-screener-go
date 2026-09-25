// Package textract pulls plain text out of uploaded resume files.
package textract

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

var ErrUnsupported = errors.New("only PDF, TXT and MD files are supported")

// Allowed reports whether a file name has a supported extension.
func Allowed(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf", ".txt", ".md":
		return true
	}
	return false
}

// Extract returns clean text for a file. PDFs must contain real text; scanned
// images have no text layer and are rejected with a clear message.
func Extract(name string, data []byte) (text string, err error) {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".md":
		if !utf8.Valid(data) {
			return "", fmt.Errorf("%s is not a UTF-8 text file", name)
		}
		text = string(data)
	case ".pdf":
		text, err = fromPDF(data)
		if err != nil {
			return "", fmt.Errorf("could not read %s: %w", name, err)
		}
	default:
		return "", ErrUnsupported
	}
	text = Clean(text)
	if len(strings.Fields(text)) < 30 {
		return "", fmt.Errorf("%s has too little text (is it a scanned image?)", name)
	}
	return text, nil
}

func fromPDF(data []byte) (text string, err error) {
	// The PDF library can panic on broken files; turn that into an error.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("broken PDF: %v", r)
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	for i := 1; i <= r.NumPage(); i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		rows, err := p.GetTextByRow()
		if err != nil {
			return "", err
		}
		for _, row := range rows {
			for j, w := range row.Content {
				if j > 0 {
					buf.WriteByte(' ')
				}
				buf.WriteString(w.S)
			}
			buf.WriteByte('\n')
		}
		buf.WriteString("\n")
	}
	return buf.String(), nil
}

var (
	reSpaces    = regexp.MustCompile(`[ \t\f\v]+`)
	reBlankRuns = regexp.MustCompile(`\n{3,}`)
)

// Clean normalises spaces and blank lines and drops control characters.
func Clean(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= 32 {
			return r
		}
		return -1
	}, s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(reSpaces.ReplaceAllString(l, " "))
	}
	s = strings.Join(lines, "\n")
	s = reBlankRuns.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
