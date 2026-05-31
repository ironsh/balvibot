package parse

import (
	"strings"
	"testing"

	"github.com/ironsh/balvibot/tools/api/internal/cas"
)

const sampleEml = "From: Jane Doe <jane@acme.org>\r\n" +
	"To: Us <us@here.local>\r\n" +
	"Subject: Hello\r\n" +
	"Message-ID: <m1@acme.org>\r\n" +
	"Date: Mon, 01 Jan 2024 10:00:00 +0000\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"BOUNDARY\"\r\n" +
	"\r\n" +
	"--BOUNDARY\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"hello body\r\n" +
	"--BOUNDARY\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	"<p>hello body</p>\r\n" +
	"--BOUNDARY\r\n" +
	"Content-Type: text/plain; name=note.txt\r\n" +
	"Content-Disposition: attachment; filename=note.txt\r\n" +
	"\r\n" +
	"attachment data\r\n" +
	"--BOUNDARY--\r\n"

func TestParseRFC822(t *testing.T) {
	c, err := cas.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParseRFC822([]byte(sampleEml), c)
	if err != nil {
		t.Fatal(err)
	}
	if p.MessageID != "m1@acme.org" {
		t.Errorf("MessageID=%q", p.MessageID)
	}
	if p.From.Email != "jane@acme.org" {
		t.Errorf("From=%+v", p.From)
	}
	if len(p.To) != 1 || p.To[0].Email != "us@here.local" {
		t.Errorf("To=%+v", p.To)
	}
	if p.Subject != "Hello" {
		t.Errorf("Subject=%q", p.Subject)
	}
	if !strings.Contains(p.BodyText, "hello body") {
		t.Errorf("BodyText=%q", p.BodyText)
	}
	if !strings.Contains(p.BodyHTML, "<p>hello body</p>") {
		t.Errorf("BodyHTML=%q", p.BodyHTML)
	}
	if len(p.Attachments) != 1 {
		t.Fatalf("attachments=%d", len(p.Attachments))
	}
	if p.Attachments[0].Filename != "note.txt" {
		t.Errorf("filename=%q", p.Attachments[0].Filename)
	}
	if p.Attachments[0].SizeBytes == 0 {
		t.Errorf("size=0")
	}
}

func TestParseRFC822SyntheticID(t *testing.T) {
	c, err := cas.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	noID := "From: <a@b>\r\nTo: <c@d>\r\nSubject: x\r\n\r\nbody\r\n"
	p, err := ParseRFC822([]byte(noID), c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.MessageID, "synthetic.") {
		t.Errorf("expected synthetic ID, got %q", p.MessageID)
	}
}
