package main

import (
	"net/http"
	"testing"
)

func TestShouldReportServerErrorIgnoresExpectedShutdown(t *testing.T) {
	if shouldReportServerError(http.ErrServerClosed) {
		t.Fatal("expected normal server shutdown to be ignored")
	}
}
