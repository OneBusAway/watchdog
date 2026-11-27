package history

import (
	"context"
	"fmt"
	"sync"
	"time"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	gtfsrt "github.com/OneBusAway/go-gtfs/proto"
	"github.com/jackc/pgx/v5/pgtype"

	"watchdog.onebusaway.org/internal/db/dbgen"
	"watchdog.onebusaway.org/internal/geo"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/metrics"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
)

// Steps name these writes in logs, Sentry and the write failure counter.
const (
	vehicleAnomalyStep            = "vehicle_anomalies"
	vehicleAnomalyLoadStep        = "vehicle_anomaly_load"
	vehicleAnomalyRuleVersionStep = "vehicle_anomaly_rule_versions"
)

// Evidence roles link evidence to an episode.
const (
	evidenceOpening          = "opening"
	evidenceMeaningfulChange = "meaningful_change"
	evidenceResolution       = "resolution"
)

// vehicleAnomalyRuleLogicVersion is bumped whenever a rule's detection logic
// changes, so a new vehicle_anomaly_rule_version row is recorded.
const vehicleAnomalyRuleLogicVersion int32 = 1

// gtfsDateLayout is a GTFS date (YYYYMMDD), the form trip start dates are
// compared and classified in.
const gtfsDateLayout = "20060102"

// vehicleFacts is what a vehicle's evidence row records beyond the rule
// inputs.
type vehicleFacts struct {
	vehicle remoteGtfs.Vehicle
	// latitude and longitude are the raw values, kept even when invalid.
	latitude  *float64
	longitude *float64
	// agency is the attributed agency entry; it is the zero value when
	// attributionReason is set.
	agency            models.ObaServer
	attributionReason gtfs.AttributionFailureReason
	// boxVersionID is the bounding_box_version of the box the vehicle was
	// checked against, read when it was checked. It is nil when no box
	// applied or the box's version is not recorded.
	boxVersionID *int64
}

// serverVehicleEpisodes is one server's open episodes. mu serializes every
// change to them, from every entry on the server.
type serverVehicleEpisodes struct {
	mu     sync.Mutex
	loaded bool
	open   map[vehicleEpisodeKey]*openVehicleEpisode
}

// openVehicleEpisodes holds every server's open episodes, keyed by the
// sanitized base URL. The database allows one open episode per vehicle and
// type, and a vehicle belongs to a feed of a server, not to an agency, so
// every entry on a base URL shares one set: agency-mode entries that share a
// feed URL, and a server-scoped entry alongside them, see the same episode.
type openVehicleEpisodes struct {
	mu      sync.Mutex
	entries map[string]*serverVehicleEpisodes
}

func newOpenVehicleEpisodes() *openVehicleEpisodes {
	return &openVehicleEpisodes{entries: make(map[string]*serverVehicleEpisodes)}
}

// forServer returns the server's open episodes, creating an unloaded set if
// needed.
func (o *openVehicleEpisodes) forServer(baseURL string) *serverVehicleEpisodes {
	o.mu.Lock()
	defer o.mu.Unlock()
	entry, ok := o.entries[baseURL]
	if !ok {
		entry = &serverVehicleEpisodes{open: make(map[vehicleEpisodeKey]*openVehicleEpisode)}
		o.entries[baseURL] = entry
	}
	return entry
}

// vehicleRuleVersions holds the anomaly type IDs and the rule version in
// effect for each type. It is filled once, before the first episode opens.
type vehicleRuleVersions struct {
	mu        sync.Mutex
	ready     bool
	typeIDs   map[vehicleAnomalyType]int16
	versionID map[vehicleAnomalyType]int64
}

// vehicleRuleSettings is one type's rule as Watchdog currently applies it.
type vehicleRuleSettings struct {
	logicVersion              int32
	staleAgeThresholdSeconds  *int32
	versionName               string
	reportedSpeedThresholdMps *float64
}

// ruleSettings returns the rule each type is applied with.
func ruleSettings(anomaly vehicleAnomalyType, threshold time.Duration) vehicleRuleSettings {
	settings := vehicleRuleSettings{logicVersion: vehicleAnomalyRuleLogicVersion, versionName: fmt.Sprintf("logic %d", vehicleAnomalyRuleLogicVersion)}
	if anomaly == anomalyStaleSourceTimestamp {
		seconds := int32(threshold / time.Second)
		settings.staleAgeThresholdSeconds = &seconds
		settings.versionName = fmt.Sprintf("logic %d, stale > %ds", vehicleAnomalyRuleLogicVersion, seconds)
	}
	return settings
}

// sameRule reports whether the newest stored rule version matches settings.
func sameRule(row dbgen.ListNewestVehicleAnomalyRuleVersionsRow, settings vehicleRuleSettings) bool {
	return row.RuleLogicVersion == settings.logicVersion &&
		equalPtr(row.StaleAgeThresholdSeconds, settings.staleAgeThresholdSeconds) &&
		equalPtr(row.ReportedSpeedThresholdMps, settings.reportedSpeedThresholdMps)
}

// SetFetchInterval sets the collection interval the stale threshold is
// derived from. app.New calls it once, before collection starts.
func (h *HistoryService) SetFetchInterval(interval time.Duration) {
	h.fetchInterval = interval
}

// RecordVehiclePositions records the vehicle anomalies of one realtime
// snapshot for one server. server, agencies and attributor are the ones the
// vehicle passes used; boxes is the bounding box store they read.
//
// Call it only after a successful realtime fetch. A snapshot that opens,
// closes and adds evidence to nothing does not touch the database.
func (h *HistoryService) RecordVehiclePositions(ctx context.Context, server models.ObaServer, snapshot *models.RealtimeData, attributor metrics.VehicleAttributor, boxes *geo.BoundingBoxStore) {
	if h == nil || h.pool == nil || snapshot == nil {
		return
	}
	baseURL := utils.SanitizeServerURL(server.ObaBaseURL)
	if baseURL == "" {
		return
	}
	// write bounds the transaction, but the queries run on the context fn
	// captures, so bound that too.
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()

	entry := h.vehicles.forServer(baseURL)
	entry.mu.Lock()
	defer entry.mu.Unlock()

	if !entry.loaded && !h.loadOpenVehicleEpisodes(ctx, server, baseURL, entry) {
		return
	}
	// Rule versions are recorded at the first recording after startup, even
	// one that only closes episodes, so the database always holds the rules
	// this process applies.
	threshold := staleThreshold(h.fetchInterval)
	if !h.ensureRuleVersions(ctx, server, threshold) {
		return
	}

	// Read the static version together with the observations, so evidence
	// cites the version current when the vehicle was judged.
	var staticVersionID *int64
	if version, ok := h.static.currentVersion(server.ServerKey()); ok {
		staticVersionID = &version.id
	}
	feeds, observations := vehicleObservations(server, snapshot, attributor, boxes, h.static.box)
	plan := planVehicleAnomalies(entry.open, feeds, observations, threshold)
	if plan.empty() {
		return
	}

	var opened map[vehicleEpisodeKey]*openVehicleEpisode
	committed := h.write(ctx, vehicleAnomalyStep, publicationTags(server), func(q *dbgen.Queries) error {
		var err error
		opened, err = h.recordVehicleEpisodeChanges(ctx, q, server, plan, entry.open, feeds, staticVersionID)
		return err
	})
	if !committed {
		// The commit may have reached the database even though the client
		// saw an error, so memory can no longer be trusted: reload on the
		// next snapshot, which then records any difference with its own data.
		entry.loaded = false
		return
	}
	for key, episode := range opened {
		entry.open[key] = episode
	}
	for _, change := range plan.changes {
		episode := entry.open[change.key]
		episode.tripID = change.observation.tripID
		episode.tripStartDate = change.observation.tripStartDate
	}
	for _, closed := range plan.closes {
		delete(entry.open, closed.key)
	}
}

// loadOpenVehicleEpisodes replaces entry's open episodes with the ones in the
// database. It reports whether it succeeded; on failure entry is unchanged
// and stays unloaded, and the next snapshot tries again.
func (h *HistoryService) loadOpenVehicleEpisodes(ctx context.Context, server models.ObaServer, baseURL string, entry *serverVehicleEpisodes) bool {
	rows, err := dbgen.New(h.pool).ListOpenVehicleAnomalyEpisodesForServer(ctx, baseURL)
	if err != nil {
		reportWriteFailure(h.logger, vehicleAnomalyLoadStep, publicationTags(server), fmt.Errorf("load open vehicle anomaly episodes: %w", err))
		return false
	}
	entry.open = make(map[vehicleEpisodeKey]*openVehicleEpisode, len(rows))
	for _, row := range rows {
		key := vehicleEpisodeKey{
			feedURL:   row.NormalizedVehiclePositionUrl,
			vehicleID: row.ExternalVehicleID,
			anomaly:   vehicleAnomalyType(row.AnomalyCode),
		}
		episode := &openVehicleEpisode{episodeID: row.VehicleAnomalyEpisodeID}
		if row.TripID != nil {
			episode.tripID = *row.TripID
		}
		if row.TripStartDate.Valid {
			episode.tripStartDate = row.TripStartDate.Time.Format(gtfsDateLayout)
		}
		entry.open[key] = episode
	}
	entry.loaded = true
	return true
}

// ensureRuleVersions records the rule version of every anomaly type once per
// process, before the first episode opens. A type gets a new row when its
// newest stored version differs from the rule Watchdog applies now, or when
// it has none. It reports whether the versions are ready.
func (h *HistoryService) ensureRuleVersions(ctx context.Context, server models.ObaServer, threshold time.Duration) bool {
	h.rules.mu.Lock()
	defer h.rules.mu.Unlock()
	if h.rules.ready {
		return true
	}

	typeIDs := make(map[vehicleAnomalyType]int16, len(vehicleAnomalyTypes))
	versionIDs := make(map[vehicleAnomalyType]int64, len(vehicleAnomalyTypes))
	committed := h.write(ctx, vehicleAnomalyRuleVersionStep, publicationTags(server), func(q *dbgen.Queries) error {
		types, err := q.ListVehicleAnomalyTypeIDs(ctx)
		if err != nil {
			return fmt.Errorf("list vehicle anomaly types: %w", err)
		}
		for _, row := range types {
			typeIDs[vehicleAnomalyType(row.AnomalyCode)] = row.AnomalyTypeID
		}
		newest, err := q.ListNewestVehicleAnomalyRuleVersions(ctx)
		if err != nil {
			return fmt.Errorf("list newest vehicle anomaly rule versions: %w", err)
		}
		newestByType := make(map[int16]dbgen.ListNewestVehicleAnomalyRuleVersionsRow, len(newest))
		for _, row := range newest {
			newestByType[row.AnomalyTypeID] = row
		}

		for _, anomaly := range vehicleAnomalyTypes {
			typeID, ok := typeIDs[anomaly]
			if !ok {
				return fmt.Errorf("vehicle anomaly type %q is not in the database", anomaly)
			}
			settings := ruleSettings(anomaly, threshold)
			if row, ok := newestByType[typeID]; ok && sameRule(row, settings) {
				versionIDs[anomaly] = row.RuleVersionID
				continue
			}
			id, err := q.InsertVehicleAnomalyRuleVersion(ctx, dbgen.InsertVehicleAnomalyRuleVersionParams{
				AnomalyTypeID:            typeID,
				RuleLogicVersion:         settings.logicVersion,
				VersionName:              settings.versionName,
				WatchdogBuildVersion:     nonEmpty(h.version),
				EffectiveAt:              h.startedAt,
				StaleAgeThresholdSeconds: settings.staleAgeThresholdSeconds,
			})
			if err != nil {
				return fmt.Errorf("insert rule version for %q: %w", anomaly, err)
			}
			versionIDs[anomaly] = id
		}
		return nil
	})
	if !committed {
		return false
	}
	h.rules.typeIDs = typeIDs
	h.rules.versionID = versionIDs
	h.rules.ready = true
	return true
}

// vehicleObservations turns a realtime snapshot into the rules' input. A
// vehicle the snapshot only knows from a trip update, with no position
// entity, is not a position report and is left out, as is a vehicle with no
// ID.
//
// boxVersion returns the recorded version of a bounding box key.
func vehicleObservations(server models.ObaServer, snapshot *models.RealtimeData, attributor metrics.VehicleAttributor, boxes *geo.BoundingBoxStore, boxVersion func(string) (int64, bool)) ([]vehicleFeed, []vehicleObservation) {
	feeds := make([]vehicleFeed, 0, len(snapshot.Feeds))
	feedURLs := make(map[string]string, len(snapshot.Feeds))
	for _, feed := range snapshot.Feeds {
		feeds = append(feeds, vehicleFeed{
			feedURL:            feed.FeedURL,
			observedAt:         snapshot.ObservationAt,
			fullDataset:        feed.FullDataset,
			sourceTimestamp:    feed.SourceTimestamp,
			payloadHash:        feed.PayloadHash,
			reportedVehicleIDs: feed.ReportedVehicleIDs,
		})
		feedURLs[feed.FeedID] = feed.FeedURL
	}

	observations := make([]vehicleObservation, 0, len(snapshot.Vehicles))
	for _, realtimeVehicle := range snapshot.Vehicles {
		vehicle := realtimeVehicle.Vehicle
		feedURL, ok := feedURLs[realtimeVehicle.FeedID]
		vehicleID := vehicle.GetID().ID
		if !ok || vehicleID == "" || !vehicle.IsEntityInMessage {
			continue
		}
		observations = append(observations, observeVehicle(server, feedURL, vehicle, attributor, boxes, boxVersion))
	}
	return feeds, observations
}

// observeVehicle judges one vehicle the way trackInvalidVehiclesAndStoppedOutOfBounds
// does: coordinates first, then the agency box when the vehicle is
// attributed and its agency has one, otherwise the server box.
func observeVehicle(server models.ObaServer, feedURL string, vehicle remoteGtfs.Vehicle, attributor metrics.VehicleAttributor, boxes *geo.BoundingBoxStore, boxVersion func(string) (int64, bool)) vehicleObservation {
	lat, lon, positionReason := geo.VehiclePosition(vehicle)
	agency, attributionReason := attributor.Attribute(vehicle)

	obs := vehicleObservation{
		feedURL:        feedURL,
		vehicleID:      vehicle.GetID().ID,
		sourceTime:     vehicle.Timestamp,
		positionReason: positionReason,
		stoppedAtStop:  vehicle.CurrentStatus != nil && *vehicle.CurrentStatus == gtfsrt.VehiclePosition_STOPPED_AT,
		facts: vehicleFacts{
			vehicle:           vehicle,
			agency:            agency,
			attributionReason: attributionReason,
		},
	}
	if vehicle.Position != nil {
		obs.facts.latitude = float32Ptr(vehicle.Position.Latitude)
		obs.facts.longitude = float32Ptr(vehicle.Position.Longitude)
	}
	if vehicle.Trip != nil {
		obs.tripID = vehicle.Trip.ID.ID
		if vehicle.Trip.ID.HasStartDate {
			obs.tripStartDate = vehicle.Trip.ID.StartDate.Format(gtfsDateLayout)
		}
	}

	if boxes == nil {
		return obs
	}
	boxKey := server.ServerKey()
	if attributionReason == "" && agency.AgencyID != "" {
		agencyKey := models.ServerKey(server.ObaBaseURL, agency.AgencyID)
		if _, ok := boxes.Get(agencyKey); ok {
			boxKey = agencyKey
		}
	}
	if box, ok := boxes.Get(boxKey); ok {
		if id, ok := boxVersion(boxKey); ok {
			obs.facts.boxVersionID = &id
		}
		if positionReason == "" {
			inside := box.Contains(lat, lon)
			obs.insideBox = &inside
		}
	}
	return obs
}

// vehicleEvidenceKey identifies the one evidence row a vehicle gets per feed
// response, shared by every episode that tick links to it.
type vehicleEvidenceKey struct {
	feedURL   string
	vehicleID string
}

// vehicleTickWriter writes one plan inside one transaction, creating each
// identity, response and evidence row at most once.
type vehicleTickWriter struct {
	h        *HistoryService
	q        *dbgen.Queries
	server   models.ObaServer
	serverID int64
	feeds    map[string]vehicleFeed
	// needsBox holds the vehicles whose evidence backs a
	// stopped_out_of_bounds episode, so their row records the box version.
	needsBox        map[vehicleEvidenceKey]bool
	staticVersionID *int64

	endpoints map[string]int64
	responses map[string]int64
	vehicles  map[vehicleEvidenceKey]int64
	evidence  map[vehicleEvidenceKey]int64
	agencies  map[string]int64
}

// recordVehicleEpisodeChanges writes the plan's opens, changes and closes. It
// returns the opened episodes, which the caller adds to memory once the
// transaction commits.
func (h *HistoryService) recordVehicleEpisodeChanges(ctx context.Context, q *dbgen.Queries, server models.ObaServer, plan vehicleEpisodePlan, open map[vehicleEpisodeKey]*openVehicleEpisode, feeds []vehicleFeed, staticVersionID *int64) (map[vehicleEpisodeKey]*openVehicleEpisode, error) {
	serverID, err := ensureServer(ctx, q, server, h.resolver)
	if err != nil {
		return nil, err
	}
	w := &vehicleTickWriter{
		h: h, q: q, server: server, serverID: serverID,
		feeds:     make(map[string]vehicleFeed, len(feeds)),
		needsBox:  plan.boxEvidence(),
		endpoints: make(map[string]int64),
		responses: make(map[string]int64),
		vehicles:  make(map[vehicleEvidenceKey]int64),
		evidence:  make(map[vehicleEvidenceKey]int64),
		agencies:  make(map[string]int64),

		staticVersionID: staticVersionID,
	}
	for _, feed := range feeds {
		w.feeds[feed.feedURL] = feed
	}

	opened := make(map[vehicleEpisodeKey]*openVehicleEpisode, len(plan.opens))
	for _, o := range plan.opens {
		episode, err := w.open(ctx, o)
		if err != nil {
			return nil, err
		}
		opened[o.key] = episode
	}
	for _, change := range plan.changes {
		if err := w.link(ctx, open[change.key].episodeID, change.key, change.observation, evidenceMeaningfulChange); err != nil {
			return nil, err
		}
	}
	for _, c := range plan.closes {
		if err := w.close(ctx, open[c.key].episodeID, c); err != nil {
			return nil, err
		}
	}
	return opened, nil
}

// boxEvidence returns the vehicles whose evidence this plan links to a
// stopped_out_of_bounds episode.
func (p vehicleEpisodePlan) boxEvidence() map[vehicleEvidenceKey]bool {
	needs := make(map[vehicleEvidenceKey]bool)
	mark := func(key vehicleEpisodeKey) {
		if key.anomaly == anomalyStoppedOutOfBounds {
			needs[vehicleEvidenceKey{key.feedURL, key.vehicleID}] = true
		}
	}
	for _, o := range p.opens {
		mark(o.key)
	}
	for _, change := range p.changes {
		mark(change.key)
	}
	for _, c := range p.closes {
		if c.observation != nil {
			mark(c.key)
		}
	}
	return needs
}

// open inserts an episode and links its opening evidence.
func (w *vehicleTickWriter) open(ctx context.Context, o vehicleEpisodeOpen) (*openVehicleEpisode, error) {
	responseID, err := w.response(ctx, o.key.feedURL)
	if err != nil {
		return nil, err
	}
	vehicleID, err := w.vehicle(ctx, o.key.feedURL, o.key.vehicleID)
	if err != nil {
		return nil, err
	}
	episodeID, err := w.q.InsertVehicleAnomalyEpisode(ctx, dbgen.InsertVehicleAnomalyEpisodeParams{
		VehicleEntityID:   vehicleID,
		AnomalyTypeID:     w.h.rules.typeIDs[o.key.anomaly],
		RuleVersionID:     w.h.rules.versionID[o.key.anomaly],
		OpeningResponseID: responseID,
	})
	if err != nil {
		return nil, fmt.Errorf("server %q: open %s for vehicle %q: %w", w.server.ServerName, o.key.anomaly, o.key.vehicleID, err)
	}
	if err := w.link(ctx, episodeID, o.key, o.observation, evidenceOpening); err != nil {
		return nil, err
	}
	return &openVehicleEpisode{
		episodeID:     episodeID,
		tripID:        o.observation.tripID,
		tripStartDate: o.observation.tripStartDate,
	}, nil
}

// close closes an episode and, when the vehicle was present, links its
// resolution evidence.
func (w *vehicleTickWriter) close(ctx context.Context, episodeID int64, c vehicleEpisodeClose) error {
	responseID, err := w.response(ctx, c.key.feedURL)
	if err != nil {
		return err
	}
	rows, err := w.q.CloseVehicleAnomalyEpisode(ctx, dbgen.CloseVehicleAnomalyEpisodeParams{
		ClosingResponseID:       responseID,
		CloseReason:             string(c.reason),
		VehicleAnomalyEpisodeID: episodeID,
	})
	if err != nil {
		return fmt.Errorf("server %q: close %s for vehicle %q: %w", w.server.ServerName, c.key.anomaly, c.key.vehicleID, err)
	}
	if rows != 1 {
		return fmt.Errorf("server %q: close %s for vehicle %q: episode %d is not open", w.server.ServerName, c.key.anomaly, c.key.vehicleID, episodeID)
	}
	if c.observation == nil {
		return nil
	}
	return w.link(ctx, episodeID, c.key, *c.observation, evidenceResolution)
}

// link links the vehicle's evidence for this tick to an episode, inserting
// the evidence the first time the tick needs it.
func (w *vehicleTickWriter) link(ctx context.Context, episodeID int64, key vehicleEpisodeKey, obs vehicleObservation, role string) error {
	evidenceID, err := w.evidenceFor(ctx, obs)
	if err != nil {
		return err
	}
	if err := w.q.LinkVehicleAnomalyEpisodeEvidence(ctx, dbgen.LinkVehicleAnomalyEpisodeEvidenceParams{
		VehicleAnomalyEpisodeID:   episodeID,
		VehiclePositionEvidenceID: evidenceID,
		EvidenceRole:              role,
	}); err != nil {
		return fmt.Errorf("server %q: link %s evidence for vehicle %q: %w", w.server.ServerName, role, key.vehicleID, err)
	}
	return nil
}

// response returns the feed's vehicle_position_response for this tick,
// inserting it the first time. Only feeds with a change get one.
func (w *vehicleTickWriter) response(ctx context.Context, feedURL string) (int64, error) {
	if id, ok := w.responses[feedURL]; ok {
		return id, nil
	}
	endpointID, err := w.endpoint(ctx, feedURL)
	if err != nil {
		return 0, err
	}
	feed := w.feeds[feedURL]
	incrementality := gtfsrt.FeedHeader_DIFFERENTIAL.String()
	if feed.fullDataset {
		incrementality = gtfsrt.FeedHeader_FULL_DATASET.String()
	}
	id, err := w.q.InsertVehiclePositionResponse(ctx, dbgen.InsertVehiclePositionResponseParams{
		EndpointID:       endpointID,
		ObservedAt:       feed.observedAt,
		SourceHeaderTime: feed.sourceTimestamp,
		Incrementality:   incrementality,
		PayloadSha256:    nonEmpty(feed.payloadHash),
	})
	if err != nil {
		return 0, fmt.Errorf("server %q: insert vehicle-position response: %w", w.server.ServerName, err)
	}
	w.responses[feedURL] = id
	return id, nil
}

func (w *vehicleTickWriter) endpoint(ctx context.Context, feedURL string) (int64, error) {
	if id, ok := w.endpoints[feedURL]; ok {
		return id, nil
	}
	id, err := ensureEndpoint(ctx, w.q, w.serverID, w.server.ServerName, feedURL)
	if err != nil {
		return 0, err
	}
	w.endpoints[feedURL] = id
	return id, nil
}

func (w *vehicleTickWriter) vehicle(ctx context.Context, feedURL, vehicleID string) (int64, error) {
	key := vehicleEvidenceKey{feedURL, vehicleID}
	if id, ok := w.vehicles[key]; ok {
		return id, nil
	}
	endpointID, err := w.endpoint(ctx, feedURL)
	if err != nil {
		return 0, err
	}
	id, err := ensureVehicle(ctx, w.q, endpointID, vehicleID)
	if err != nil {
		return 0, err
	}
	w.vehicles[key] = id
	return id, nil
}

func (w *vehicleTickWriter) agency(ctx context.Context, agency models.ObaServer) (int64, error) {
	if id, ok := w.agencies[agency.AgencyID]; ok {
		return id, nil
	}
	id, err := ensureAgency(ctx, w.q, w.serverID, w.server.ServerName, agency.AgencyID, agency.AgencyName)
	if err != nil {
		return 0, err
	}
	w.agencies[agency.AgencyID] = id
	return id, nil
}

// evidenceFor returns the vehicle's evidence row for this tick, inserting it
// the first time.
func (w *vehicleTickWriter) evidenceFor(ctx context.Context, obs vehicleObservation) (int64, error) {
	key := vehicleEvidenceKey{obs.feedURL, obs.vehicleID}
	if id, ok := w.evidence[key]; ok {
		return id, nil
	}
	responseID, err := w.response(ctx, obs.feedURL)
	if err != nil {
		return 0, err
	}
	vehicleID, err := w.vehicle(ctx, obs.feedURL, obs.vehicleID)
	if err != nil {
		return 0, err
	}
	params := evidenceParams(obs, w.feeds[obs.feedURL].observedAt, w.h.scheduleStore())
	params.VehiclePositionResponseID = responseID
	params.VehicleEntityID = &vehicleID
	params.GtfsStaticVersionID = w.staticVersionID
	if obs.facts.attributionReason == "" && obs.facts.agency.AgencyID != "" {
		agencyID, err := w.agency(ctx, obs.facts.agency)
		if err != nil {
			return 0, err
		}
		params.AgencyID = &agencyID
	}
	if w.needsBox[key] {
		params.BoundingBoxVersionID = obs.facts.boxVersionID
	}
	id, err := w.q.InsertVehiclePositionEvidence(ctx, params)
	if err != nil {
		return 0, fmt.Errorf("server %q: insert evidence for vehicle %q: %w", w.server.ServerName, obs.vehicleID, err)
	}
	w.evidence[key] = id
	return id, nil
}

// scheduleStore returns the compiled schedules, or nil before SetStaticStore.
func (h *HistoryService) scheduleStore() *gtfs.ScheduleStore {
	if h.staticStore == nil {
		return nil
	}
	return h.staticStore.ScheduleStore()
}

// evidenceParams fills the evidence columns that come from the vehicle
// itself. The caller sets the identity, version and agency columns.
// computed_speed_mps and speed_discrepancy_ratio stay null: VehicleLastSeen
// keeps when a speed was computed, not its value.
func evidenceParams(obs vehicleObservation, observedAt time.Time, schedules *gtfs.ScheduleStore) dbgen.InsertVehiclePositionEvidenceParams {
	vehicle := obs.facts.vehicle
	params := dbgen.InsertVehiclePositionEvidenceParams{
		VehicleSourceTime: vehicle.Timestamp,
		Latitude:          obs.facts.latitude,
		Longitude:         obs.facts.longitude,
		PositionValid:     obs.positionReason == "",
	}
	if obs.positionReason != "" {
		params.PositionValidationReason = nonEmpty(string(obs.positionReason))
	}
	if obs.facts.attributionReason != "" {
		params.AttributionReason = nonEmpty(string(obs.facts.attributionReason))
	}
	if vehicle.Position != nil {
		params.ReportedSpeedMps = float32Ptr(vehicle.Position.Speed)
	}
	if vehicle.CurrentStatus != nil {
		status := int16(*vehicle.CurrentStatus)
		params.CurrentStatus = &status
	}
	if vehicle.Trip != nil {
		fillTripEvidence(&params, vehicle.Trip.ID)
	}
	fillScheduleEvidence(&params, obs, observedAt, schedules)
	return params
}

// fillTripEvidence copies the realtime trip descriptor into the evidence.
func fillTripEvidence(params *dbgen.InsertVehiclePositionEvidenceParams, trip remoteGtfs.TripID) {
	params.TripID = nonEmpty(trip.ID)
	params.RouteID = nonEmpty(trip.RouteID)
	if trip.HasStartDate {
		y, m, d := trip.StartDate.Date()
		params.TripStartDate = pgtype.Date{Time: time.Date(y, m, d, 0, 0, 0, 0, time.UTC), Valid: true}
	}
	if trip.HasStartTime {
		seconds := int32(trip.StartTime / time.Second)
		params.TripStartTimeSeconds = &seconds
	}
	relationship := trip.ScheduleRelationship.String()
	params.ScheduleRelationship = &relationship
}

// fillScheduleEvidence classifies the trip against the attributed agency's
// compiled schedule. The columns stay null when the vehicle is unattributed
// or no schedule is available.
func fillScheduleEvidence(params *dbgen.InsertVehiclePositionEvidenceParams, obs vehicleObservation, observedAt time.Time, schedules *gtfs.ScheduleStore) {
	agency := obs.facts.agency
	if schedules == nil || obs.facts.attributionReason != "" || agency.AgencyID == "" {
		return
	}
	descriptor := gtfs.TripDescriptor{TripID: obs.tripID, StartDate: obs.tripStartDate}
	if trip := obs.facts.vehicle.Trip; trip != nil && trip.ID.HasStartTime {
		descriptor.StartTime = gtfsClock(trip.ID.StartTime)
	}
	classification := schedules.ClassifyTrip(models.ServerKey(agency.ObaBaseURL, agency.AgencyID), descriptor, observedAt)
	if !classification.ScheduleAvailable {
		return
	}
	state := string(classification.State)
	reason := string(classification.Reason)
	params.ExpectedScheduleState = &state
	params.ExpectedScheduleReason = &reason
	if !classification.WindowStart.IsZero() {
		start, end := classification.WindowStart.UTC(), classification.WindowEnd.UTC()
		params.ExpectedWindowStart = &start
		params.ExpectedWindowEnd = &end
	}
}

// gtfsClock formats a duration after service-day midnight as GTFS HH:MM:SS,
// where the hour may exceed 23.
func gtfsClock(d time.Duration) string {
	seconds := int64(d / time.Second)
	return fmt.Sprintf("%02d:%02d:%02d", seconds/3600, seconds%3600/60, seconds%60)
}

func float32Ptr(v *float32) *float64 {
	if v == nil {
		return nil
	}
	f := float64(*v)
	return &f
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
