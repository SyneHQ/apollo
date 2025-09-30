package runner

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/infisical/go-sdk/packages/models"
)

type LocalRunner struct {
	Image   string
	Secrets []models.Secret
}

func NewLocalRunner(image string, secrets []models.Secret) *LocalRunner {
	return &LocalRunner{Image: image, Secrets: secrets}
}

func (l *LocalRunner) RunJob(ctx context.Context, _cmd string, req JobRequest) (string, error) {
	// Run container using docker with bun command inside image
	// Example: docker run --rm <image> rover <command> <argsBase64>
	args := []string{"run", "--rm"}

	args, err := l.AppendSecrets(ctx, req, args)
	if err != nil {
		fmt.Printf("Error appending secrets: %v\n", err)
		return "", err
	}

	args, err = l.AppendOverrides(ctx, req, args)
	if err != nil {
		fmt.Printf("Error appending overrides: %v\n", err)
		return "", err
	}

	args = append(args, l.Image, _cmd, req.Command)

	if req.ArgsJSONBase64 != "" {
		args = append(args, req.ArgsJSONBase64)
	}

	args, err = l.LimitResources(ctx, req, args)
	if err != nil {
		fmt.Printf("Error limiting resources: %v\n", err)
		return "", err
	}

	// Use overrides if provided, otherwise use default args
	if req.Overrides != nil && len(req.Overrides.Args) > 0 {
		args = append(args, req.Overrides.Args...)
	}

	cmd := exec.CommandContext(ctx, "docker", args...)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("local run failed: %w: %s", err, string(out))
	}
	return string(out), nil
}

func (l *LocalRunner) AppendSecrets(ctx context.Context, req JobRequest, args []string) ([]string, error) {
	// Inject Infisical secrets as environment variables
	for _, secret := range l.Secrets {
		args = append(args, "-e", secret.SecretKey+"="+secret.SecretValue)
	}
	return args, nil
}

func (l *LocalRunner) LimitResources(ctx context.Context, req JobRequest, args []string) ([]string, error) {
	// Use overrides if provided, otherwise use default resources
	resources := req.Resources
	if req.Overrides != nil && req.Overrides.Resources != nil {
		resources = *req.Overrides.Resources
	}

	// we need to read memory and cpu limits and apply those limits
	args = append(args, "--memory", resources.Memory, "--cpus", resources.CPU)
	return args, nil
}

func (l *LocalRunner) AppendOverrides(ctx context.Context, req JobRequest, args []string) ([]string, error) {
	// Append client-provided environment variables from overrides
	// These will override Infisical secrets if there are conflicts
	if req.Overrides != nil && len(req.Overrides.Env) > 0 {
		for _, envVar := range req.Overrides.Env {
			args = append(args, "-e", envVar.Name+"="+envVar.Value)
		}
	}
	return args, nil
}

func (l *LocalRunner) DeleteJob(ctx context.Context, name string) error {
	// local one-off containers are ephemeral; nothing to delete
	return nil
}

func (l *LocalRunner) UpdateSchedule(ctx context.Context, name string, spec string) error {
	// scheduling is handled by the in-memory scheduler in the server for local provider
	return nil
}
