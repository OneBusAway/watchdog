package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// ObaApiStatus API Status (up/down)
	ObaApiStatus = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_api_status",
			Help: "Status of the OneBusAway API Server (0 = not working, 1 = working)",
		},
		[]string{"server_id", "server_url"},
	)
)

var (
	BundleEarliestExpirationGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_bundle_days_until_earliest_expiration",
		Help: "Number of days until the earliest GTFS bundle expiration",
	}, []string{"server_id"})

	BundleLatestExpirationGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gtfs_bundle_days_until_latest_expiration",
		Help: "Number of days until the latest GTFS bundle expiration",
	}, []string{"server_id"})
)

var (
	AgenciesInStaticGtfs = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "oba_agencies_in_static_gtfs",
		Help: "Number of agencies in the static GTFS file",
	}, []string{"server_id"})

	AgenciesInCoverageEndpoint = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "oba_agencies_in_coverage_endpoint",
		Help: "Number of agencies in the agencies-with-coverage endpoint",
	}, []string{"server_id"})

	AgenciesMatch = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "oba_agencies_match",
		Help: "Whether the number of agencies in the static GTFS file matches the agencies-with-coverage endpoint (1 = match, 0 = no match)",
	}, []string{"server_id"})
)

var (
	RealtimeVehiclePositions = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "realtime_vehicle_positions_count_gtfs_rt",
		Help: "Number of realtime vehicle positions in the GTFS-RT feed",
	}, []string{"gtfs_rt_url", "server_id"})

	VehicleCountAPI = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vehicle_count_api",
		Help: "Number of vehicles in the API response",
	}, []string{"agency_id", "server_id"})

	VehicleCountMatch = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vehicle_count_match",
		Help: "Whether the number of vehicles in the API response matches the number of vehicles in the static GTFS-RT file (1 = match, 0 = no match)",
	}, []string{"agency_id", "server_id"})

	VehicleReportInterval = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vehicle_position_report_interval_seconds",
		Help: "Time in seconds since each vehicle last reported a GTFS-RT position",
	}, []string{"vehicle_id", "server_id"})

	VehicleReportCount = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "vehicle_report_total",
		Help: "Total number of GTFS-RT updates received from each vehicle",
	}, []string{"vehicle_id", "server_id"})

	InvalidVehicleCoordinatesGauge = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gtfs_rt_invalid_vehicle_coordinates",
			Help: "Current number of GTFS-RT vehicle positions with invalid coordinates",
		},
		[]string{"server_id"},
	)

	OutOfBoundsVehicleCoordinatesGauge = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gtfs_rt_out_of_bounds_vehicle_coordinates",
			Help: "Current number of GTFS-RT vehicle positions outside bounding box",
		},
		[]string{"server_id"},
	)
)

// OBA REST API 2.6.0 >= Metrics
var (
	ObaAgenciesWithCoverage = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_agencies_with_coverage_count",
			Help: "Number of agencies with coverage",
		},
		[]string{"server"},
	)

	ObaRealtimeRecords = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_realtime_records_total",
			Help: "Total number of realtime records",
		},
		[]string{"server", "agency"},
	)

	ObaRealtimeTripsMatched = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_realtime_trips_matched_count",
			Help: "Number of matched realtime trips",
		},
		[]string{"server", "agency"},
	)

	ObaRealtimeTripsUnmatched = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_realtime_trips_unmatched_count",
			Help: "Number of unmatched realtime trips",
		},
		[]string{"server", "agency"},
	)

	ObaScheduledTrips = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_scheduled_trips_count",
			Help: "Number of scheduled trips",
		},
		[]string{"server", "agency"},
	)

	ObaStopsMatched = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_stops_matched_count",
			Help: "Number of matched stops",
		},
		[]string{"server", "agency"},
	)

	ObaStopsUnmatched = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_stops_unmatched_count",
			Help: "Number of unmatched stops",
		},
		[]string{"server", "agency"},
	)

	TripMatchRatio = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "oba_realtime_trip_match_ratio",
		Help: "Ratio of matched realtime trips to total realtime trips",
	}, []string{"server", "agency"})

	StopMatchRatio = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "oba_stop_match_ratio",
		Help: "Ratio of matched stops to total stops",
	}, []string{"server", "agency"})

	ObaTimeSinceUpdate = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_time_since_last_update_seconds",
			Help: "Time since last realtime update in seconds",
		},
		[]string{"server", "agency"},
	)

	ObaUnmatchedStopLocation = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_unmatched_stop_location",
			Help: "Location info of unmatched stops from static GTFS",
		},
		[]string{"server", "agency", "stop_id", "stop_name", "lat", "lon"},
	)

	UnmatchedStopClusterCount = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "oba_unmatched_stop_cluster_count",
			Help: "Number of unmatched stops grouped by station or spatial cluster",
		},
		[]string{"server", "agency", "cluster_id", "cluster_type"},
	)
)
