package config

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	KMSAddress  string
	Port        string
	DatabaseURL string
	Environment string
}

func Load() (*Config, error) {
	// let's load the config from the .env file
	err := godotenv.Load()
	if err != nil {
		log.Printf("Error loading .env file: %v", err)
	}

	return &Config{
		Port:        getEnv("PORT", "8080"),
		Environment: getEnv("ENVIRONMENT", "development"),
		DatabaseURL: getEnv("DATABASE_URL", "postgres://syneuser:synehq@mbp:5432/synehq?sslmode=require"),
	}, nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
