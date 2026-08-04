//go:build !darwin

package resourcegov

import (
	"context"
	"fmt"
	"time"
)

func NewSystemSampler() *SystemSampler {
	return &SystemSampler{now: time.Now}
}

func (s *SystemSampler) Sample(_ context.Context) (Sample, error) {
	now := time.Now()
	if s != nil && s.now != nil {
		now = s.now()
	}
	return unavailableSample(now), fmt.Errorf("system sampler unavailable on this platform")
}
