package investigation

import (
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// apiTime is a point in time in a response. It always encodes in UTC with
// time.RFC3339Nano, for example "2026-10-24T14:05:30Z", whatever zone the
// value carries. Use *apiTime for a field that can be null: a nil pointer
// encodes as null, and the field is never omitted.
type apiTime time.Time

func (t apiTime) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Time(t).UTC().Format(time.RFC3339Nano) + `"`), nil
}

// newAPITime converts a NOT NULL timestamp column.
func newAPITime(t time.Time) apiTime {
	return apiTime(t)
}

// newOptionalAPITime converts a nullable timestamp column (sqlc generates
// *time.Time): nil stays nil and encodes as null.
func newOptionalAPITime(t *time.Time) *apiTime {
	if t == nil {
		return nil
	}
	v := apiTime(*t)
	return &v
}

// apiDate is a calendar date with no time of day, such as a trip's service
// date. It encodes as "YYYY-MM-DD".
type apiDate time.Time

func (d apiDate) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Time(d).Format(time.DateOnly) + `"`), nil
}

// newAPIDate converts a date column (sqlc generates pgtype.Date). A null or
// infinite date gives nil, which encodes as null.
func newAPIDate(d pgtype.Date) *apiDate {
	if !d.Valid || d.InfinityModifier != pgtype.Finite {
		return nil
	}
	v := apiDate(d.Time)
	return &v
}
