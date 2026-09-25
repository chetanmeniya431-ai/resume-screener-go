package textract

import (
	"strings"
	"testing"
)

func TestExtractText(t *testing.T) {
	body := strings.Repeat("Experienced Go developer building APIs. ", 10)
	got, err := Extract("cv.TXT", []byte("  Jane   Doe\r\n\r\n\r\n\r\n"+body))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "Jane Doe\n\n") {
		t.Errorf("not cleaned: %q", got[:20])
	}
}

func TestExtractRejects(t *testing.T) {
	if _, err := Extract("cv.docx", []byte("x")); err == nil {
		t.Error("docx should be rejected")
	}
	if _, err := Extract("cv.txt", []byte("too short")); err == nil {
		t.Error("tiny files should be rejected")
	}
	if _, err := Extract("cv.pdf", []byte("not a pdf at all")); err == nil {
		t.Error("broken PDF should be rejected")
	}
	if _, err := Extract("cv.txt", []byte{0xff, 0xfe, 0x00}); err == nil {
		t.Error("non UTF-8 should be rejected")
	}
}
