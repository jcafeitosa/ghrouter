package resourcegov

import (
	"context"
	"os/exec"
	"time"
)

type commandRunner func(context.Context, string, ...string) ([]byte, error)

type SystemSampler struct {
	now timefunc
	run commandRunner
}

type timefunc func() time.Time

func commandOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 500 * time.Millisecond
	return cmd.Output()
}

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
