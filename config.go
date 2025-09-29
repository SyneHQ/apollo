package config

import (
	"log"
	"os"

	"github.com/joho/godotenv"
	"go.yaml.in/yaml/v3"
)

type JobsConfig struct {
	Image   string
	Backup  JobConfig
	Restore JobConfig
	Migrate JobConfig
}

type JobConfig struct {
	Resources ResourceConfig
}

type ResourceConfig struct {
	Memory string
	CPU    string
}

type Config struct {
	KMSAddress   string
	Port         string
	DatabaseURL  string
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
		Port:         getEnv("PORT", "8080"),
		Environment:  getEnv("ENVIRONMENT", "development"),
		DatabaseURL:  getEnv("DATABASE_URL", "postgres://syneuser:synehq@mbp:5432/synehq?sslmode=require"),
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
	// load and parse jobs.yml file
	yml, err := os.ReadFile("jobs.yml")
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
	switch jobName {
	case "backup", "handleBackupJob", "handle_backup":
		return c.Jobs.Backup.Resources
	case "restore", "handleRestoreJob", "handle_restore":
		return c.Jobs.Restore.Resources
	case "migrate", "migrateJob", "migrate_job":
		return c.Jobs.Migrate.Resources
	default:
		return ResourceConfig{}
	}
}
