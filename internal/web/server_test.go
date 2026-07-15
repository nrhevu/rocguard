package web

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"rocguard/internal/config"
	"rocguard/internal/model"
	"rocguard/internal/protocol"
)

type cacheTestNodeClient struct {
	mu        sync.Mutex
	snapshots int
}

func (c *cacheTestNodeClient) Health(context.Context, ServerRecord, string) error {
	return nil
}

func (c *cacheTestNodeClient) Snapshot(context.Context, ServerRecord) (model.NodeSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshots++
	return model.NodeSnapshot{
		Now:      time.Unix(int64(c.snapshots), 0).UTC(),
		Hostname: "node-a",
	}, nil
}

func (c *cacheTestNodeClient) CreateReservation(context.Context, ServerRecord, protocol.RegisterArgs) (model.RegisterResult, error) {
	return model.RegisterResult{}, nil
}

func (c *cacheTestNodeClient) CreateClaimKey(context.Context, ServerRecord, protocol.RegisterArgs) (model.RegisterResult, error) {
	return model.RegisterResult{}, nil
}

func (c *cacheTestNodeClient) ShowKeys(context.Context, ServerRecord, string) (model.KeyStatus, error) {
	return model.KeyStatus{}, nil
}

func (c *cacheTestNodeClient) Allow(context.Context, ServerRecord, protocol.AllowArgs) (model.AllowResult, error) {
	return model.AllowResult{}, nil
}

func (c *cacheTestNodeClient) Revoke(context.Context, ServerRecord, protocol.RevokeArgs) (map[string]string, error) {
	return nil, nil
}

func (c *cacheTestNodeClient) snapshotCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshots
}

func TestFleetSnapshotUsesOneSecondCache(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	client := &cacheTestNodeClient{}
	server := New(config.Config{WebRegistry: filepath.Join(t.TempDir(), "servers.json")})
	server.Client = client
	server.now = func() time.Time { return now }

	if _, err := server.Registry.Upsert(ServerRecord{
		Name:     "node-a",
		Endpoint: "http://node-a:8443",
		RootKey:  "rk_test",
	}); err != nil {
		t.Fatal(err)
	}

	first, err := server.cachedFleetSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := server.cachedFleetSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := client.snapshotCount(); got != 1 {
		t.Fatalf("snapshot calls = %d, want 1", got)
	}
	if !first.Servers[0].Snapshot.Now.Equal(second.Servers[0].Snapshot.Now) {
		t.Fatalf("second snapshot was not served from cache")
	}

	now = now.Add(time.Second)
	third, err := server.cachedFleetSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := client.snapshotCount(); got != 2 {
		t.Fatalf("snapshot calls after cache expiry = %d, want 2", got)
	}
	if !third.Servers[0].Snapshot.Now.After(second.Servers[0].Snapshot.Now) {
		t.Fatalf("third snapshot did not refresh after cache expiry")
	}
}
