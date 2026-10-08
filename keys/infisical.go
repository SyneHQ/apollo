package keys

import (
	"context"
	"errors"
	"net/url"
	"os"
	"time"

	infisical "github.com/infisical/go-sdk"
	"github.com/infisical/go-sdk/packages/models"
)

type InfisicalSecrets struct {
	client infisical.InfisicalClientInterface
}

func (i *InfisicalSecrets) GetClient() infisical.InfisicalClientInterface { return i.client }

type BootstrapConfig struct{ URL, ClientID, ClientSecret, ProjectID, Environment string }

// Hydrate loads secrets before configuration, database access, or listeners start.
func Hydrate(getenv func(string) string, fetch func(context.Context, BootstrapConfig) error) error {
	enabled := getenv("ENABLE_INFISICAL") == "true" || getenv("USE_INFISICAL") == "true" || getenv("INFISICAL_CLIENT_ID") != "" || getenv("INFISICAL_CLIENT_SECRET") != ""
	if !enabled {
		return nil
	}
	cfg := BootstrapConfig{URL: getenv("INFISICAL_API_URL"), ClientID: getenv("INFISICAL_CLIENT_ID"), ClientSecret: getenv("INFISICAL_CLIENT_SECRET"), ProjectID: getenv("INFISICAL_PROJECT_ID"), Environment: getenv("INFISICAL_ENV")}
	if cfg.URL == "" {
		cfg.URL = "https://app.infisical.com"
	}
	endpoint, err := url.Parse(cfg.URL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return errors.New("INFISICAL_API_URL must be an HTTPS service URL without credentials, query parameters, or a fragment")
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.ProjectID == "" || cfg.Environment == "" {
		return errors.New("Infisical bootstrap configuration is incomplete; set client ID, client secret, project ID, and environment")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := fetch(ctx, cfg); err != nil {
		return errors.New("Infisical secret loading failed; verify service access and bootstrap configuration")
	}
	return nil
}

// NewInfisicalSecrets returns errors to the caller. It never exits the process.
// enabled permits callers to require loading without setting an environment flag.
func NewInfisicalSecrets(enabled bool) ([]models.Secret, error) {
	var result []models.Secret
	getenv := func(key string) string {
		if enabled && key == "ENABLE_INFISICAL" {
			return "true"
		}
		return os.Getenv(key)
	}
	err := Hydrate(getenv, func(ctx context.Context, cfg BootstrapConfig) error {
		client := infisical.NewInfisicalClient(ctx, infisical.Config{SiteUrl: cfg.URL, AutoTokenRefresh: false})
		if _, err := client.Auth().UniversalAuthLogin(cfg.ClientID, cfg.ClientSecret); err != nil {
			return err
		}
		secrets, err := client.Secrets().List(infisical.ListSecretsOptions{ProjectID: cfg.ProjectID, Environment: cfg.Environment, AttachToProcessEnv: true})
		if err != nil {
			return err
		}
		result = secrets
		return nil
	})
	return result, err
}
