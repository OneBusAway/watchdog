package gtfs

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"
)

type StaticFailureStage string

const (
	StaticFailureRequest          StaticFailureStage = "request"
	StaticFailureHTTP             StaticFailureStage = "http"
	StaticFailureDownload         StaticFailureStage = "download"
	StaticFailureArchive          StaticFailureStage = "archive"
	StaticFailureParse            StaticFailureStage = "parse"
	StaticFailureAgencyDiscovery  StaticFailureStage = "agency_discovery"
	StaticFailureSchedule         StaticFailureStage = "schedule"
	StaticFailureCompleteValidate StaticFailureStage = "complete_validation"
	StaticFailurePublish          StaticFailureStage = "publish"
)

type StaticFailureReason string

const (
	StaticReasonTimeout           StaticFailureReason = "timeout"
	StaticReasonConnection        StaticFailureReason = "connection"
	StaticReasonRateLimited       StaticFailureReason = "rate_limited"
	StaticReasonUnauthorized      StaticFailureReason = "unauthorized"
	StaticReasonForbidden         StaticFailureReason = "forbidden"
	StaticReasonNotFound          StaticFailureReason = "not_found"
	StaticReasonGone              StaticFailureReason = "gone"
	StaticReasonServerError       StaticFailureReason = "server_error"
	StaticReasonInvalidURL        StaticFailureReason = "invalid_url"
	StaticReasonResponseTooLarge  StaticFailureReason = "response_too_large"
	StaticReasonInvalidZIP        StaticFailureReason = "invalid_zip"
	StaticReasonMissingAgencyFile StaticFailureReason = "missing_agency_file"
	StaticReasonEmptyAgencyFile   StaticFailureReason = "empty_agency_file"
	StaticReasonMissingAgencyID   StaticFailureReason = "missing_agency_id"
	StaticReasonAmbiguousAgency   StaticFailureReason = "ambiguous_agency"
	StaticReasonUnknownAgency     StaticFailureReason = "unknown_agency"
	StaticReasonInvalidSchedule   StaticFailureReason = "invalid_schedule"
	StaticReasonUnknown           StaticFailureReason = "unknown"
)

type StaticFeedError struct {
	FeedURL     string
	Stage       StaticFailureStage
	Reason      StaticFailureReason
	StatusCode  int
	RetryAfter  time.Duration
	ContentHash string
	Err         error
}

func (e *StaticFeedError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("static GTFS %s failure: %s", e.Stage, e.Reason)
	}
	return e.Err.Error()
}

func (e *StaticFeedError) Unwrap() error { return e.Err }

func asStaticFeedError(err error) *StaticFeedError {
	var failure *StaticFeedError
	if errors.As(err, &failure) {
		return failure
	}
	return &StaticFeedError{Stage: StaticFailureCompleteValidate, Reason: StaticReasonUnknown, Err: err}
}

func requestFailure(err error) *StaticFeedError {
	reason := StaticReasonConnection
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		reason = StaticReasonTimeout
	}
	return &StaticFeedError{Stage: StaticFailureRequest, Reason: reason, Err: err}
}

func statusFailure(status int, retryAfter time.Duration, err error) *StaticFeedError {
	reason := StaticReasonUnknown
	switch status {
	case http.StatusUnauthorized:
		reason = StaticReasonUnauthorized
	case http.StatusForbidden:
		reason = StaticReasonForbidden
	case http.StatusNotFound:
		reason = StaticReasonNotFound
	case http.StatusGone:
		reason = StaticReasonGone
	case http.StatusTooManyRequests:
		reason = StaticReasonRateLimited
	default:
		if status >= 500 {
			reason = StaticReasonServerError
		}
	}
	return &StaticFeedError{Stage: StaticFailureHTTP, Reason: reason, StatusCode: status, RetryAfter: retryAfter, Err: err}
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if retryAt, err := http.ParseTime(value); err == nil && retryAt.After(now) {
		return retryAt.Sub(now)
	}
	return 0
}
