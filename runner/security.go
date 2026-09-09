package runner

import (
	"crypto/sha256"
	"fmt"
	"github.com/infisical/go-sdk/packages/models"
	"strings"
)

func containerName(name string) string {
	return fmt.Sprintf("apollo-%x", sha256.Sum256([]byte(name)))[:39]
}
func jobSecrets(req JobRequest, secrets []models.Secret) []models.Secret {
	allowed := map[string]bool{"DATABASE_URL": true, "KMS_API_URL": true, "SYNE_KMS_SERVICE_TOKEN": true, "SYNE_KMS_CA_PEM": true, "GOOGLE_CLOUD_PROJECT": true, "R09PR0xFX0FQUExJQ0FUSU9OX0NSRURFTlRJQUw": true, "KORI_EMAIL_API_KEY": true, "KORI_EMAIL_API_URL": true, "NEXT_PUBLIC_APP_URL": true}
	if strings.HasPrefix(req.Name, "flowr-pipeline-") {
		allowed = map[string]bool{"FLOWR_API_URL": true}
	}
	out := []models.Secret{}
	for _, secret := range secrets {
		if allowed[secret.SecretKey] {
			out = append(out, secret)
		}
	}
	return out
}
