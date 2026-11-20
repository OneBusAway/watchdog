package grafana

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Match the shared heartbeat age filter, including optional grouping.
var collectionHeartbeatGate = regexp.MustCompile(`\band\s+on\s*\(\s*\)\s*(?:\(\s*)*time\s*\(\s*\)\s*-\s*watchdog_collection_last_completed_timestamp_seconds\s*(?:\)\s*)*<`)

func TestCollectionHeartbeatGate(t *testing.T) {
	const age = "time() - watchdog_collection_last_completed_timestamp_seconds"
	cases := []struct {
		name  string
		query string
		gated bool
	}{
		{"age filter", "up and on() ((" + age + ") < 90)", true},
		{"spaced age filter", "up and on ( ) ( " + age + " < 90 )", true},
		{"age display", age, false},
		{"age comparison display", "(" + age + ") < 90", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := collectionHeartbeatGate.MatchString(tc.query); got != tc.gated {
				t.Errorf("heartbeat gate match = %t, want %t", got, tc.gated)
			}
		})
	}
}

func TestPanelFreshnessExpressions(t *testing.T) {
	const suitePath = "../../prometheus/tests/panel_freshness_test.yml"
	data, err := os.ReadFile(suitePath)
	if err != nil {
		t.Fatal(err)
	}
	var suite struct {
		Tests []struct {
			Expressions []struct {
				Expr    string      `yaml:"expr"`
				Samples []yaml.Node `yaml:"exp_samples"`
			} `yaml:"promql_expr_test"`
		} `yaml:"tests"`
	}
	if err := yaml.Unmarshal(data, &suite); err != nil {
		t.Fatalf("decode %s: %v", suitePath, err)
	}

	type expectations struct {
		withSamples    bool
		withoutSamples bool
	}
	tested := make(map[string]expectations)
	for _, group := range suite.Tests {
		for _, query := range group.Expressions {
			coverage := tested[query.Expr]
			if len(query.Samples) == 0 {
				coverage.withoutSamples = true
			} else {
				coverage.withSamples = true
			}
			tested[query.Expr] = coverage
		}
	}

	expressions, err := ExtractExpressions("../../grafana/dashboards/watchdog_metrics_dashboard.json")
	if err != nil {
		t.Fatal(err)
	}

	variables := strings.NewReplacer("$agency_id", ".*", "$server_url", ".*")
	type panelTarget struct {
		panelID int
		refID   string
	}
	gated := make(map[panelTarget]struct{})
	for _, expression := range expressions {
		if !collectionHeartbeatGate.MatchString(expression.Expr) {
			continue
		}
		gated[panelTarget{expression.PanelID, expression.RefID}] = struct{}{}
		coverage, ok := tested[variables.Replace(expression.Expr)]
		switch {
		case !ok:
			t.Errorf("panel %d target %q: expression missing from %s", expression.PanelID, expression.RefID, suitePath)
		case !coverage.withSamples || !coverage.withoutSamples:
			t.Errorf("panel %d target %q: %s must test both a sample result and an empty result", expression.PanelID, expression.RefID, suitePath)
		}
	}
	// Pin existing targets so deleting a target or its heartbeat cannot
	// remove it from the check. Ref IDs survive reordering within the panel.
	required := []panelTarget{
		{301, "A"}, {301, "B"}, {301, "C"}, {301, "F"}, {301, "G"}, {301, "H"}, {301, "I"},
		{501, "A"}, {503, "A"}, {503, "B"},
	}
	for _, target := range required {
		if _, ok := gated[target]; !ok {
			t.Errorf("panel %d target %q: required freshness-gated expression missing from dashboard", target.panelID, target.refID)
		}
	}
	t.Logf("checked %d freshness-gated dashboard targets", len(gated))
}
