package server

import (
	"context"
	"errors"
	"strings"
)

func publicProviderError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "provider request timed out"
	}
	if errors.Is(err, context.Canceled) {
		return "provider request canceled"
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"quota", "rate limit", "rate_limit", "too many requests", "insufficient credits", "usage limit", "weekly limit", "monthly limit", "credits exhausted", "upgrade your plan", "extra usage", "third-party apps"} {
		if strings.Contains(message, marker) {
			return "provider capacity limit reached"
		}
	}
	return "provider request failed"
}
