package types

import (
	"testing"
	"time"
)

func TestModelInfoProvenance(t *testing.T) {
	t.Run("configured source", func(t *testing.T) {
		if got := (ModelInfo{Source: "configured"}).Provenance(); got != ModelProvenanceConfigured {
			t.Fatalf("expected configured provenance, got %q", got)
		}
	})
	t.Run("observed source", func(t *testing.T) {
		if got := (ModelInfo{Source: "native"}).Provenance(); got != ModelProvenanceObserved {
			t.Fatalf("expected observed provenance, got %q", got)
		}
	})
	t.Run("verified overrides source", func(t *testing.T) {
		if got := (ModelInfo{Source: "configured", VerifiedAt: now()}).Provenance(); got != ModelProvenanceVerified {
			t.Fatalf("expected verified provenance, got %q", got)
		}
	})
}

func TestCanonicalModelID(t *testing.T) {
	if got := CanonicalModelID("  CC/Claude-Sonnet-5  "); got != "cc/claude-sonnet-5" {
		t.Fatalf("expected lowercase trimmed canonical id, got %q", got)
	}
}

func now() time.Time {
	return time.Unix(1, 0).UTC()
}
