package server

import (
	"errors"
	"testing"
)

func TestPublicProviderErrorOmitsSuccessfulAttemptError(t *testing.T) {
	if got := publicProviderError(nil); got != "" {
		t.Fatalf("expected no public error for a successful attempt, got %q", got)
	}
}

func TestPublicProviderErrorClassifiesCapacityWithoutLeakingDetails(t *testing.T) {
	got := publicProviderError(errors.New("provider rate limit rejected: weekly limit for account abc"))
	if got != "provider capacity limit reached" {
		t.Fatalf("expected safe capacity error, got %q", got)
	}
}

func TestPublicProviderErrorClassifiesPlanUpgradeAsCapacity(t *testing.T) {
	if got := publicProviderError(errors.New("provider capacity error: upgrade your plan to continue")); got != "provider capacity limit reached" {
		t.Fatalf("expected plan limit classification, got %q", got)
	}
}

func TestIsQuotaErrorRecognizesProviderCapacityMarkers(t *testing.T) {
	for _, message := range []string{
		"provider capacity error: upgrade your plan to continue",
		"provider capacity error: weekly limit",
		"provider rate limit rejected: seven_day",
		"provider request failed: insufficient credits",
		"Third-party apps now draw from your extra usage",
	} {
		if !isQuotaError(errors.New(message)) {
			t.Errorf("expected quota classification for %q", message)
		}
	}
	if isQuotaError(errors.New("provider returned an empty response")) {
		t.Fatal("ordinary provider failure must remain model-scoped")
	}
}
