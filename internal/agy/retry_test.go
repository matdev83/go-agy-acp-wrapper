package agy

import (
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want time.Duration
		ok   bool
	}{
		{name: "empty", in: "", ok: false},
		{name: "json retryDelay seconds", in: `"retryDelay":"58s"`, want: 58 * time.Second, ok: true},
		{name: "json retryDelay number as seconds", in: `retryDelay": 12`, want: 12 * time.Second, ok: true},
		{name: "please retry in go duration", in: "RESOURCE_EXHAUSTED Please retry in 3h25m", want: 3*time.Hour + 25*time.Minute, ok: true},
		{name: "retry in words", in: "please retry in 2 hours", want: 2 * time.Hour, ok: true},
		{name: "retry-after header seconds", in: "Retry-After: 120", want: 120 * time.Second, ok: true},
		{name: "reset in duration", in: "individual quota resets in 2h59m1s", want: 2*time.Hour + 59*time.Minute + time.Second, ok: true},
		{name: "unrelated", in: "authentication failed", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseRetryAfter(tt.in)
			if ok != tt.ok {
				t.Fatalf("ok=%v want %v (got %s)", ok, tt.ok, got)
			}
			if got != tt.want {
				t.Fatalf("wait=%s want %s", got, tt.want)
			}
		})
	}
}

func TestNextProviderLimitWait_ExponentialWhenNoRetryAfter(t *testing.T) {
	err := &ProcessError{Detail: "status 429: model API overloaded"}
	remaining := 10 * time.Minute
	wait, ok := NextProviderLimitWait(0, err, remaining, 0)
	if !ok || wait != 2*time.Second {
		t.Fatalf("attempt 0: wait=%s ok=%v", wait, ok)
	}
	wait, ok = NextProviderLimitWait(1, err, remaining, 0)
	if !ok || wait != 4*time.Second {
		t.Fatalf("attempt 1: wait=%s ok=%v", wait, ok)
	}
	wait, ok = NextProviderLimitWait(8, err, remaining, 0)
	if !ok || wait != maxProviderLimitBackoff {
		t.Fatalf("capped wait=%s ok=%v", wait, ok)
	}
}

func TestNextProviderLimitWait_UsesRetryAfterWithinBudget(t *testing.T) {
	err := &ProcessError{Detail: `RESOURCE_EXHAUSTED Please retry in 3h0m0s`}
	remaining := 4 * time.Hour
	wait, ok := NextProviderLimitWait(0, err, remaining, 0)
	if !ok || wait != 3*time.Hour {
		t.Fatalf("wait=%s ok=%v", wait, ok)
	}
}

func TestNextProviderLimitWait_ClampsToRemainingBudget(t *testing.T) {
	err := &ProcessError{Detail: `RESOURCE_EXHAUSTED Please retry in 3h0m0s`}
	remaining := 10 * time.Minute
	wait, ok := NextProviderLimitWait(0, err, remaining, 0)
	if !ok {
		t.Fatal("expected a clamped wait")
	}
	if wait != remaining-retryRunReserve {
		t.Fatalf("wait=%s want %s", wait, remaining-retryRunReserve)
	}
}

func TestNextProviderLimitWait_HonorsMaxWaitCap(t *testing.T) {
	err := &ProcessError{Detail: `RESOURCE_EXHAUSTED Please retry in 3h0m0s`}
	wait, ok := NextProviderLimitWait(0, err, 4*time.Hour, 15*time.Minute)
	if !ok || wait != 15*time.Minute {
		t.Fatalf("wait=%s ok=%v", wait, ok)
	}
}

func TestNextProviderLimitWait_GivesUpWhenTurnAlmostOver(t *testing.T) {
	err := &ProcessError{Detail: "status 429"}
	if _, ok := NextProviderLimitWait(0, err, 3*time.Second, 0); ok {
		t.Fatal("expected no wait when remaining time cannot cover backoff plus retry")
	}
}

func TestNextProviderLimitWait_IgnoresNonQuotaErrors(t *testing.T) {
	err := &ProcessError{Detail: "authentication failed"}
	if _, ok := NextProviderLimitWait(0, err, time.Hour, 0); ok {
		t.Fatal("unexpected wait for non-quota error")
	}
}
