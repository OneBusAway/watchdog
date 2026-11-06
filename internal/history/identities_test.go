package history

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/models"
)

var ctx = context.Background()

func mustEnsureServer(t *testing.T, pool *pgxpool.Pool, server models.ObaServer) int64 {
	t.Helper()
	var id int64
	inTx(t, pool, func(q *dbgen.Queries) error {
		var err error
		id, err = ensureServer(ctx, q, server)
		return err
	})
	return id
}

func TestEnsureServer_Idempotent(t *testing.T) {
	pool := newMigratedPool(t)

	first := mustEnsureServer(t, pool, testServer)
	second := mustEnsureServer(t, pool, testServer)
	if first != second {
		t.Fatalf("second call returned %d, want %d", second, first)
	}

	renamed := testServer
	renamed.ServerName = "Renamed Transit"
	if got := mustEnsureServer(t, pool, renamed); got != first {
		t.Fatalf("rename returned %d, want %d", got, first)
	}

	var name string
	if err := pool.QueryRow(ctx, "SELECT display_name FROM onebusaway_server WHERE server_id = $1", first).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Renamed Transit" {
		t.Fatalf("display_name = %q, want %q", name, "Renamed Transit")
	}
	if n := count(t, pool, "onebusaway_server"); n != 1 {
		t.Fatalf("onebusaway_server has %d rows, want 1", n)
	}
}

// An unchanged identity is read, not rewritten: the row's xmin (the ID of
// the transaction that last wrote it) stays the same.
func TestEnsureServer_UnchangedNotRewritten(t *testing.T) {
	pool := newMigratedPool(t)
	id := mustEnsureServer(t, pool, testServer)

	xmin := func() string {
		var x string
		if err := pool.QueryRow(ctx, "SELECT xmin::text FROM onebusaway_server WHERE server_id = $1", id).Scan(&x); err != nil {
			t.Fatal(err)
		}
		return x
	}
	before := xmin()
	mustEnsureServer(t, pool, testServer)
	if after := xmin(); after != before {
		t.Fatalf("unchanged server row was rewritten (xmin %s -> %s)", before, after)
	}
}

func TestEnsureServer_Normalizes(t *testing.T) {
	pool := newMigratedPool(t)

	withCredentials := models.ObaServer{ServerName: "S", ObaBaseURL: "https://user:pw@Example.com/api?key=x"}
	plain := models.ObaServer{ServerName: "S", ObaBaseURL: "https://example.com/api"}

	if a, b := mustEnsureServer(t, pool, withCredentials), mustEnsureServer(t, pool, plain); a != b {
		t.Fatalf("got two rows (%d, %d) for the same server", a, b)
	}

	var stored string
	if err := pool.QueryRow(ctx, "SELECT normalized_base_url FROM onebusaway_server").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "https://example.com/api" {
		t.Fatalf("normalized_base_url = %q, want %q", stored, "https://example.com/api")
	}
}

func TestEnsureServer_NewURLNewIdentity(t *testing.T) {
	pool := newMigratedPool(t)

	moved := testServer
	moved.ObaBaseURL = "https://api2.test-transit.example/"
	if a, b := mustEnsureServer(t, pool, testServer), mustEnsureServer(t, pool, moved); a == b {
		t.Fatal("a different base URL returned the same row")
	}
	if n := count(t, pool, "onebusaway_server"); n != 2 {
		t.Fatalf("onebusaway_server has %d rows, want 2", n)
	}
}

// The error names the server, never the URL, which may carry credentials.
func TestEnsureServer_UnnormalizableURL(t *testing.T) {
	pool := newMigratedPool(t)
	bad := models.ObaServer{ServerName: "Broken", ObaBaseURL: "https://user:secret@/"}

	err := runInTx(ctx, pool, func(q *dbgen.Queries) error {
		_, err := ensureServer(ctx, q, bad)
		return err
	})
	if err == nil {
		t.Fatal("expected an error for a URL with no host")
	}
	if !strings.Contains(err.Error(), `"Broken"`) {
		t.Errorf("error %q does not name the server", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("error %q leaks the URL", err)
	}
}

func TestEnsureAgency_KeepsNameWhenNull(t *testing.T) {
	pool := newMigratedPool(t)
	serverID := mustEnsureServer(t, pool, testServer)

	ensure := func(name string) int64 {
		var id int64
		inTx(t, pool, func(q *dbgen.Queries) error {
			var err error
			id, err = ensureAgency(ctx, q, serverID, testServer.ServerName, "1", name)
			return err
		})
		return id
	}
	currentName := func(id int64) *string {
		var name *string
		if err := pool.QueryRow(ctx, "SELECT current_name FROM agency WHERE agency_id = $1", id).Scan(&name); err != nil {
			t.Fatal(err)
		}
		return name
	}

	id := ensure("Metro")
	if got := ensure(""); got != id {
		t.Fatalf("call without a name returned %d, want %d", got, id)
	}
	if name := currentName(id); name == nil || *name != "Metro" {
		t.Fatalf("current_name = %v after a call without a name, want Metro", name)
	}

	if got := ensure("Metro Transit"); got != id {
		t.Fatalf("rename returned %d, want %d", got, id)
	}
	if name := currentName(id); name == nil || *name != "Metro Transit" {
		t.Fatalf("current_name = %v, want Metro Transit", name)
	}
}

// Agency IDs are only unique within one server.
func TestEnsureAgency_ScopedToServer(t *testing.T) {
	pool := newMigratedPool(t)
	other := models.ObaServer{ServerName: "Other", ObaBaseURL: "https://other.example/"}
	a, b := mustEnsureServer(t, pool, testServer), mustEnsureServer(t, pool, other)

	var first, second int64
	inTx(t, pool, func(q *dbgen.Queries) error {
		var err error
		if first, err = ensureAgency(ctx, q, a, "A", "1", ""); err != nil {
			return err
		}
		second, err = ensureAgency(ctx, q, b, "B", "1", "")
		return err
	})
	if first == second {
		t.Fatal("the same agency ID on two servers returned one row")
	}
}

// Each remaining helper returns the same ID for the same key, a new ID for a
// new key, and rejects empty input.
func TestEnsureHelpers_Idempotent(t *testing.T) {
	pool := newMigratedPool(t)
	serverID := mustEnsureServer(t, pool, testServer)

	var agencyID, endpointID int64
	inTx(t, pool, func(q *dbgen.Queries) error {
		var err error
		if agencyID, err = ensureAgency(ctx, q, serverID, "S", "1", ""); err != nil {
			return err
		}
		endpointID, err = ensureEndpoint(ctx, q, serverID, "S", "https://rt.example/vp.pb")
		return err
	})

	type ensureFunc func(q *dbgen.Queries, key string) (int64, error)
	tests := []struct {
		name   string
		table  string
		same   [2]string // two spellings of one identity
		other  string
		empty  string
		ensure ensureFunc
	}{
		{
			name: "endpoint", table: "vehicle_position_endpoint",
			same:  [2]string{"https://feeds.example/positions.pb?key=a", "https://u:p@FEEDS.example/positions.pb"},
			other: "https://feeds.example/other.pb", empty: "",
			ensure: func(q *dbgen.Queries, url string) (int64, error) { return ensureEndpoint(ctx, q, serverID, "S", url) },
		},
		{
			name: "static source", table: "static_gtfs_source",
			same:  [2]string{"https://gtfs.example/gtfs.zip?token=a", "https://GTFS.example/gtfs.zip"},
			other: "https://gtfs.example/other.zip", empty: "",
			ensure: func(q *dbgen.Queries, url string) (int64, error) {
				return ensureStaticSource(ctx, q, serverID, "S", url)
			},
		},
		{
			name: "vehicle", table: "vehicle_entity",
			same:  [2]string{"bus-1", "bus-1"},
			other: "bus-2", empty: "",
			ensure: func(q *dbgen.Queries, id string) (int64, error) { return ensureVehicle(ctx, q, endpointID, id) },
		},
		{
			name: "reported stop", table: "reported_stop",
			same:  [2]string{"1_100", "1_100"},
			other: "1_200", empty: "",
			ensure: func(q *dbgen.Queries, id string) (int64, error) { return ensureReportedStop(ctx, q, agencyID, id) },
		},
		{
			name: "reported trip", table: "reported_trip",
			same:  [2]string{"1_trip", "1_trip"},
			other: "1_trip_2", empty: "",
			ensure: func(q *dbgen.Queries, id string) (int64, error) { return ensureReportedTrip(ctx, q, agencyID, id) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := count(t, pool, tt.table)
			var first, second, other int64
			inTx(t, pool, func(q *dbgen.Queries) error {
				var err error
				if first, err = tt.ensure(q, tt.same[0]); err != nil {
					return err
				}
				if second, err = tt.ensure(q, tt.same[1]); err != nil {
					return err
				}
				other, err = tt.ensure(q, tt.other)
				return err
			})
			if first != second {
				t.Errorf("same identity returned %d and %d", first, second)
			}
			if other == first {
				t.Errorf("different key returned the same ID %d", first)
			}
			if got := count(t, pool, tt.table) - before; got != 2 {
				t.Errorf("%s gained %d rows, want 2", tt.table, got)
			}

			err := runInTx(ctx, pool, func(q *dbgen.Queries) error {
				_, err := tt.ensure(q, tt.empty)
				return err
			})
			if err == nil {
				t.Error("empty input did not return an error")
			}
		})
	}
}

func TestEnsureAgency_RejectsEmptyID(t *testing.T) {
	pool := newMigratedPool(t)
	serverID := mustEnsureServer(t, pool, testServer)
	err := runInTx(ctx, pool, func(q *dbgen.Queries) error {
		_, err := ensureAgency(ctx, q, serverID, "S", "", "Name")
		return err
	})
	if !errors.Is(err, errEmptyID) {
		t.Fatalf("err = %v, want errEmptyID", err)
	}
}
