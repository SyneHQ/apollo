package _secrets

import (
	"log"
	"os"
	"strings"

	config "github.com/SyneHQ/apollo"
	"github.com/infisical/go-sdk/packages/models"
)

func FilterSecrets(secrets []models.Secret, secretsConfig []config.SecretConfig) []models.Secret {
	// Create a map for O(1) secret lookups
	secretMap := make(map[string]models.Secret, len(secrets))
	for _, s := range secrets {
		secretMap[s.SecretKey] = s
	}

	// Pre-allocate slice with estimated capacity
	allSecrets := make([]models.Secret, 0, len(secretsConfig))

	// Use map for O(1) lookup instead of slice for injected secrets
	injectedSecrets := make(map[string]bool, len(secretsConfig))

	// Process secrets that need injection
	for _, secret := range secretsConfig {
		if s, exists := secretMap[secret.Name]; exists {
			if strings.Contains(secret.Value, "$") {
				allSecrets = append(allSecrets, s)
				injectedSecrets[s.SecretKey] = true
			}
		}
	}

	// Add remaining secrets
	for _, s := range secretsConfig {
		if !injectedSecrets[s.Name] {
			if !strings.Contains(s.Value, "$") {
				allSecrets = append(allSecrets, models.Secret{SecretKey: s.Name, SecretValue: s.Value})
			} else {
				value := os.Getenv(s.Name)
				if value == "" {
					log.Printf("Secret %s not found in environment", s.Name)
				} else {
					allSecrets = append(allSecrets, models.Secret{SecretKey: s.Name, SecretValue: value})
				}
			}
		}
	}

	return allSecrets
}
