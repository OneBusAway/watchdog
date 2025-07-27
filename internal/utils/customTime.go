package utils

import (
	"encoding/json"
	"time"
)

const YYYYMMDD = "20060102"

type CustomTime time.Time

func (d CustomTime) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Time(d).Format(YYYYMMDD))
}

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

func (d CustomTime) Time() time.Time {
	return time.Time(d)
}
