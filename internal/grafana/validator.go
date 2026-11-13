package grafana

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/prometheus/prometheus/promql/parser"
)

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

var (
	// Matches legacy variable syntax [[var]] and converts to $var
	legacyVarRegex = regexp.MustCompile(`\[\[([a-zA-Z_][a-zA-Z0-9_]*)\]\]`)

	// Matches offset position with optional sign (+ or -), optional whitespace,
	// and optional attached duration unit suffix (e.g. offset $var, offset - $var, offset +${hours}h).
	offsetRegex = regexp.MustCompile(`(?i)\boffset\s*([+-])?\s*(?:\$\{[a-zA-Z_][a-zA-Z0-9_]*(?::[a-zA-Z0-9_]+)?\}|\$[a-zA-Z_][a-zA-Z0-9_]*)([a-zA-Z]+)?`)

	// Matches range brackets: [ ... ]
	rangeSelectorRegex = regexp.MustCompile(`\[([^\[\]]+)\]`)

	// Matches variables inside range selectors/subqueries, including attached duration units:
	// e.g. $var, ${var}, ${hours}h
	variableInDurationRegex = regexp.MustCompile(`(?:\$\{[a-zA-Z_][a-zA-Z0-9_]*(?::[a-zA-Z0-9_]+)?\}|\$[a-zA-Z_][a-zA-Z0-9_]*)([a-zA-Z]+)?`)
)

// normalizeGrafanaExpression replaces supported Grafana macros and template
// variables with representative PromQL-compatible values for syntax validation.
func normalizeGrafanaExpression(expression string) string {
	// 1. Built-in Grafana macros.
	// Longer prefixes are replaced first to prevent partial replacement
	// (e.g. $__range_s before $__range).
	macroReplacements := []struct {
		macro       string
		replacement string
	}{
		// Rate interval:
		{"$__rate_interval", "5m"},
		{"${__rate_interval}", "5m"},
		{"[[__rate_interval]]", "5m"},

		// Milliseconds:
		{"$__range_ms", "300000"},
		{"${__range_ms}", "300000"},
		{"[[__range_ms]]", "300000"},
		{"$__interval_ms", "300000"},
		{"${__interval_ms}", "300000"},
		{"[[__interval_ms]]", "300000"},

		// Seconds:
		{"$__range_s", "300"},
		{"${__range_s}", "300"},
		{"[[__range_s]]", "300"},

		// Interval:
		{"$__interval", "5m"},
		{"${__interval}", "5m"},
		{"[[__interval]]", "5m"},

		// Range:
		{"$__range", "5m"},
		{"${__range}", "5m"},
		{"[[__range]]", "5m"},
	}

	for _, mr := range macroReplacements {
		expression = strings.ReplaceAll(expression, mr.macro, mr.replacement)
	}

	// Normalize legacy [[var]] into $var
	expression = legacyVarRegex.ReplaceAllStringFunc(expression, func(m string) string {
		return "$" + m[2:len(m)-2]
	})

	// 2. Variables in offset position:
	expression = offsetRegex.ReplaceAllStringFunc(expression, func(match string) string {
		sub := offsetRegex.FindStringSubmatch(match)
		sign := sub[1]
		unit := sub[2]
		dur := "5m"
		if unit != "" {
			dur = "5" + unit
		}
		if sign != "" {
			return "offset " + sign + dur
		}
		return "offset " + dur
	})

	// 3. Variables inside range selectors: [$var], [${var}], [${hours}h], [$range:$resolution], etc.
	expression = rangeSelectorRegex.ReplaceAllStringFunc(expression, func(match string) string {
		inner := match[1 : len(match)-1]
		replaced := variableInDurationRegex.ReplaceAllStringFunc(inner, func(vMatch string) string {
			vSub := variableInDurationRegex.FindStringSubmatch(vMatch)
			unit := vSub[1]
			if unit != "" {
				return "5" + unit
			}
			return "5m"
		})
		return "[" + replaced + "]"
	})

	return expression
}
