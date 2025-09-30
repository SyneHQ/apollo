package keys

import (
	"context"
	"fmt"
	"log"
	"os"

	infisical "github.com/infisical/go-sdk"
	"github.com/infisical/go-sdk/packages/models"
)

type InfisicalSecrets struct {
	client  infisical.InfisicalClientInterface
	secrets []models.Secret
}

func (i *InfisicalSecrets) GetClient() infisical.InfisicalClientInterface {
	return i.client
}

func NewInfisicalSecrets(exitOnError bool) ([]models.Secret, error) {
	log.Printf("🔑 Line 18 - NewInfisicalSecrets: Starting Infisical client initialization")

	client := infisical.NewInfisicalClient(context.Background(), infisical.Config{
		SiteUrl:          os.Getenv("INFISICAL_API_URL"), // Optional, default is https://app.infisical.com
		AutoTokenRefresh: true,                           // Wether or not to let the SDK handle the access token lifecycle. Defaults to true if not specified.
	})

	infisicalSecrets := InfisicalSecrets{
		client:  client,
		secrets: make([]models.Secret, 0),
	}

	log.Printf("🔐 Line 28 - NewInfisicalSecrets: Attempting universal auth login")
	_, err := infisicalSecrets.client.Auth().UniversalAuthLogin(os.Getenv("INFISICAL_CLIENT_ID"), os.Getenv("INFISICAL_CLIENT_SECRET"))

	if err != nil {
		log.Printf("❌ Line 32 - NewInfisicalSecrets: Auth login failed - %v", err)
		if exitOnError {
			os.Exit(1)
		}
		return nil, fmt.Errorf("failed to authenticate with Infisical: %w", err)
	}

	log.Printf("✅ Line 39 - NewInfisicalSecrets: Auth login successful, loading secrets")
	// load the secrets
	sec, err := infisicalSecrets.client.Secrets().List(infisical.ListSecretsOptions{
		ProjectID:          os.Getenv("INFISICAL_PROJECT_ID"),
		Environment:        os.Getenv("INFISICAL_ENV"),
		AttachToProcessEnv: true,
	})

	if err != nil {
		log.Printf("❌ Line 47 - NewInfisicalSecrets: Failed to load secrets - %v", err)
		if exitOnError {
			os.Exit(1)
		}
		return nil, fmt.Errorf("failed to load secrets from Infisical: %w", err)
	}

	log.Printf("🎉 Line 54 - NewInfisicalSecrets: Infisical client successfully initialized and secrets loaded")

	infisicalSecrets.secrets = sec
	return infisicalSecrets.secrets, nil
}
