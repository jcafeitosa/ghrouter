//go:build darwin

package resourcegov

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func commandOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 500 * time.Millisecond
	return cmd.Output()
}

func NewSystemSampler() *SystemSampler {
	return &SystemSampler{now: time.Now, run: commandOutput}
}

func (s *SystemSampler) Sample(ctx context.Context) (Sample, error) {
	now := time.Now()
	if s != nil && s.now != nil {
		now = s.now()
	}
	sample := unavailableSample(now)
	if s == nil || s.run == nil {
		return sample, fmt.Errorf("system sampler unavailable")
	}
	memsizeOutput, err := s.run(ctx, "sysctl", "-n", "hw.memsize")
	if err != nil {
		return sample, fmt.Errorf("read host memory size: %w", err)
	}
	memsize, err := strconv.ParseUint(strings.TrimSpace(string(memsizeOutput)), 10, 64)
	if err != nil || memsize == 0 {
		return sample, fmt.Errorf("parse host memory size")
	}
	vmOutput, err := s.run(ctx, "vm_stat")
	if err != nil {
		return sample, fmt.Errorf("read virtual memory statistics: %w", err)
	}
	pressure, err := parseDarwinMemoryPressure(string(vmOutput), memsize)
	if err != nil {
		return sample, err
	}
	sample.UnifiedMemory = Reading{Status: MetricValid, Value: pressure}
	sample.SharedUnifiedMem = true
	return sample, nil
}

func parseDarwinMemoryPressure(output string, memsize uint64) (float64, error) {
	pageSize, pages, err := parseDarwinVMStat(output)
	if err != nil {
		return 0, err
	}
	totalPages := float64(memsize / pageSize)
	if totalPages <= 0 {
		return 0, fmt.Errorf("invalid virtual memory capacity")
	}
	available := float64(pages["pages free"] + pages["pages speculative"] + pages["pages purgeable"])
	pressure := 1 - available/totalPages
	if pressure < 0 {
		return 0, nil
	}
	if pressure > 1 {
		return 1, nil
	}
	return pressure, nil
}

func parseDarwinVMStat(output string) (uint64, map[string]uint64, error) {
	pageSize := uint64(0)
	pages := make(map[string]uint64)
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Mach Virtual Memory Statistics:") {
			fields := strings.Fields(trimmed)
			for i, field := range fields {
				if field == "of" && i+1 < len(fields) {
					pageSize, _ = strconv.ParseUint(fields[i+1], 10, 64)
				}
			}
			continue
		}
		colon := strings.IndexByte(trimmed, ':')
		if colon < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(trimmed[:colon]))
		value := strings.TrimSuffix(strings.TrimSpace(trimmed[colon+1:]), ".")
		parsed, parseErr := strconv.ParseUint(value, 10, 64)
		if parseErr == nil {
			pages[key] = parsed
		}
	}
	if pageSize == 0 {
		return 0, nil, fmt.Errorf("missing virtual memory page size")
	}
	if len(pages) == 0 {
		return 0, nil, fmt.Errorf("missing virtual memory counters")
	}
	return pageSize, pages, nil
}
