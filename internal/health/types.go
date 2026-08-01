package health

import "time"

type HealthStatus string

const (
	HealthUnknown   HealthStatus = "unknown"
	HealthHealthy   HealthStatus = "healthy"
	HealthDegraded  HealthStatus = "degraded"
	HealthUnhealthy HealthStatus = "unhealthy"
	HealthCooldown  HealthStatus = "cooldown"
)

type HealthCheckResult struct {
	Provider            string
	Model               string
	Status              HealthStatus
	Latency             time.Duration
	Error               error
	Timestamp           time.Time
	ConsecutiveErrors   int
	ConsecutiveTimeouts int
}
