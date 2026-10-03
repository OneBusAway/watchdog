package metrics

import (
	"sort"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"watchdog.onebusaway.org/internal/gtfs"
	"watchdog.onebusaway.org/internal/models"
	"watchdog.onebusaway.org/internal/utils"
)

type staticAgency struct {
	id   string
	name string
}

// reportTrackedAgencies refreshes inventory metrics for the configured OBA
// servers. Inventory comes from the latest complete static snapshot, not from
// config-entry count or OBA's realtime/metrics API response.
// TODO(#163): compare this static agency set with the agencies reported by
// OBA's metrics API and expose their alignment without replacing this inventory.
func reportTrackedAgencies(servers []models.ObaServer, staticStore *gtfs.StaticStore) {
	AgenciesTrackedCount.Reset()
	AgenciesTrackedInfo.Reset()

	seen := make(map[string]struct{}, len(servers))
	for _, server := range servers {
		serverURL := utils.SanitizeServerURL(server.ObaBaseURL)
		if _, exists := seen[serverURL]; exists {
			continue
		}
		seen[serverURL] = struct{}{}
		reportTrackedAgenciesForServer(server, staticStore)
	}
}

// reportTrackedAgenciesForServer republishes the agency inventory for one OBA
// base URL. It is also called after a static snapshot is atomically published.
func reportTrackedAgenciesForServer(server models.ObaServer, staticStore *gtfs.StaticStore) {
	serverURL := utils.SanitizeServerURL(server.ObaBaseURL)
	serverLabels := prometheus.Labels{"server_url": serverURL}
	AgenciesTrackedCount.DeletePartialMatch(serverLabels)
	AgenciesTrackedInfo.DeletePartialMatch(serverLabels)

	agencies := agenciesFromConfiguredStaticSnapshots(server, staticStore)
	AgenciesTrackedCount.WithLabelValues(server.ServerName, serverURL).Set(float64(len(agencies)))
	for _, agency := range agencies {
		AgenciesTrackedInfo.WithLabelValues(agency.id, agency.name, server.ServerName, serverURL).Set(1)
	}
}

func agenciesFromConfiguredStaticSnapshots(server models.ObaServer, staticStore *gtfs.StaticStore) []staticAgency {
	if staticStore == nil {
		return nil
	}
	configured := staticStore.ConfiguredServers()
	if len(configured) == 0 {
		configured = []models.ObaServer{server}
	}
	byID := make(map[string]string)
	serverURL := utils.SanitizeServerURL(server.ObaBaseURL)
	for _, entry := range configured {
		if utils.SanitizeServerURL(entry.ObaBaseURL) != serverURL {
			continue
		}
		for _, agency := range staticAgenciesForServer(entry, staticStore) {
			if _, exists := byID[agency.id]; !exists {
				byID[agency.id] = agency.name
			}
		}
	}
	agencies := make([]staticAgency, 0, len(byID))
	for id, name := range byID {
		agencies = append(agencies, staticAgency{id: id, name: name})
	}
	sort.Slice(agencies, func(i, j int) bool { return agencies[i].id < agencies[j].id })
	return agencies
}

func staticAgenciesForServer(server models.ObaServer, staticStore *gtfs.StaticStore) []staticAgency {
	if staticStore == nil {
		return nil
	}
	if !server.IsServerScoped() {
		data, ok := staticStore.Get(server.ServerKey())
		if !ok || data == nil {
			return nil
		}
		for _, agency := range data.Agencies {
			if agency.Id == server.AgencyID {
				name := agency.Name
				if name == "" {
					name = server.AgencyName
				}
				return []staticAgency{{id: server.AgencyID, name: name}}
			}
		}
		return nil
	}

	byID := make(map[string]string)
	prefix := models.ServerKeyPrefix(server.ObaBaseURL)
	staticStore.Range(func(serverKey string, data *models.StaticData) bool {
		if data == nil || !strings.HasPrefix(serverKey, prefix) {
			return true
		}
		for _, agency := range data.Agencies {
			if agency.Id == "" {
				continue
			}
			if _, exists := byID[agency.Id]; !exists {
				byID[agency.Id] = agency.Name
			}
		}
		return true
	})

	agencies := make([]staticAgency, 0, len(byID))
	for id, name := range byID {
		agencies = append(agencies, staticAgency{id: id, name: name})
	}
	return agencies
}
