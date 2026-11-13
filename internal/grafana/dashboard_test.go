package grafana

import (
	"os"
	"path/filepath"
	"testing"
)

func writeDashboard(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "dashboard.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestExtractExpressions(t *testing.T) {
	path := writeDashboard(t, `{
		"panels": [
			{
				"id": 1,
				"title": "Parent panel",
				"datasource": {"type": "prometheus"},
				"targets": [
					{"expr": "up"},
					{"expr": "rate(http_requests_total[5m])"},
					{"expr": "ignored", "datasource": {"type": "loki"}}
				],
				"panels": [
					{
						"id": 2,
						"title": "Nested panel",
						"targets": [
							{"expr": "process_cpu_seconds_total"}
						]
					}
				]
			},
			{
				"id": 3,
				"title": "Non-Prometheus panel",
				"datasource": {"type": "loki"},
				"targets": [
					{"expr": "ignored"}
				]
			},
			{
				"id": 4,
				"title": "Target datasource override",
				"datasource": {"type": "loki"},
				"targets": [
					{"expr": "my_metric", "datasource": {"type": "prometheus"}}
				]
			}
		]
	}`)

	got, err := ExtractExpressions(path)
	if err != nil {
		t.Fatalf("ExtractExpressions() error = %v", err)
	}

	if len(got) != 4 {
		t.Fatalf("got %d expressions, want 4: %+v", len(got), got)
	}

	if got[0].Expr != "up" || got[0].Target != 0 {
		t.Errorf("unexpected first expression: %+v", got[0])
	}

	if got[1].Expr != "rate(http_requests_total[5m])" {
		t.Errorf("unexpected second expression: %+v", got[1])
	}

	if got[2].Expr != "process_cpu_seconds_total" ||
		got[2].PanelID != 2 {
		t.Errorf("nested panel expression not extracted correctly: %+v", got[2])
	}

	if got[3].Expr != "my_metric" || got[3].PanelID != 4 {
		t.Errorf("target datasource override not respected: %+v", got[3])
	}
}

func TestExtractExpressionsInvalidJSON(t *testing.T) {
	path := writeDashboard(t, `{invalid json`)

	_, err := ExtractExpressions(path)
	if err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}

func TestExtractExpressionsMissingFile(t *testing.T) {
	_, err := ExtractExpressions(filepath.Join(t.TempDir(), "missing.json"))
	if err == nil {
		t.Fatal("expected an error for missing file")
	}
}

func TestExtractExpressionsDatasourceName(t *testing.T) {
	path := writeDashboard(t, `{
		"panels": [
			{
				"id": 1,
				"title": "Name-based datasource",
				"datasource": {"name": "Prometheus"},
				"targets": [
					{"expr": "up"}
				]
			},
			{
				"id": 2,
				"title": "String datasource",
				"targets": [
					{"expr": "process_cpu_seconds_total", "datasource": "Prometheus"}
				]
			}
		]
	}`)

	got, err := ExtractExpressions(path)
	if err != nil {
		t.Fatalf("ExtractExpressions() error = %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d expressions, want 2: %+v", len(got), got)
	}
}

func TestRepositoryDashboards(t *testing.T) {
	paths, err := filepath.Glob("../../grafana/dashboards/*.json")
	if err != nil {
		t.Fatal(err)
	}

	if len(paths) == 0 {
		t.Fatal("no dashboard JSON files found")
	}

	total := 0

	for _, path := range paths {
		expressions, err := ExtractExpressions(path)
		if err != nil {
			t.Errorf("failed to extract %s: %v", path, err)
			continue
		}

		t.Logf("%s: %d PromQL expressions", filepath.Base(path), len(expressions))

		if len(expressions) == 0 {
			t.Errorf("%s: no PromQL expressions extracted", path)
		}

		total += len(expressions)
	}

	t.Logf("Total extracted expressions: %d", total)
}
