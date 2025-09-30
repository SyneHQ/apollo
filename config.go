package config

import (
	"log"
	"os"

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
	err := godotenv.Load()
	if err != nil {
		log.Printf("Error loading .env file: %v", err)
	}

	jobs := readYML()

	return &Config{
		Port:         getEnv("PORT", "6910"),
		Environment:  getEnv("ENVIRONMENT", "development"),
		Store:        StoreConfig{Driver: getEnv("STORE_DRIVER", "sqlite"), Path: getEnv("STORE_PATH", "jobs.db")},
		Jobs:         *jobs,
		JobsProvider: getEnv("JOBS_PROVIDER", "local"),
		GCPProjectID: getEnv("GCP_PROJECT_ID", ""),
		GCPRegion:    getEnv("GCP_REGION", "us-central1"),
	}, nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func readYML() *JobsConfig {
	// file can be on /app/jobs.yml or jobs.yml
	// load and parse jobs.yml file
	yml, err := os.ReadFile("/app/jobs.yml")
	if err != nil {
		yml, err = os.ReadFile("jobs.yml")
	}
	if err != nil {
		return &JobsConfig{}
	}

	var jobs JobsConfig
	// parse the yaml
	yaml.Unmarshal(yml, &jobs)
	return &jobs
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
