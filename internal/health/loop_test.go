package health

import (
	"context"
	"testing"
	"time"
)

type testChecker struct {
	name   string
	models []string
}

func (c testChecker) HealthCheck(context.Context) error { return nil }
func (c testChecker) GetName() string                   { return c.name }
func (c testChecker) GetModels() []string               { return c.models }

func TestLoopCheckOneMarksHealthyChecker(t *testing.T) {
	loop := NewLoop(time.Hour, time.Second)
	loop.checkOne(context.Background(), testChecker{name: "test", models: []string{"model"}})

	result := loop.GetHealth("test")
	if result == nil {
		t.Fatal("expected a health result")
	}
	if result.Status != HealthHealthy {
		t.Fatalf("expected healthy status, got %q", result.Status)
	}
}
