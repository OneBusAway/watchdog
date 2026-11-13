package grafana

import (
	"errors"
	"fmt"
	"strings"

	"github.com/prometheus/prometheus/promql/parser"
)

const grafanaRateInterval = "$__rate_interval"

type ValidationError struct {
	Expression Expression
	Err        error
}

func (e ValidationError) Error() string {
	return fmt.Sprintf(
		"dashboard %q, panel %q (ID: %d), target %d: invalid PromQL %q: %v",
		e.Expression.Dashboard,
		e.Expression.PanelTitle,
		e.Expression.PanelID,
		e.Expression.Target,
		e.Expression.Expr,
		e.Err,
	)
}

// ValidateExpression checks whether an expression is valid PromQL syntax.
func ValidateExpression(expression Expression) error {
	normalized := normalizeGrafanaExpression(expression.Expr)

	p := parser.NewParser(parser.Options{})
	if _, err := p.ParseExpr(normalized); err != nil {
		return ValidationError{
			Expression: expression,
			Err:        err,
		}
	}

	return nil
}

// ValidateExpressions validates all extracted expressions and returns
// contextual errors for expressions that fail parsing.
func ValidateExpressions(expressions []Expression) []ValidationError {
	var validationErrors []ValidationError

	for _, expression := range expressions {
		if err := ValidateExpression(expression); err != nil {
			var validationErr ValidationError
			if errors.As(err, &validationErr) {
				validationErrors = append(validationErrors, validationErr)
			}
		}
	}

	return validationErrors
}

// normalizeGrafanaExpression replaces supported Grafana macros with
// representative PromQL-compatible values for syntax validation.
func normalizeGrafanaExpression(expression string) string {
	return strings.ReplaceAll(expression, grafanaRateInterval, "5m")
}
