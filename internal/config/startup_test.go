package config

import (
	"errors"
	"testing"

	"watchdog.onebusaway.org/internal/models"
)

func TestShouldExitOnStartupLoad(t *testing.T) {
	one := []models.ObaServer{{ServerName: "a"}}
	boom := errors.New("boom")
	tests := []struct {
		name    string
		remote  bool
		servers []models.ObaServer
		err     error
		want    bool
	}{
		{"file ok", false, one, nil, false},
		{"file error", false, nil, boom, true},
		{"file empty", false, nil, nil, true},
		{"url ok", true, one, nil, false},
		{"url error survives", true, nil, boom, false},
		{"url empty survives", true, nil, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldExitOnStartupLoad(tt.remote, tt.servers, tt.err); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
