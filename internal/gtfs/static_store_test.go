package gtfs

import (
	"testing"
	"time"

	"watchdog.onebusaway.org/internal/models"
)

func TestStaticStore(t *testing.T) {
	store := NewStaticStore()
	if store == nil {
		t.Fatal("expected non-nil store")
	}

	serverKey := "test-server"
	data := &models.StaticData{}

	store.Set(serverKey, data)
	if d, ok := store.Get(serverKey); !ok || d != data {
		t.Fatalf("expected to get data back")
	}

	store.SetFetchTime(serverKey, time.Unix(1000, 0))
	if ft, ok := store.GetFetchTime(serverKey); !ok || ft != time.Unix(1000, 0) {
		t.Fatalf("expected to get fetch time back")
	}

	count := 0
	store.Range(func(key string, d *models.StaticData) bool {
		count++
		return false // stop early
	})
	if count != 1 {
		t.Fatalf("expected to iterate once")
	}

	// test prune keep
	store.Set(serverKey, data)
	removed := store.Prune(func(key string) bool {
		return true // keep it
	})
	if len(removed) != 0 {
		t.Fatalf("expected to keep the server")
	}

	// test prune delete
	removed = store.Prune(func(key string) bool {
		return false
	})
	if len(removed) != 1 || removed[0] != serverKey {
		t.Fatalf("expected to prune 1 server, got %v", removed)
	}
	if _, ok := store.Get(serverKey); ok {
		t.Fatalf("expected server to be pruned")
	}
}
