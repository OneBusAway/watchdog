package grafana

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateExpression(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{
			name: "valid instant vector",
			expr: "up",
		},
		{
			name: "valid rate expression",
			expr: "rate(http_requests_total[5m])",
		},
		{
			name: "valid vector matching",
			expr: "up * on (instance) group_left (version) node_info",
		},
		{
			name:    "invalid expression",
			expr:    "rate(http_requests_total[)",
			wantErr: true,
		},
		{
			name: "Grafana rate interval macro",
			expr: "rate(http_requests_total[$__rate_interval])",
		},
		{
			name: "Grafana rate interval macro with curly braces",
			expr: "rate(http_requests_total[${__rate_interval}])",
		},
		{
			name: "Grafana interval macro in range selector",
			expr: "rate(http_requests_total[$__interval])",
		},
		{
			name: "Grafana range macro in range selector",
			expr: "rate(http_requests_total[$__range])",
		},
		{
			name: "Grafana range in seconds macro",
			expr: "http_requests_total * $__range_s",
		},
		{
			name: "Grafana range in seconds macro with curly braces and unit",
			expr: "rate(http_requests_total[${__range_s}s])",
		},
		{
			name: "Grafana range in milliseconds macro",
			expr: "http_requests_total * $__range_ms",
		},
		{
			name: "Grafana interval in milliseconds macro",
			expr: "http_requests_total * $__interval_ms",
		},
		{
			name: "Grafana interval macro in offset",
			expr: "http_requests_total offset $__interval",
		},
		{
			name: "Grafana variable in range selector",
			expr: "rate(http_requests_total[$interval])",
		},
		{
			name: "Grafana variable with curly braces in range selector",
			expr: "rate(http_requests_total[${interval}])",
		},
		{
			name: "Grafana variable with legacy brackets in range selector",
			expr: "rate(http_requests_total[[[interval]]])",
		},
		{
			name: "Grafana variable with unit in range selector",
			expr: "rate(up[${hours}h])",
		},
		{
			name: "Grafana variable in offset",
			expr: "http_requests_total offset $offset",
		},
		{
			name: "Grafana variable with curly braces in offset",
			expr: "http_requests_total offset ${offset}",
		},
		{
			name: "Grafana variable with legacy brackets in offset",
			expr: "http_requests_total offset [[offset]]",
		},
		{
			name: "Grafana variable in negative offset",
			expr: "http_requests_total offset -$offset",
		},
		{
			name: "Grafana variable in negative offset with space",
			expr: "http_requests_total offset - $offset",
		},
		{
			name: "Grafana variable in positive offset",
			expr: "http_requests_total offset +$offset",
		},
		{
			name: "Grafana variable with unit in offset",
			expr: "http_requests_total offset ${hours}h",
		},
		{
			name: "Grafana variable in subquery range and resolution",
			expr: "rate(http_requests_total[5m])[$range:$resolution]",
		},
		{
			name: "Grafana legacy variable in subquery range",
			expr: "up[[[window]]:1m]",
		},
		{
			name: "Grafana legacy variable in subquery resolution",
			expr: "up[1h:[[step]]]",
		},
		{
			name: "Grafana template variable in label matcher",
			expr: `up{job="$job", instance="$instance"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateExpression(Expression{
				Dashboard:  "test.json",
				PanelTitle: "Test panel",
				PanelID:    1,
				Target:     0,
				Expr:       tt.expr,
			})

			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateExpression() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidationErrorIncludesContext(t *testing.T) {
	expression := Expression{
		Dashboard:  "example.json",
		PanelTitle: "Request rate",
		PanelID:    12,
		Target:     1,
		Expr:       "rate(metric[)",
	}

	err := ValidateExpression(expression)
	if err == nil {
		t.Fatal("expected invalid expression error")
	}

	for _, expected := range []string{
		"example.json",
		"Request rate",
		"ID: 12",
		"target 1",
		"rate(metric[)",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("error %q does not contain %q", err, expected)
		}
	}
}

func TestValidateExpressions(t *testing.T) {
	expressions := []Expression{
		{Expr: "up"},
		{Expr: "rate(metric[)"},
		{Expr: "sum by (job) (up)"},
		{Expr: "invalid expression )"},
	}

	got := ValidateExpressions(expressions)

	if len(got) != 2 {
		t.Fatalf("got %d validation errors, want 2", len(got))
	}
}

func TestRepositoryDashboardsPromQL(t *testing.T) {
	paths, err := filepath.Glob("../../grafana/dashboards/*.json")
	if err != nil {
		t.Fatal(err)
	}

	if len(paths) == 0 {
		t.Fatal("no dashboard JSON files found")
	}

	for _, path := range paths {
		expressions, err := ExtractExpressions(path)
		if err != nil {
			t.Fatalf("extract %s: %v", path, err)
		}

		for _, validationErr := range ValidateExpressions(expressions) {
			t.Error(validationErr)
		}
	}
}
