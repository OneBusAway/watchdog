package db

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"watchdog.onebusaway.org/internal/db/dbgen"
)

// testDatabaseURLEnv names the PostgreSQL database the tests below run
// against. Tests that need a database skip when it is unset, so `go test ./...`
// works without PostgreSQL; CI sets it.
const testDatabaseURLEnv = "WATCHDOG_TEST_DATABASE_URL"

// expectedTables is every table the migrations create, excluding goose's own
// version table. It must match docs/historical-investigation-schema.md.
var expectedTables = []string{
	"agency",
	"bounding_box_version",
	"cluster_scheme",
	"gtfs_static_version",
	"gtfs_static_version_agency",
	"gtfs_static_version_source",
	"onebusaway_metrics_response",
	"onebusaway_metrics_response_agency",
	"onebusaway_server",
	"reported_stop",
	"reported_trip",
	"s2_cell",
	"static_gtfs_source",
	"stop_cluster_assignment",
	"stop_metadata_version",
	"trip_metadata_version",
	"unmatched_stop_episode",
	"unmatched_stop_episode_metadata",
	"unmatched_trip_episode",
	"unmatched_trip_episode_metadata",
	"vehicle_anomaly_episode",
	"vehicle_anomaly_episode_evidence",
	"vehicle_anomaly_rule_version",
	"vehicle_anomaly_type",
	"vehicle_entity",
	"vehicle_position_endpoint",
	"vehicle_position_evidence",
	"vehicle_position_response",
}

// PostgreSQL error codes the constraint tests expect.
const (
	codeForeignKeyViolation = "23503"
	codeUniqueViolation     = "23505"
	codeCheckViolation      = "23514"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestMigrationsAreEmbedded(t *testing.T) {
	entries, err := fs.ReadDir(Migrations(), ".")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no migrations embedded")
	}
	if got := entries[0].Name(); got != "00001_create_investigation_schema.sql" {
		t.Fatalf("first migration = %q, want 00001_create_investigation_schema.sql", got)
	}
}

func TestOpenRejectsInvalidURL(t *testing.T) {
	_, err := Open(context.Background(), "postgres://user:pass@host:notaport/db", discardLogger())
	if err == nil {
		t.Fatal("Open succeeded with an invalid URL")
	}
}

// The error from a failed connection is logged and sent to Sentry, so it must
// not contain the password from the URL.
func TestOpenErrorDoesNotLeakPassword(t *testing.T) {
	const password = "s3cret-do-not-log"
	url := fmt.Sprintf("postgres://watchdog:%s@127.0.0.1:1/watchdog?connect_timeout=1", password)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := Open(ctx, url, discardLogger())
	if err == nil {
		t.Fatal("Open succeeded against a closed port")
	}
	if strings.Contains(err.Error(), password) {
		t.Fatalf("error contains the password: %v", err)
	}
}

// newTestPool returns a pool whose connections use a fresh, empty schema, so
// each test migrates from scratch without touching other tests' data. The
// schema is dropped when the test ends.
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	databaseURL := os.Getenv(testDatabaseURLEnv)
	if databaseURL == "" {
		t.Skipf("%s is not set; skipping PostgreSQL test", testDatabaseURLEnv)
	}

	ctx := context.Background()
	schema := fmt.Sprintf("watchdog_test_%d", rand.Uint64())

	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse %s: %v", testDatabaseURLEnv, err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect to test schema: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newMigratedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := newTestPool(t)
	if err := Migrate(context.Background(), pool, discardLogger()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return pool
}

func listTables(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name <> 'goose_db_version'
		ORDER BY table_name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list tables: %v", err)
	}
	return tables
}

func TestMigrateCreatesSchema(t *testing.T) {
	pool := newMigratedPool(t)

	got := listTables(t, pool)
	if strings.Join(got, ",") != strings.Join(expectedTables, ",") {
		t.Fatalf("tables after migrating:\n got  %v\n want %v", got, expectedTables)
	}

	var anomalyCodes []string
	rows, err := pool.Query(context.Background(), "SELECT anomaly_code FROM vehicle_anomaly_type ORDER BY anomaly_code")
	if err != nil {
		t.Fatalf("query anomaly types: %v", err)
	}
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			t.Fatalf("scan anomaly code: %v", err)
		}
		anomalyCodes = append(anomalyCodes, code)
	}
	rows.Close()
	want := "invalid_coordinate,stale_source_timestamp,stopped_out_of_bounds"
	if got := strings.Join(anomalyCodes, ","); got != want {
		t.Fatalf("seeded anomaly types = %s, want %s", got, want)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	pool := newMigratedPool(t)
	if err := Migrate(context.Background(), pool, discardLogger()); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}

func TestMigrationsRollBackAndReapply(t *testing.T) {
	pool := newMigratedPool(t)
	ctx := context.Background()

	provider, err := newProvider(pool)
	if err != nil {
		t.Fatalf("newProvider: %v", err)
	}
	defer provider.Close()

	if _, err := provider.DownTo(ctx, 0); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	if got := listTables(t, pool); len(got) != 0 {
		t.Fatalf("tables left after migrating down: %v", got)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("migrate up again: %v", err)
	}
	if got := listTables(t, pool); len(got) != len(expectedTables) {
		t.Fatalf("got %d tables after re-applying, want %d", len(got), len(expectedTables))
	}
}

func TestGeneratedQueriesMatchSchema(t *testing.T) {
	pool := newMigratedPool(t)
	ctx := context.Background()

	const url = "https://api.example.com"
	if _, err := pool.Exec(ctx,
		"INSERT INTO onebusaway_server (normalized_base_url, display_name) VALUES ($1, 'Example')", url); err != nil {
		t.Fatalf("insert server: %v", err)
	}

	server, err := dbgen.New(pool).GetOneBusAwayServerByURL(ctx, url)
	if err != nil {
		t.Fatalf("GetOneBusAwayServerByURL: %v", err)
	}
	if server.DisplayName != "Example" || server.CreatedAt.IsZero() {
		t.Fatalf("unexpected server row: %+v", server)
	}
}

// fixture inserts rows through SQL with RETURNING and remembers IDs, so the
// constraint tests can stay short.
type fixture struct {
	t    *testing.T
	pool *pgxpool.Pool
}

func (f fixture) id(query string, args ...any) int64 {
	f.t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(), query, args...).Scan(&id); err != nil {
		f.t.Fatalf("fixture %q: %v", query, err)
	}
	return id
}

func (f fixture) exec(query string, args ...any) error {
	_, err := f.pool.Exec(context.Background(), query, args...)
	return err
}

func expectCode(t *testing.T, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("got error %v, want PostgreSQL error %s", err, code)
	}
	if pgErr.Code != code {
		t.Fatalf("got PostgreSQL error %s (%s), want %s", pgErr.Code, pgErr.Message, code)
	}
}

func expectOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReportedStopIsScopedToAgency(t *testing.T) {
	f := fixture{t, newMigratedPool(t)}
	server := f.id("INSERT INTO onebusaway_server (normalized_base_url, display_name) VALUES ('u', 'n') RETURNING server_id")
	agencyA := f.id("INSERT INTO agency (server_id, external_agency_id) VALUES ($1, 'A') RETURNING agency_id", server)
	agencyB := f.id("INSERT INTO agency (server_id, external_agency_id) VALUES ($1, 'B') RETURNING agency_id", server)

	insert := "INSERT INTO reported_stop (agency_id, oba_stop_id) VALUES ($1, '123')"
	expectOK(t, f.exec(insert, agencyA))
	expectOK(t, f.exec(insert, agencyB))
	expectCode(t, f.exec(insert, agencyA), codeUniqueViolation)
}

// unmatchedFixture is one agency with a reported stop, plus helpers to add OBA
// responses for it.
type unmatchedFixture struct {
	fixture
	agency, otherAgency, stop int64
}

func newUnmatchedFixture(t *testing.T) unmatchedFixture {
	f := fixture{t, newMigratedPool(t)}
	server := f.id("INSERT INTO onebusaway_server (normalized_base_url, display_name) VALUES ('u', 'n') RETURNING server_id")
	agency := f.id("INSERT INTO agency (server_id, external_agency_id) VALUES ($1, 'A') RETURNING agency_id", server)
	other := f.id("INSERT INTO agency (server_id, external_agency_id) VALUES ($1, 'B') RETURNING agency_id", server)
	stop := f.id("INSERT INTO reported_stop (agency_id, oba_stop_id) VALUES ($1, '123') RETURNING reported_stop_id", agency)
	return unmatchedFixture{f, agency, other, stop}
}

func (u unmatchedFixture) response(agency int64, complete bool) int64 {
	response := u.id(`INSERT INTO onebusaway_metrics_response (server_id, observed_at)
		SELECT server_id, now() FROM agency WHERE agency_id = $1 RETURNING response_id`, agency)
	return u.id(`INSERT INTO onebusaway_metrics_response_agency (response_id, agency_id, response_complete)
		VALUES ($1, $2, $3) RETURNING response_agency_id`, response, agency, complete)
}

const insertStopEpisode = `INSERT INTO unmatched_stop_episode
	(reported_stop_id, agency_id, first_response_agency_id, closing_response_agency_id, last_reported_at, close_reason)
	VALUES ($1, $2, $3, $4, now(), $5)`

func TestUnmatchedEpisodeOpensOnlyFromCompleteResponse(t *testing.T) {
	u := newUnmatchedFixture(t)
	incomplete := u.response(u.agency, false)
	expectCode(t, u.exec(insertStopEpisode, u.stop, u.agency, incomplete, nil, nil), codeCheckViolation)

	complete := u.response(u.agency, true)
	expectOK(t, u.exec(insertStopEpisode, u.stop, u.agency, complete, nil, nil))
}

func TestUnmatchedEpisodeAllowsOneOpenEpisodePerStop(t *testing.T) {
	u := newUnmatchedFixture(t)
	first := u.response(u.agency, true)
	expectOK(t, u.exec(insertStopEpisode, u.stop, u.agency, first, nil, nil))

	second := u.response(u.agency, true)
	expectCode(t, u.exec(insertStopEpisode, u.stop, u.agency, second, nil, nil), codeUniqueViolation)

	// A closed episode does not block a new open one.
	closing := u.response(u.agency, true)
	expectOK(t, u.exec(`UPDATE unmatched_stop_episode
		SET closing_response_agency_id = $1, close_reason = 'not_reported'`, closing))
	expectOK(t, u.exec(insertStopEpisode, u.stop, u.agency, second, nil, nil))
}

func TestUnmatchedEpisodeCloseRules(t *testing.T) {
	u := newUnmatchedFixture(t)
	opening := u.response(u.agency, true)
	closing := u.response(u.agency, true)

	t.Run("close reason requires a closing response", func(t *testing.T) {
		expectCode(t, u.exec(insertStopEpisode, u.stop, u.agency, opening, nil, "not_reported"), codeCheckViolation)
	})
	t.Run("closing response requires a close reason", func(t *testing.T) {
		expectCode(t, u.exec(insertStopEpisode, u.stop, u.agency, opening, closing, nil), codeCheckViolation)
	})
	t.Run("unknown close reason is rejected", func(t *testing.T) {
		expectCode(t, u.exec(insertStopEpisode, u.stop, u.agency, opening, closing, "matched"), codeCheckViolation)
	})
	t.Run("another agency's response cannot close it", func(t *testing.T) {
		otherAgency := u.response(u.otherAgency, true)
		expectCode(t, u.exec(insertStopEpisode, u.stop, u.agency, opening, otherAgency, "not_reported"), codeForeignKeyViolation)
	})
	t.Run("incomplete response cannot close it", func(t *testing.T) {
		incomplete := u.response(u.agency, false)
		expectCode(t, u.exec(insertStopEpisode, u.stop, u.agency, opening, incomplete, "not_reported"), codeCheckViolation)
	})
}

func TestStopMetadataNotFoundRowIsEmpty(t *testing.T) {
	u := newUnmatchedFixture(t)
	version := u.id(`INSERT INTO gtfs_static_version (server_id, published_at)
		SELECT server_id, now() FROM agency WHERE agency_id = $1 RETURNING gtfs_static_version_id`, u.agency)

	insert := `INSERT INTO stop_metadata_version
		(gtfs_static_version_id, agency_id, reported_stop_id, static_stop_found, stop_name)
		VALUES ($1, $2, $3, $4, $5)`
	expectCode(t, u.exec(insert, version, u.agency, u.stop, false, "Main St"), codeCheckViolation)
	expectOK(t, u.exec(insert, version, u.agency, u.stop, false, nil))
}

func TestStopMetadataMustMatchStopAgency(t *testing.T) {
	u := newUnmatchedFixture(t)
	version := u.id(`INSERT INTO gtfs_static_version (server_id, published_at)
		SELECT server_id, now() FROM agency WHERE agency_id = $1 RETURNING gtfs_static_version_id`, u.agency)

	expectCode(t, u.exec(`INSERT INTO stop_metadata_version
		(gtfs_static_version_id, agency_id, reported_stop_id, static_stop_found)
		VALUES ($1, $2, $3, false)`, version, u.otherAgency, u.stop), codeForeignKeyViolation)
}

// vehicleFixture is one vehicle on endpoint A, a second endpoint B, and a rule
// version for the stale-source-timestamp anomaly.
type vehicleFixture struct {
	fixture
	vehicle, endpointA, endpointB, anomalyType, rule int64
}

func newVehicleFixture(t *testing.T) vehicleFixture {
	f := fixture{t, newMigratedPool(t)}
	server := f.id("INSERT INTO onebusaway_server (normalized_base_url, display_name) VALUES ('u', 'n') RETURNING server_id")
	endpointA := f.id("INSERT INTO vehicle_position_endpoint (server_id, normalized_vehicle_position_url) VALUES ($1, 'a') RETURNING endpoint_id", server)
	endpointB := f.id("INSERT INTO vehicle_position_endpoint (server_id, normalized_vehicle_position_url) VALUES ($1, 'b') RETURNING endpoint_id", server)
	vehicle := f.id("INSERT INTO vehicle_entity (endpoint_id, external_vehicle_id) VALUES ($1, '101') RETURNING vehicle_entity_id", endpointA)
	anomalyType := f.id("SELECT anomaly_type_id FROM vehicle_anomaly_type WHERE anomaly_code = 'stale_source_timestamp'")
	rule := f.id(`INSERT INTO vehicle_anomaly_rule_version
		(anomaly_type_id, rule_logic_version, version_name, effective_at, stale_age_threshold_seconds)
		VALUES ($1, 1, 'logic 1, stale > 150s', now(), 150) RETURNING rule_version_id`, anomalyType)
	return vehicleFixture{f, vehicle, endpointA, endpointB, anomalyType, rule}
}

func (v vehicleFixture) response(endpoint int64, observedAt time.Time) int64 {
	return v.id(`INSERT INTO vehicle_position_response (endpoint_id, observed_at, incrementality)
		VALUES ($1, $2, 'FULL_DATASET') RETURNING vehicle_position_response_id`, endpoint, observedAt)
}

const insertVehicleEpisode = `INSERT INTO vehicle_anomaly_episode
	(vehicle_entity_id, anomaly_type_id, rule_version_id, opening_response_id, closing_response_id, close_reason)
	VALUES ($1, $2, $3, $4, $5, $6)`

func TestVehicleEpisodeResponsesMustBeFromVehicleEndpoint(t *testing.T) {
	v := newVehicleFixture(t)
	now := time.Now()

	otherEndpoint := v.response(v.endpointB, now)
	expectCode(t, v.exec(insertVehicleEpisode, v.vehicle, v.anomalyType, v.rule, otherEndpoint, nil, nil), codeCheckViolation)

	opening := v.response(v.endpointA, now)
	closingElsewhere := v.response(v.endpointB, now.Add(time.Minute))
	expectCode(t, v.exec(insertVehicleEpisode, v.vehicle, v.anomalyType, v.rule, opening, closingElsewhere, "fresh_source_timestamp"), codeCheckViolation)
}

func TestVehicleEpisodeCannotCloseBeforeItOpens(t *testing.T) {
	v := newVehicleFixture(t)
	now := time.Now()
	opening := v.response(v.endpointA, now)
	earlier := v.response(v.endpointA, now.Add(-time.Minute))
	later := v.response(v.endpointA, now.Add(time.Minute))

	expectCode(t, v.exec(insertVehicleEpisode, v.vehicle, v.anomalyType, v.rule, opening, earlier, "fresh_source_timestamp"), codeCheckViolation)
	expectOK(t, v.exec(insertVehicleEpisode, v.vehicle, v.anomalyType, v.rule, opening, later, "fresh_source_timestamp"))
}

func TestVehicleEpisodeAllowsOneOpenEpisodePerType(t *testing.T) {
	v := newVehicleFixture(t)
	opening := v.response(v.endpointA, time.Now())
	expectOK(t, v.exec(insertVehicleEpisode, v.vehicle, v.anomalyType, v.rule, opening, nil, nil))
	expectCode(t, v.exec(insertVehicleEpisode, v.vehicle, v.anomalyType, v.rule, opening, nil, nil), codeUniqueViolation)
}

func TestVehicleEpisodeHasOneOpeningEvidenceLink(t *testing.T) {
	v := newVehicleFixture(t)
	response := v.response(v.endpointA, time.Now())
	episode := v.id(insertVehicleEpisode+" RETURNING vehicle_anomaly_episode_id", v.vehicle, v.anomalyType, v.rule, response, nil, nil)

	evidence := func() int64 {
		return v.id(`INSERT INTO vehicle_position_evidence (vehicle_position_response_id, position_valid)
			VALUES ($1, true) RETURNING vehicle_position_evidence_id`, response)
	}
	link := `INSERT INTO vehicle_anomaly_episode_evidence
		(vehicle_anomaly_episode_id, vehicle_position_evidence_id, evidence_role) VALUES ($1, $2, $3)`

	expectOK(t, v.exec(link, episode, evidence(), "opening"))
	expectCode(t, v.exec(link, episode, evidence(), "opening"), codeUniqueViolation)
	expectOK(t, v.exec(link, episode, evidence(), "meaningful_change"))
	expectOK(t, v.exec(link, episode, evidence(), "meaningful_change"))
}

func TestS2CellLevelMustMatchScheme(t *testing.T) {
	f := fixture{t, newMigratedPool(t)}
	expectOK(t, f.exec(`INSERT INTO cluster_scheme (scheme_version, s2_level, station_grouping, description)
		VALUES ('s2_l13_root_station_v1', 13, 'root_parent_station', 'test')`))

	insert := `INSERT INTO s2_cell (scheme_version, cell_id, cell_level, center_latitude, center_longitude)
		VALUES ('s2_l13_root_station_v1', $1, $2, 47.6, -122.3)`
	expectCode(t, f.exec(insert, "cell-14", 14), codeForeignKeyViolation)
	expectOK(t, f.exec(insert, "cell-13", 13))
}
