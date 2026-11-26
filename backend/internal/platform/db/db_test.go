package db

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Whatever the driver says about a bad connection string, the error that
// reaches the logs must not contain the password or the string itself.
func TestConnectErrorsNeverContainTheConnectionString(t *testing.T) {
	for _, u := range []string{
		"postgres://app:s3cr3t-pw@127.0.0.1:notaport/db",              // unparsable
		"postgres://app:s3cr3t-pw@127.0.0.1:1/db?sslmode=disable",     // refused
		"postgres://app:p%40ss%2Fw0rd@127.0.0.1:1/db?sslmode=disable", // encoded characters
		"postgres://app:s3cr3t-pw@127.0.0.1:1/db?sslmode=bogus",       // bad option
		"postgres://app:s3cr3t-pw@%zz/db",                             // not even a URL
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, err := Connect(ctx, u)
		cancel()
		if err == nil {
			t.Fatalf("%s: connected?", u)
		}
		for _, secret := range []string{"s3cr3t-pw", "p%40ss%2Fw0rd", "p@ss/w0rd", u} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error for %q contains %q: %v", u, secret, err)
			}
		}
	}
}

func TestRedactKeepsTheWrappedError(t *testing.T) {
	base := errors.New("boom")
	err := redact(base, "postgres://a:b@h/d")
	if !errors.Is(err, base) {
		t.Error("the original error is no longer reachable with errors.Is")
	}
}
