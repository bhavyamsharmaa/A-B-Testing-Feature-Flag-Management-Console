package mail

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http/httptest"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var hostile = []string{
	"Acme\r\nBcc: attacker@evil.example",
	"Acme\nSubject: replaced",
	"Acme\r\n\r\nInjected body",
	"Acme Bcc: x@evil.example",
	"Acme\x00Bcc: x@evil.example",
	"Acme\u0085Bcc: x@evil.example",
	"=?utf-8?B?QmNjOiBhQGV2aWwuZXhhbXBsZQ==?=",
	strings.Repeat("é", 500),
}

func TestCleanHeaderRemovesEveryLineBreak(t *testing.T) {
	for _, h := range hostile {
		got := CleanHeader(h, 200)
		if strings.ContainsAny(got, "\r\n\x00  \u0085") {
			t.Errorf("%q -> %q still has a line break", h, got)
		}
		if len([]rune(got)) > 200 {
			t.Errorf("%q -> %d runes", h, len([]rune(got)))
		}
	}
	if got := CleanHeader("  a \t b\r\nc  ", 50); got != "a b c" {
		t.Errorf("%q", got)
	}
}

func TestHostileSubjectsNeverAddAHeader(t *testing.T) {
	for _, h := range hostile {
		raw, err := Build("noreply@helios.example", Message{To: "bob@example.com", Subject: "Join " + h, Text: "hi", HTML: "<p>hi</p>"}, time.Now(), "helios.example")
		if err != nil {
			t.Fatalf("%q: %v", h, err)
		}
		m, err := mail.ReadMessage(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("%q: %v", h, err)
		}
		for k := range m.Header {
			switch k {
			case "From", "To", "Subject", "Date", "Message-Id", "Mime-Version", "Content-Type", "Auto-Submitted":
			default:
				t.Errorf("%q: an unexpected header %q appeared", h, k)
			}
		}
		if len(m.Header["Subject"]) != 1 || len(m.Header["To"]) != 1 || len(m.Header["Bcc"]) != 0 {
			t.Errorf("%q: headers %v", h, m.Header)
		}
		// and the raw header block has no line that is not a header or a folded continuation
		head, _, _ := strings.Cut(string(raw), "\r\n\r\n")
		for _, line := range strings.Split(head, "\r\n") {
			if !strings.Contains(line, ": ") && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
				t.Errorf("%q: stray header line %q", h, line)
			}
			if strings.HasPrefix(strings.ToLower(line), "bcc:") {
				t.Errorf("%q: injected %q", h, line)
			}
		}
		dec := new(mime.WordDecoder)
		if subj, err := dec.DecodeHeader(m.Header.Get("Subject")); err != nil || strings.ContainsAny(subj, "\r\n") {
			t.Errorf("%q: decoded subject %q (%v)", h, subj, err)
		}
	}
}

func TestBodiesAreQuotedPrintableMultipartAndRoundTrip(t *testing.T) {
	body := "Line one\r\n.Line starting with a dot\r\n--boundary-lookalike\r\nCafé ☕ and a very long line " + strings.Repeat("x", 300)
	raw, err := Build("noreply@helios.example", Message{To: "bob@example.com", Subject: "S", Text: body, HTML: "<p>" + body + "</p>"}, time.Now(), "helios.example")
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	mt, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/alternative" {
		t.Fatalf("%q %v", mt, err)
	}
	mr := multipart.NewReader(m.Body, params["boundary"])
	var texts []string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		// (multipart.Reader decodes quoted-printable itself and drops the header)
		b, _ := io.ReadAll(p)
		texts = append(texts, string(b))
	}
	if len(texts) != 2 || !strings.Contains(texts[0], "Café ☕") || !strings.Contains(texts[0], "--boundary-lookalike") {
		t.Errorf("parts %q", texts)
	}
	for _, line := range strings.Split(string(raw), "\r\n") {
		if len(line) > 998 {
			t.Errorf("a line of %d bytes", len(line))
		}
	}
}

func TestAddressesAreStrict(t *testing.T) {
	for _, bad := range []string{"", "a", "a@b", "a@b.c\r\nBcc: x@y.z", "a@b.com, c@d.com", "Name <a@b.com>", "a@b.com\n", "\"a b\"@c.com", "a@b.com;c@d.com", "a b@c.com", "<a@b.com>"} {
		if _, err := ParseAddress(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
		if _, err := Build("noreply@helios.example", Message{To: bad, Subject: "s", Text: "t"}, time.Now(), "h"); err == nil {
			t.Errorf("Build accepted the recipient %q", bad)
		}
	}
	if _, err := Build("bad from", Message{To: "a@b.com", Subject: "s", Text: "t"}, time.Now(), "h"); err == nil {
		t.Error("a bad From was accepted")
	}
	if got, err := ParseAddress("first.last+tag@sub.example.com"); err != nil || got != "first.last+tag@sub.example.com" {
		t.Errorf("%q %v", got, err)
	}
}

func TestRedactShowsLittle(t *testing.T) {
	if got := Redact("alice@example.com"); got != "a***@example.com" {
		t.Error(got)
	}
	if Redact("nonsense") != "***" || Redact("@x.com") != "***" {
		t.Error("bad input not redacted")
	}
}

// ---- the sink: the default, sends nothing --------------------------------------------------

func TestTheSinkSendsNothingAndLogsOnlyARedactedLine(t *testing.T) {
	var lines []string
	s := &Sink{Logf: func(f string, a ...any) {
		lines = append(lines, strings.TrimSpace(strings.ReplaceAll(sprintf(f, a...), "\n", " ")))
	}}
	if s.Mode() != "sink" {
		t.Fatal(s.Mode())
	}
	err := s.Send(context.Background(), Message{To: "alice@example.com", Subject: "Join Acme Secret Workspace", Text: "https://c.example/invite/hinv_SECRETTOKEN", HTML: "<a>x</a>"})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Fatalf("%v", lines)
	}
	for _, leak := range []string{"alice@example.com", "Acme", "SECRETTOKEN", "invite/"} {
		if strings.Contains(lines[0], leak) {
			t.Errorf("the sink's log line contains %q: %s", leak, lines[0])
		}
	}
	if !strings.Contains(lines[0], "a***@example.com") {
		t.Errorf("no redacted recipient: %s", lines[0])
	}
}

func TestTheSinkWritesFilesOnlyWhenAskedAndPrivately(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mail")
	s := &Sink{Dir: dir, Logf: func(string, ...any) {}}
	if err := s.Send(context.Background(), Message{To: "a@example.com", Subject: "s", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Fatalf("%d files", len(files))
	}
	info, _ := files[0].Info()
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
	// no directory configured: no files
	none := &Sink{Logf: func(string, ...any) {}}
	if err := none.Send(context.Background(), Message{To: "a@example.com", Subject: "s", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
}

func sprintf(f string, a ...any) string {
	return strings.TrimSpace(strings.Join(strings.Fields(sp(f, a...)), " "))
}

// ---- SMTP: TLS is required unless a development switch says otherwise ---------------------------

func TestSMTPRefusesPlaintextUnlessTheDevelopmentFlagIsOn(t *testing.T) {
	base := SMTPConfig{Host: "smtp.example.com", Port: 587, From: "noreply@example.com"}
	none := base
	none.TLS = "none"
	if _, err := NewSMTP(none); err == nil || !strings.Contains(err.Error(), "MAIL_ALLOW_INSECURE_SMTP") {
		t.Errorf("TLS none without the flag: %v", err)
	}
	none.AllowInsecure = true
	if _, err := NewSMTP(none); err != nil {
		t.Errorf("TLS none with the flag: %v", err)
	}
	if s, err := NewSMTP(base); err != nil || s.cfg.TLS != "starttls" {
		t.Errorf("the default is not STARTTLS: %v", err)
	}
	for _, bad := range []SMTPConfig{{Port: 587, From: "a@b.com"}, {Host: "h", From: "a@b.com"}, {Host: "h", Port: 587, From: "not an address"}, {Host: "h", Port: 587, From: "a@b.com", TLS: "ssl3"}} {
		if _, err := NewSMTP(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

// fakeSMTP speaks just enough SMTP, and records every command it is sent.
type fakeSMTP struct {
	ln       net.Listener
	mu       sync.Mutex
	commands []string
	data     string
	offerTLS bool
	tlsCfg   *tls.Config // non-nil: implicit TLS
}

func newFakeSMTP(t *testing.T, offerStartTLS bool, tlsCfg *tls.Config) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, offerTLS: offerStartTLS, tlsCfg: tlsCfg}
	if tlsCfg != nil {
		f.ln = tls.NewListener(ln, tlsCfg)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := f.ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) record(s string) {
	f.mu.Lock()
	f.commands = append(f.commands, s)
	f.mu.Unlock()
}

func (f *fakeSMTP) saw(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.commands {
		if strings.HasPrefix(strings.ToUpper(c), prefix) {
			return true
		}
	}
	return false
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	w := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
	w("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		f.record(line)
		switch cmd := strings.ToUpper(line); {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			if f.offerTLS {
				w("250-fake\r\n250 STARTTLS")
			} else {
				w("250 fake")
			}
		case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
			w("250 ok")
		case cmd == "DATA":
			w("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.data = b.String()
			f.mu.Unlock()
			w("250 queued")
		case cmd == "QUIT":
			w("221 bye")
			return
		default:
			w("502 no")
		}
	}
}

func TestSMTPNeverDowngradesStartTLS(t *testing.T) {
	srv := newFakeSMTP(t, false, nil) // a server that does not offer STARTTLS
	s, err := NewSMTP(SMTPConfig{Host: "127.0.0.1", Port: srv.port(), From: "noreply@example.com", TLS: "starttls", Username: "u", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Send(context.Background(), Message{To: "bob@example.com", Subject: "s", Text: "secret invite link"})
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("send to a server without STARTTLS: %v", err)
	}
	for _, cmd := range []string{"MAIL", "RCPT", "DATA", "AUTH"} {
		if srv.saw(cmd) {
			t.Errorf("the client sent %s over a connection that is not encrypted", cmd)
		}
	}
	if strings.Contains(err.Error(), "p") && strings.Contains(err.Error(), "password") {
		t.Errorf("the error may mention the password: %v", err)
	}
}

func TestSMTPOverImplicitTLSSendsTheMessage(t *testing.T) {
	cert := httptest.NewUnstartedServer(nil)
	cert.StartTLS()
	cfg := cert.TLS.Clone()
	roots := x509Pool(t, cert)
	cert.Close()
	srv := newFakeSMTP(t, false, cfg)
	s, err := NewSMTP(SMTPConfig{Host: "127.0.0.1", Port: srv.port(), From: "noreply@example.com", TLS: "implicit", TLSConfig: &tls.Config{RootCAs: roots, ServerName: "example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	// the certificate httptest issues is valid for 127.0.0.1 and example.com
	s.cfg.Host = "127.0.0.1"
	if err := s.Send(context.Background(), Message{To: "bob@example.com", Subject: "Hello", Text: "body text"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if !strings.Contains(srv.data, "Subject: Hello") || !strings.Contains(srv.data, "To: bob@example.com") {
		t.Errorf("data %q", srv.data)
	}
}

func TestSMTPInsecureWorksOnlyWhenExplicitlyAllowed(t *testing.T) {
	srv := newFakeSMTP(t, false, nil)
	s, err := NewSMTP(SMTPConfig{Host: "127.0.0.1", Port: srv.port(), From: "noreply@example.com", TLS: "none", AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), Message{To: "bob@example.com", Subject: "s", Text: "t"}); err != nil {
		t.Fatal(err)
	}
	if !srv.saw("DATA") {
		t.Error("no message arrived")
	}
}

func TestBodiesAreDeclaredQuotedPrintableOnTheWire(t *testing.T) {
	raw, _ := Build("noreply@helios.example", Message{To: "bob@example.com", Subject: "S", Text: "Café"}, time.Now(), "h")
	if !bytes.Contains(raw, []byte("Content-Transfer-Encoding: quoted-printable")) || bytes.Contains(raw, []byte("Café")) {
		t.Errorf("not encoded: %q", raw)
	}
}
