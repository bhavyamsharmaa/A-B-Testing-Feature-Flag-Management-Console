package health

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// testPool connects to the local test database server (HELIOS_TEST_DATABASE_URL), or skips.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("HELIOS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("HELIOS_TEST_DATABASE_URL not set; skipping the test that needs a real database")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
