package utils

import (
	"encoding/json"
	"time"
)

const YYYYMMDD = "20060102" // Date format used for (un)marshaling CustomTime

// CustomTime wraps time.Time to enable custom JSON marshaling/unmarshaling
// using the "YYYYMMDD" format (e.g., "20250807").
type CustomTime time.Time

// MarshalJSON serializes the CustomTime to JSON in "YYYYMMDD" format.
func (d CustomTime) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Time(d).Format(YYYYMMDD))
}

// UnmarshalJSON parses a "YYYYMMDD" formatted string into a CustomTime.
func (d *CustomTime) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	t, err := time.Parse(YYYYMMDD, s)
	if err != nil {
		return err
	}
	*d = CustomTime(t)
	return nil
}

// Time returns the underlying time.Time value of the CustomTime.
func (d CustomTime) Time() time.Time {
	return time.Time(d)
}
