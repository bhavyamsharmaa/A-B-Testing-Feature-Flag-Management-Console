package db

import (
	"context"
	"os"
	"testing"
)

func TestDefaultPoolSizeFollowsTheCPUsBetweenFourAndTen(t *testing.T) {
	for cpus, want := range map[int]int32{1: 4, 2: 4, 4: 4, 6: 6, 10: 10, 16: 10, 64: 10} {
		if got := DefaultMaxConns(cpus); got != want {
			t.Errorf("%d CPUs: %d, want %d", cpus, got, want)
		}
	}
}

func TestTheConfiguredPoolSizeIsTheOneTheDriverUses(t *testing.T) {
	url := os.Getenv("HELIOS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("HELIOS_TEST_DATABASE_URL not set")
	}
	pool, err := ConnectWith(context.Background(), url, Options{MaxConns: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if got := pool.Config().MaxConns; got != 3 {
		t.Errorf("MaxConns = %d, want 3", got)
	}
	def, err := Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer def.Close()
	if def.Config().MaxConns == 3 {
		t.Error("Connect without options picked up another pool's size")
	}
}
