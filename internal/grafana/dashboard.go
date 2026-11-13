package grafana

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Dashboard struct {
	Panels []Panel `json:"panels"`
}

type Panel struct {
	ID         int             `json:"id"`
	Title      string          `json:"title"`
	Datasource json.RawMessage `json:"datasource"`
	Targets    []Target        `json:"targets"`
	Panels     []Panel         `json:"panels"`
}

type Target struct {
	Expr       string          `json:"expr"`
	Datasource json.RawMessage `json:"datasource"`
}

type Expression struct {
	Dashboard  string
	PanelTitle string
	PanelID    int
	Target     int
	Expr       string
}

// ExtractExpressions loads a dashboard and extracts Prometheus target expressions.
func ExtractExpressions(path string) ([]Expression, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read dashboard %q: %w", path, err)
	}

	var dashboard Dashboard
	if err := json.Unmarshal(data, &dashboard); err != nil {
		return nil, fmt.Errorf("decode dashboard %q: %w", path, err)
	}

	var expressions []Expression
	dashboardName := filepath.Base(path)

	var walk func([]Panel, json.RawMessage)
	walk = func(panels []Panel, inheritedDatasource json.RawMessage) {
		for _, panel := range panels {
			panelDatasource := panel.Datasource
			if len(panelDatasource) == 0 || string(panelDatasource) == "null" {
				panelDatasource = inheritedDatasource
			}

			for i, target := range panel.Targets {
				expr := strings.TrimSpace(target.Expr)
				if expr == "" {
					continue
				}

				datasource := target.Datasource
				if len(datasource) == 0 || string(datasource) == "null" {
					datasource = panelDatasource
				}

				if !isPrometheusDatasource(datasource) {
					continue
				}

				expressions = append(expressions, Expression{
					Dashboard:  dashboardName,
					PanelTitle: panel.Title,
					PanelID:    panel.ID,
					Target:     i,
					Expr:       expr,
				})
			}

			walk(panel.Panels, panelDatasource)
		}
	}

	walk(dashboard.Panels, nil)

	return expressions, nil
}

func isPrometheusDatasource(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}

	var datasource struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}

	if err := json.Unmarshal(raw, &datasource); err == nil {
		return strings.EqualFold(datasource.Type, "prometheus") ||
			strings.EqualFold(datasource.Name, "Prometheus")
	}

	var name string
	if err := json.Unmarshal(raw, &name); err == nil {
		return strings.EqualFold(name, "Prometheus")
	}

	return false
}
