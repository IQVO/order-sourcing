package main

import (
	"context"
	"errors"
	"testing"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadConfigPrefersTheReaderDSN(t *testing.T) {
	c, err := loadConfig(envOf(map[string]string{"ANALYTICS_READER_DATABASE_URL": "postgres://reader", "ANALYTICS_DATABASE_URL": "postgres://writer"}))
	if err != nil || c.analyticsURL != "postgres://reader" || c.httpAddr != ":8092" {
		t.Fatalf("config = %+v, %v", c, err)
	}
}

func TestLoadConfigFallsBackToTheProjectorDSN(t *testing.T) {
	c, err := loadConfig(envOf(map[string]string{"ANALYTICS_DATABASE_URL": "postgres://writer", "HTTP_ADDR": ":9"}))
	if err != nil || c.analyticsURL != "postgres://writer" || c.httpAddr != ":9" {
		t.Fatalf("config = %+v, %v", c, err)
	}
}

func TestLoadConfigRefusesWithoutADSN(t *testing.T) {
	if _, err := loadConfig(envOf(map[string]string{})); !errors.Is(err, errMissingAnalyticsURL) {
		t.Fatalf("err = %v", err)
	}
	if err := run(context.Background(), envOf(map[string]string{})); !errors.Is(err, errMissingAnalyticsURL) {
		t.Fatalf("run must refuse before opening anything: %v", err)
	}
}
