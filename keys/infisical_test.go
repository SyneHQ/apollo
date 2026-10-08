package keys

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestHydrate(t *testing.T) {
	good := map[string]string{"ENABLE_INFISICAL": "true", "INFISICAL_API_URL": "https://secrets.example.test", "INFISICAL_CLIENT_ID": "id", "INFISICAL_CLIENT_SECRET": "credential", "INFISICAL_PROJECT_ID": "project", "INFISICAL_ENV": "prod"}
	for _, test := range []struct {
		name, key, value string
		wantErr          bool
	}{
		{"complete", "", "", false}, {"missing project", "INFISICAL_PROJECT_ID", "", true},
		{"HTTP", "INFISICAL_API_URL", "http://example.test", true},
		{"URL credentials", "INFISICAL_API_URL", "https://secret@example.test", true},
		{"URL query", "INFISICAL_API_URL", "https://example.test?secret=hidden", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			err := Hydrate(func(key string) string {
				if key == test.key {
					return test.value
				}
				return good[key]
			}, func(ctx context.Context, cfg BootstrapConfig) error {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 20*time.Second {
					t.Fatal("fetch requires a bounded deadline")
				}
				if cfg.ProjectID != "project" || cfg.Environment != "prod" {
					t.Fatal("wrong secret scope")
				}
				return nil
			})
			if (err != nil) != test.wantErr {
				t.Fatalf("unexpected result: %v", err)
			}
			if test.wantErr && calls != 0 {
				t.Fatal("invalid configuration reached remote service")
			}
			if !test.wantErr && calls != 1 {
				t.Fatal("secrets were not loaded")
			}
		})
	}
	err := Hydrate(func(key string) string { return good[key] }, func(context.Context, BootstrapConfig) error { return errors.New("provider leaked credential") })
	if err == nil || strings.Contains(err.Error(), "credential") {
		t.Fatalf("provider errors must be redacted: %v", err)
	}
}

func TestHydrateDisabledAndImplicitEnable(t *testing.T) {
	called := false
	fetch := func(context.Context, BootstrapConfig) error { called = true; return nil }
	if err := Hydrate(func(string) string { return "" }, fetch); err != nil || called {
		t.Fatal("disabled bootstrap must not contact provider")
	}
	err := Hydrate(func(key string) string {
		if key == "INFISICAL_CLIENT_SECRET" {
			return "secret"
		}
		return ""
	}, fetch)
	if err == nil || called {
		t.Fatal("partial credentials must reject startup")
	}
}
