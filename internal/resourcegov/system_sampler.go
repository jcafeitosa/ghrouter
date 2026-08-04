package resourcegov

import (
	"context"
	"time"
)

type commandRunner func(context.Context, string, ...string) ([]byte, error)

type SystemSampler struct {
	now timefunc
	run commandRunner
}

type timefunc func() time.Time

func unavailableSample(now time.Time) Sample {
	reading := Reading{Status: MetricUnavailable}
	return Sample{
		CapturedAt:      now,
		HostCPU:         reading,
		HostMemory:      reading,
		ProcessRSS:      reading,
		GPUVRAM:         reading,
		UnifiedMemory:   reading,
		DiskPressure:    reading,
		ProcessPressure: reading,
	}
}
