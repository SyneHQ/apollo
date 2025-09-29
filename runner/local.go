package runner

import (
	"context"
	"fmt"
	"os/exec"
)

type LocalRunner struct {
	Image string
}

func NewLocalRunner(image string) *LocalRunner {
	return &LocalRunner{Image: image}
}

func (l *LocalRunner) RunJob(ctx context.Context, req JobRequest) (string, error) {
	// Run container using docker with bun command inside image
	// Example: docker run --rm <image> rover <command> <argsBase64>
	args := []string{"run", "--rm", l.Image, "/app/rover", req.Command}
	if req.ArgsJSONBase64 != "" {
		args = append(args, req.ArgsJSONBase64)
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("local run failed: %w: %s", err, string(out))
	}
	return string(out), nil
}

func (l *LocalRunner) DeleteJob(ctx context.Context, name string) error {
	// local one-off containers are ephemeral; nothing to delete
	return nil
}

func (l *LocalRunner) UpdateSchedule(ctx context.Context, name string, spec string) error {
	// scheduling is handled by the in-memory scheduler in the server for local provider
	return nil
}
