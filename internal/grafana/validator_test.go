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
			name: "Grafana template variable in label matcher",
			expr: `up{job="$job"}`,
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
