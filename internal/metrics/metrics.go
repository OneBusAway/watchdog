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
)

var (
	ScheduleRouteForStop = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "schedule_route_stop",
		Help: "Gives the number of scheduled routes for the given stop",
	}, []string{"server_id", "stop_id"})
)

var (
	PredictArrivalRate = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "predict_arrival_ratio",
			Help: "Ratio of predicted to total arrivals",
		},
		[]string{"agency_id", "stop_id"},
	)

	PerfectPredictionRate = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "perfect_prediction_rate",
			Help: "Percentage of perfect predictions",
		},
		[]string{"agency_id", "stop_id"},
	)

	AlertLowDataAvailability = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "alert_low_data_availability",
			Help: "1 if real-time data availability < 50%",
		},
		[]string{"agency_id", "stop_id"},
	)

	AlertPoorPredictionAccuracy = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "alert_poor_prediction_accuracy",
			Help: "1 if perfect prediction accuracy < 10%",
		},
		[]string{"agency_id", "stop_id"},
	)
)

func init() {
	prometheus.MustRegister(PredictArrivalRate)
	prometheus.MustRegister(PerfectPredictionRate)
	prometheus.MustRegister(AlertLowDataAvailability)
	prometheus.MustRegister(AlertPoorPredictionAccuracy)
}
