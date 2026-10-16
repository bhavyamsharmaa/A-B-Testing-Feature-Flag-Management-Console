package mail

import (
	"crypto/x509"
	"fmt"
	"net/http/httptest"
	"testing"
)

func sp(f string, a ...any) string { return fmt.Sprintf(f, a...) }

func x509Pool(t *testing.T, s *httptest.Server) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(s.Certificate())
	return pool
}
