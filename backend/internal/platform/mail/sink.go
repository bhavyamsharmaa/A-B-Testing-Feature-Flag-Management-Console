package mail

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"path/filepath"
	"time"
)

// Sink is the development sender: it sends nothing. It logs one line with the
// recipient redacted and the subject's length (never the subject, which can name
// a workspace, and never the body, which holds an invite link), and, if Dir is
// set, writes the whole message to a file there so a person can open it. Those
// files DO contain the invite link: Dir is for a local machine, mode 0600.
type Sink struct {
	Dir string
	// Logf defaults to log.Printf.
	Logf func(format string, args ...any)
}

func (s *Sink) Mode() string { return "sink" }

func (s *Sink) Send(_ context.Context, msg Message) error {
	raw, err := Build("sink@helios.invalid", msg, time.Now(), "helios.invalid")
	if err != nil {
		return err
	}
	logf := s.Logf
	if logf == nil {
		logf = log.Printf
	}
	if s.Dir != "" {
		if err := os.MkdirAll(s.Dir, 0o700); err != nil {
			return err
		}
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		name := time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b) + ".eml"
		if err := os.WriteFile(filepath.Join(s.Dir, name), raw, 0o600); err != nil {
			return err
		}
	}
	logf("mail sink: not sent: to %s, subject %d characters, body %d bytes", Redact(msg.To), len([]rune(msg.Subject)), len(msg.Text)+len(msg.HTML))
	return nil
}
