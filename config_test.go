package config

import (
	"os"
	"testing"
)

func TestProductionConfig(t *testing.T) {
	base := Config{Port: "6910", JobsProvider: "local", Environment: "production", Store: StoreConfig{Driver: "postgres", Path: "postgres://private"}}
	for _, tc := range []struct {
		name    string
		mutate  func(*Config)
		invalid bool
	}{
		{"valid", func(*Config) {}, false},
		{"cloud provider has no local scheduler", func(c *Config) {
			c.JobsProvider = "cloudrun"
			c.GCPProjectID = "test"
			c.GCPRegion = "us-central1"
			c.Store = StoreConfig{Driver: "sqlite", Path: "unused.db"}
		}, false},
		{"invalid provider", func(c *Config) { c.JobsProvider = "locla" }, true},
		{"invalid port", func(c *Config) { c.Port = "0" }, true},
		{"ephemeral sqlite", func(c *Config) { c.Store = StoreConfig{Driver: "sqlite", Path: "jobs.db"} }, true},
		{"persistent sqlite", func(c *Config) { c.Store = StoreConfig{Driver: "sqlite", Path: "/data/jobs.db"} }, false},
		{"unknown store", func(c *Config) { c.Store.Driver = "memory" }, true},
		{"missing store", func(c *Config) { c.Store.Path = "" }, true},
		{"incomplete cloud", func(c *Config) { c.JobsProvider = "cloudrun" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			tc.mutate(&c)
			if (c.Validate() != nil) != tc.invalid {
				t.Fatal("unexpected validation result")
			}
		})
	}
}
func TestMalformedJobsConfig(t *testing.T) {
	if _, err := os.Stat("/app/jobs.yml"); err == nil {
		t.Skip("requires no system jobs file")
	}
	t.Chdir(t.TempDir())
	if err := os.WriteFile("jobs.yml", []byte("jobs: ["), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readYML(); err == nil {
		t.Fatal("invalid YAML accepted")
	}
}
