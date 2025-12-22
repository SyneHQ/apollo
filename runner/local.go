package runner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
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
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return "", fmt.Errorf("failed to create docker client: %w", err)
	}
	defer cli.Close()

	image := l.Image
	if req.Image != "" {
		image = req.Image
	}

	// Pull image if it doesn't exist locally
	if err := l.ensureImageExists(ctx, cli, image); err != nil {
		return "", fmt.Errorf("failed to ensure image exists: %w", err)
	}

	resourceLimits, err := l.buildResourceLimits(req)
	if err != nil {
		fmt.Printf("Error limiting resources: %v\n", err)
		return "", err
	}

	envVars := l.buildEnvVars(req)
	cmdArgs := l.buildCmdArgs(_cmd, req)

	containerCfg := &container.Config{
		Image: image,
		Cmd:   cmdArgs,
		Env:   envVars,
	}

	hostCfg := &container.HostConfig{
		AutoRemove: false, // remove explicitly after reading logs
		Resources:  resourceLimits,
	}

	created, err := cli.ContainerCreate(ctx, containerCfg, hostCfg, nil, nil, req.Name)
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w", err)
	}

	defer func() {
		_ = cli.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{
			Force:         true,
			RemoveVolumes: true,
		})
	}()

	if err := cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("failed to start container: %w", err)
	}

	statusCh, errCh := cli.ContainerWait(ctx, created.ID, container.WaitConditionNotRunning)

	var waitStatus container.WaitResponse
	select {
	case err := <-errCh:
		if err != nil {
			return "", fmt.Errorf("failed while waiting for container: %w", err)
		}
	case waitStatus = <-statusCh:
	}

	logs, err := l.readContainerLogs(ctx, cli, created.ID)
	if err != nil {
		return "", fmt.Errorf("failed to read container logs: %w", err)
	}

	if waitStatus.Error != nil && waitStatus.Error.Message != "" {
		return "", fmt.Errorf("container error: %s: %s", waitStatus.Error.Message, logs)
	}

	if waitStatus.StatusCode != 0 {
		return "", fmt.Errorf("local run failed: exit code %d: %s", waitStatus.StatusCode, logs)
	}

	return logs, nil
}

func (l *LocalRunner) ensureImageExists(ctx context.Context, cli *client.Client, imageName string) error {
	// Check if image exists locally
	_, _, err := cli.ImageInspectWithRaw(ctx, imageName)
	if err == nil {
		// Image exists locally
		return nil
	}

	// Image doesn't exist, pull it
	fmt.Printf("Image %s not found locally, pulling...\n", imageName)
	reader, err := cli.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", imageName, err)
	}
	defer reader.Close()

	// Read the pull output to ensure it completes
	_, err = io.Copy(io.Discard, reader)
	if err != nil {
		return fmt.Errorf("failed to read image pull output: %w", err)
	}

	fmt.Printf("Successfully pulled image %s\n", imageName)
	return nil
}

func (l *LocalRunner) buildEnvVars(req JobRequest) []string {
	envs := make(map[string]string)

	for _, secret := range l.Secrets {
		envs[secret.SecretKey] = secret.SecretValue
	}

	if req.Overrides != nil && len(req.Overrides.Env) > 0 {
		for _, envVar := range req.Overrides.Env {
			envs[envVar.Name] = envVar.Value
		}
	}

	keys := make([]string, 0, len(envs))
	for k := range envs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	built := make([]string, 0, len(keys))
	for _, k := range keys {
		built = append(built, fmt.Sprintf("%s=%s", k, envs[k]))
	}

	return built
}

func (l *LocalRunner) buildCmdArgs(_cmd string, req JobRequest) []string {
	args := []string{_cmd, req.Command}

	if req.ArgsJSONBase64 != "" {
		args = append(args, req.ArgsJSONBase64)
	}

	if req.Overrides != nil && len(req.Overrides.Args) > 0 {
		args = append(args, req.Overrides.Args...)
	}

	return args
}

func (l *LocalRunner) buildResourceLimits(req JobRequest) (container.Resources, error) {
	limits := req.Resources
	if req.Overrides != nil && req.Overrides.Resources != nil {
		limits = *req.Overrides.Resources
	}

	memoryBytes, err := parseMemoryBytes(limits.Memory)
	if err != nil {
		return container.Resources{}, fmt.Errorf("invalid memory limit %q: %w", limits.Memory, err)
	}

	nanoCPUs, err := parseNanoCPUs(limits.CPU)
	if err != nil {
		return container.Resources{}, fmt.Errorf("invalid cpu limit %q: %w", limits.CPU, err)
	}

	return container.Resources{
		Memory:   memoryBytes,
		NanoCPUs: nanoCPUs,
	}, nil
}

func (l *LocalRunner) DeleteJob(ctx context.Context, name string) error {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("failed to create docker client: %w", err)
	}
	defer cli.Close()

	err = cli.ContainerRemove(ctx, name, container.RemoveOptions{
		Force:         true,
		RemoveVolumes: true,
	})
	if err != nil {
		return fmt.Errorf("failed to delete container: %w", err)
	}
	return nil
}

func (l *LocalRunner) UpdateSchedule(ctx context.Context, name string, spec string) error {
	// scheduling is handled by the in-memory scheduler in the server for local provider
	return nil
}

func (l *LocalRunner) readContainerLogs(ctx context.Context, cli *client.Client, id string) (string, error) {
	reader, err := cli.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     false,
		Timestamps: false,
		Tail:       "all",
	})
	if err != nil {
		return "", err
	}
	defer reader.Close()

	var buf bytes.Buffer
	if _, err := stdcopy.StdCopy(&buf, &buf, reader); err != nil {
		return "", err
	}

	return buf.String(), nil
}

func parseMemoryBytes(memory string) (int64, error) {
	mem := strings.TrimSpace(strings.ToUpper(memory))
	if mem == "" {
		return 0, nil
	}

	switch {
	case strings.HasSuffix(mem, "GI"):
		val, err := strconv.ParseInt(strings.TrimSuffix(mem, "GI"), 10, 64)
		if err != nil {
			return 0, err
		}
		return val * 1024 * 1024 * 1024, nil
	case strings.HasSuffix(mem, "MI"):
		val, err := strconv.ParseInt(strings.TrimSuffix(mem, "MI"), 10, 64)
		if err != nil {
			return 0, err
		}
		return val * 1024 * 1024, nil
	case strings.HasSuffix(mem, "G"):
		val, err := strconv.ParseInt(strings.TrimSuffix(mem, "G"), 10, 64)
		if err != nil {
			return 0, err
		}
		return val * 1024 * 1024 * 1024, nil
	case strings.HasSuffix(mem, "M"):
		val, err := strconv.ParseInt(strings.TrimSuffix(mem, "M"), 10, 64)
		if err != nil {
			return 0, err
		}
		return val * 1024 * 1024, nil
	default:
		val, err := strconv.ParseInt(mem, 10, 64)
		if err != nil {
			return 0, err
		}
		return val, nil
	}
}

func parseNanoCPUs(cpu string) (int64, error) {
	c := strings.TrimSpace(cpu)
	if c == "" {
		return 0, nil
	}

	if strings.HasSuffix(c, "m") {
		val, err := strconv.ParseInt(strings.TrimSuffix(c, "m"), 10, 64)
		if err != nil {
			return 0, err
		}
		return val * 1_000_000, nil
	}

	f, err := strconv.ParseFloat(c, 64)
	if err != nil {
		return 0, err
	}
	return int64(f * 1_000_000_000), nil
}
