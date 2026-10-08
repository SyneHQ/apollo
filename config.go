package config

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"

	"github.com/joho/godotenv"
	"go.yaml.in/yaml/v3"
)

type JobsConfig struct {
	Cmd     string         `yaml:"cmd"`
	Image   string         `yaml:"image"`
	Secrets []SecretConfig `yaml:"secrets"`
	Jobs    []JobConfig    `yaml:"jobs"`
}

type SecretConfig struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

type JobConfig struct {
	Name      string         `yaml:"name"`
	Resources ResourceConfig `yaml:"resources"`
}

type ResourceConfig struct {
	Memory string `yaml:"memory"`
	CPU    string `yaml:"cpu"`
}

type StoreConfig struct {
	Driver string
	Path   string
}

type Config struct {
	KMSAddress   string
	Port         string
	Store        StoreConfig
	Environment  string
	Jobs         JobsConfig
	JobsProvider string // "cloudrun" or "local"
	GCPProjectID string
	GCPRegion    string
}

func Load() (*Config, error) {
	// let's load the config from the .env file
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		return nil, errors.New("cannot read local environment file")
	}

	jobs, err := readYML()
	if err != nil {
		return nil, err
	}

	c := &Config{
		Port:         getEnv("PORT", "6910"),
		Environment:  getEnv("ENVIRONMENT", "development"),
		Store:        StoreConfig{Driver: getEnv("STORE_DRIVER", "sqlite"), Path: getEnv("STORE_PATH", "jobs.db")},
		Jobs:         *jobs,
		JobsProvider: getEnv("JOBS_PROVIDER", "local"),
		GCPProjectID: getEnv("GCP_PROJECT_ID", ""),
		GCPRegion:    getEnv("GCP_REGION", "us-central1"),
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func readYML() (*JobsConfig, error) {
	// file can be on /app/jobs.yml or jobs.yml
	// load and parse jobs.yml file
	yml, err := os.ReadFile("/app/jobs.yml")
	if os.IsNotExist(err) {
		yml, err = os.ReadFile("jobs.yml")
	}
	if err != nil {
		if os.IsNotExist(err) {
			return &JobsConfig{}, nil
		}
		return nil, errors.New("cannot read jobs.yml")
	}

	var jobs JobsConfig
	// parse the yaml
	if err := yaml.Unmarshal(yml, &jobs); err != nil {
		return nil, errors.New("jobs.yml contains invalid YAML")
	}
	return &jobs, nil
}

// GetResourcesFor returns resource config for a known job key
func (c *Config) GetResourcesFor(jobName string) ResourceConfig {
	for _, job := range c.Jobs.Jobs {
		if job.Name == jobName {
			return job.Resources
		}
	}

	return ResourceConfig{
		Memory: "256Mi",
		CPU:    "250m",
	}
}

// Validate rejects settings that can silently disable persistence or select a runner.
func (c *Config) Validate() error {
	port, err := strconv.Atoi(c.Port)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("PORT must be an integer from 1 to 65535")
	}
	if c.JobsProvider != "local" && c.JobsProvider != "cloudrun" {
		return errors.New("JOBS_PROVIDER must be local or cloudrun")
	}
	if c.Store.Driver != "sqlite" && c.Store.Driver != "postgres" {
		return errors.New("STORE_DRIVER must be sqlite or postgres")
	}
	if c.Store.Path == "" {
		return errors.New("STORE_PATH must be set")
	}
	if c.Environment == "production" && c.Store.Driver == "sqlite" && !filepath.IsAbs(c.Store.Path) {
		return errors.New("production SQLite STORE_PATH must be an absolute path on persistent storage")
	}
	if c.JobsProvider == "cloudrun" && (c.GCPProjectID == "" || c.GCPRegion == "") {
		return errors.New("cloudrun requires GCP_PROJECT_ID and GCP_REGION")
	}
	return nil
}
