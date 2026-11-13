package gtfs

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"strings"

	remoteGtfs "github.com/OneBusAway/go-gtfs"
	gtfscsv "github.com/OneBusAway/go-gtfs/csv"
	gtfsrt "github.com/OneBusAway/go-gtfs/proto"
)

type duplicateIDError struct {
	kind string
	id   string
}

func (e *duplicateIDError) Error() string {
	return fmt.Sprintf("merged feed contains duplicate %s %q", e.kind, e.id)
}

// validateStaticIDs reads primary keys before go-gtfs can collapse them into
// its lookup maps. References may repeat: for example a service can have many
// calendar_dates rows and a trip can have many stop_times rows.
func validateStaticIDs(data []byte) error {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	keys := map[string]string{
		"agency.txt": "agency_id", "stops.txt": "stop_id",
		"routes.txt": "route_id", "trips.txt": "trip_id", "calendar.txt": "service_id",
	}
	files := make(map[string]bool)
	for _, file := range reader.File {
		name := file.Name
		key, relevant := keys[name]
		if !relevant {
			continue
		}
		if files[name] {
			return &duplicateIDError{kind: "table", id: name}
		}
		files[name] = true
		if err := validateStaticTableIDs(file, key); err != nil {
			return err
		}
	}
	return nil
}

func validateStaticTableIDs(file *zip.File, key string) error {
	body, err := file.Open()
	if err != nil {
		return err
	}
	defer body.Close()
	reader := gtfscsv.BOMAwareCSVReader(body)
	header, err := reader.Read()
	if err == io.EOF {
		return nil // Required tables and columns are checked by the parsers.
	}
	if err != nil {
		return err
	}
	column := -1
	for i, name := range header {
		if strings.TrimPrefix(strings.TrimSpace(name), "\ufeff") == key {
			column = i
			break
		}
	}
	if column < 0 {
		return nil // A sole agency may omit agency_id.
	}
	reader.ReuseRecord = true
	seen := make(map[string]struct{})
	for {
		row, err := reader.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if column >= len(row) {
			return fmt.Errorf("%s row has no %s column", file.Name, key)
		}
		id := row[column]
		if id == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			return &duplicateIDError{kind: key, id: id}
		}
		seen[strings.Clone(id)] = struct{}{}
	}
}

func validateRealtimeIDs(feed *gtfsrt.FeedMessage) error {
	entities := make(map[string]struct{})
	vehicles := make(map[string]struct{})
	for _, entity := range feed.Entity {
		if id := entity.GetId(); id != "" {
			if _, duplicate := entities[id]; duplicate {
				return &duplicateIDError{kind: "entity_id", id: id}
			}
			entities[id] = struct{}{}
		}
		if id := entity.GetVehicle().GetVehicle().GetId(); id != "" {
			if _, duplicate := vehicles[id]; duplicate {
				return &duplicateIDError{kind: "vehicle_id", id: id}
			}
			vehicles[id] = struct{}{}
		}
	}
	return nil
}

// validateParsedStaticIDs also protects callers publishing an already parsed
// bundle. Raw validation remains necessary because parser maps can hide rows.
func validateParsedStaticIDs(bundle *remoteGtfs.Static) error {
	seen := make(map[string]map[string]bool)
	check := func(kind, id string) error {
		if id == "" {
			return nil
		}
		if seen[kind] == nil {
			seen[kind] = make(map[string]bool)
		}
		if seen[kind][id] {
			return &duplicateIDError{kind: kind, id: id}
		}
		seen[kind][id] = true
		return nil
	}
	for _, agency := range bundle.Agencies {
		if err := check("agency_id", agency.Id); err != nil {
			return err
		}
	}
	for _, stop := range bundle.Stops {
		if err := check("stop_id", stop.Id); err != nil {
			return err
		}
	}
	for _, route := range bundle.Routes {
		if err := check("route_id", route.Id); err != nil {
			return err
		}
	}
	for _, trip := range bundle.Trips {
		if err := check("trip_id", trip.ID); err != nil {
			return err
		}
	}
	return nil
}
