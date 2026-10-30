package investigation

import "log/slog"

// AcceptedTokens returns the bearer tokens the Investigation API accepts,
// given the values of INVESTIGATION_API_TOKEN (current) and
// INVESTIGATION_API_TOKEN_NEXT (next).
//
// The API is enabled only by the current token: when current is empty it
// returns nil, so the routes are not registered, even if next is set. It logs
// why the API is disabled, but never a token value. Call it once at startup.
func AcceptedTokens(current, next string, logger *slog.Logger) []string {
	if current == "" {
		if next != "" {
			logger.Error("Investigation API disabled: INVESTIGATION_API_TOKEN_NEXT is set but INVESTIGATION_API_TOKEN is not")
			return nil
		}
		logger.Info("Investigation API disabled: INVESTIGATION_API_TOKEN is not set")
		return nil
	}
	if next == "" {
		return []string{current}
	}
	return []string{current, next}
}

// Enabled reports whether the service accepts any token, which is when its
// routes should be registered. A nil service is disabled.
func (s *InvestigationService) Enabled() bool {
	return s != nil && len(s.tokens) > 0
}
