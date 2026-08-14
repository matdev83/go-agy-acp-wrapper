package agy

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultQuotaRetryAttempts = 5
	minProviderLimitBackoff   = 2 * time.Second
	maxProviderLimitBackoff   = 60 * time.Second
	maxParsedRetryAfter       = 24 * time.Hour
	retryRunReserve           = 2 * time.Second
)

var (
	retryDelayJSON = regexp.MustCompile(`(?i)retrydelay"\s*:\s*"?([0-9]+(?:\.[0-9]+)?)(ms|s|m|h)?"?`)
	retryInGoDur   = regexp.MustCompile(`(?i)(?:please\s+)?retry\s+in\s+((?:[0-9]+(?:\.[0-9]+)?(?:ms|h|m|s))+)\b`)
	retryInWords   = regexp.MustCompile(`(?i)(?:please\s+)?retry\s+in\s+([0-9]+(?:\.[0-9]+)?)\s*(milliseconds?|seconds?|minutes?|hours?|ms|h|m|s)\b`)
	retryAfterHdr  = regexp.MustCompile(`(?i)retry-after[:\s]+([0-9]+)`)
	resetInGoDur   = regexp.MustCompile(`(?i)reset(?:s)?\s+in\s+((?:[0-9]+(?:\.[0-9]+)?(?:ms|h|m|s))+)\b`)
)

// ParseRetryAfter extracts a provider-suggested wait from quota or 429 text.
func ParseRetryAfter(text string) (time.Duration, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, false
	}
	if wait, ok := parseRetryAfterMatch(retryDelayJSON.FindStringSubmatch(text), true); ok {
		return wait, true
	}
	if wait, ok := parseGoDurationCapture(retryInGoDur.FindStringSubmatch(text)); ok {
		return wait, true
	}
	if wait, ok := parseRetryAfterMatch(retryInWords.FindStringSubmatch(text), false); ok {
		return wait, true
	}
	if wait, ok := parseGoDurationCapture(resetInGoDur.FindStringSubmatch(text)); ok {
		return wait, true
	}
	if matches := retryAfterHdr.FindStringSubmatch(text); len(matches) == 2 {
		seconds, err := strconv.Atoi(matches[1])
		if err == nil && seconds > 0 {
			return clampRetryAfter(time.Duration(seconds) * time.Second), true
		}
	}
	return 0, false
}

func parseGoDurationCapture(matches []string) (time.Duration, bool) {
	if len(matches) < 2 {
		return 0, false
	}
	wait, err := time.ParseDuration(strings.ToLower(matches[1]))
	if err != nil || wait <= 0 {
		return 0, false
	}
	return clampRetryAfter(wait), true
}

func parseRetryAfterMatch(matches []string, jsonDelay bool) (time.Duration, bool) {
	if len(matches) < 2 {
		return 0, false
	}
	amount, err := strconv.ParseFloat(matches[1], 64)
	if err != nil || amount <= 0 {
		return 0, false
	}
	unit := "s"
	if len(matches) >= 3 && matches[2] != "" {
		unit = strings.ToLower(matches[2])
	} else if !jsonDelay {
		return 0, false
	}
	wait := durationFromUnit(amount, unit)
	if wait <= 0 {
		return 0, false
	}
	return clampRetryAfter(wait), true
}

func durationFromUnit(amount float64, unit string) time.Duration {
	switch unit {
	case "ms", "millisecond", "milliseconds":
		return time.Duration(amount * float64(time.Millisecond))
	case "s", "second", "seconds":
		return time.Duration(amount * float64(time.Second))
	case "m", "minute", "minutes":
		return time.Duration(amount * float64(time.Minute))
	case "h", "hour", "hours":
		return time.Duration(amount * float64(time.Hour))
	default:
		return 0
	}
}

func clampRetryAfter(wait time.Duration) time.Duration {
	if wait > maxParsedRetryAfter {
		return maxParsedRetryAfter
	}
	return wait
}

// NextProviderLimitWait chooses the backoff after a provider-limit failure.
// remaining is time left on the ACP turn. maxWait 0 means "no extra cap".
func NextProviderLimitWait(failedAttempt int, err error, remaining, maxWait time.Duration) (time.Duration, bool) {
	if !IsProviderLimitError(err) {
		return 0, false
	}
	if remaining < minProviderLimitBackoff+retryRunReserve {
		return 0, false
	}
	wait := minProviderLimitBackoff << failedAttempt
	if wait <= 0 || wait > maxProviderLimitBackoff {
		wait = maxProviderLimitBackoff
	}
	if parsed, ok := ParseRetryAfter(errorDetail(err)); ok {
		wait = parsed
	}
	if maxWait > 0 && wait > maxWait {
		wait = maxWait
	}
	budget := remaining - retryRunReserve
	if budget < minProviderLimitBackoff {
		return 0, false
	}
	if wait > budget {
		wait = budget
	}
	if wait < minProviderLimitBackoff {
		wait = minProviderLimitBackoff
		if wait > budget {
			return 0, false
		}
	}
	return wait, true
}

func errorDetail(err error) string {
	var processErr *ProcessError
	if errors.As(err, &processErr) {
		return processErr.Detail + "\n" + processErr.Error()
	}
	return err.Error()
}
