package runner

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/infisical/go-sdk/packages/models"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

type LocalRunner struct {
	Image   string
	Secrets []models.Secret
}

func NewLocalRunner(image string, secrets []models.Secret) *LocalRunner {
	return &LocalRunner{Image: image, Secrets: secrets}
}

func (l *LocalRunner) RunJob(ctx context.Context, _cmd string, req JobRequest) (string, error) {
	cli, err := client.New(client.FromEnv)
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
		Image:  image,
		Cmd:    cmdArgs,
		Env:    envVars,
		Labels: map[string]string{"syne.apollo.managed": "true", "syne.apollo.job": req.Name},
	}

	hostCfg := &container.HostConfig{
		AutoRemove:  false, // remove explicitly after reading logs
		Resources:   resourceLimits,
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges:true"},
	}

	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Config: containerCfg, HostConfig: hostCfg, Name: containerName(req.Name)})
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w", err)
	}

	defer func() {
		_, _ = cli.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{
			Force:         true,
			RemoveVolumes: true,
		})
	}()

	if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return "", fmt.Errorf("failed to start container: %w", err)
	}

	wait := cli.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})

	var waitStatus container.WaitResponse
	select {
	case err := <-wait.Error:
		if err != nil {
			return "", fmt.Errorf("failed while waiting for container: %w", err)
		}
	case waitStatus = <-wait.Result:
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
	_, err := cli.ImageInspect(ctx, imageName)
	if err == nil {
		// Image exists locally
		return nil
	}

	// Image doesn't exist, pull it
	fmt.Printf("Image %s not found locally, pulling...\n", imageName)
	reader, err := cli.ImagePull(ctx, imageName, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", imageName, err)
	}
	defer reader.Close()

	// Read the pull output to ensure it completes
	err = reader.Wait(ctx)
	if err != nil {
		return fmt.Errorf("failed to read image pull output: %w", err)
	}

	fmt.Printf("Successfully pulled image %s\n", imageName)
	return nil
}

func (l *LocalRunner) buildEnvVars(req JobRequest) []string {
	envs := make(map[string]string)

	for _, secret := range jobSecrets(req, l.Secrets) {
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

	if memoryBytes <= 0 {
		memoryBytes = 512 * 1024 * 1024
	}
	if nanoCPUs <= 0 {
		nanoCPUs = 1_000_000_000
	}
	if memoryBytes > 8*1024*1024*1024 || nanoCPUs > 4_000_000_000 {
		return container.Resources{}, fmt.Errorf("resource limit exceeded")
	}
	pids := int64(256)
	return container.Resources{
		PidsLimit: &pids,
		Memory:    memoryBytes,
		NanoCPUs:  nanoCPUs,
	}, nil
}

func (l *LocalRunner) DeleteJob(ctx context.Context, name string) error {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return fmt.Errorf("failed to create docker client: %w", err)
	}
	defer cli.Close()

	logicalName := name
	name = containerName(name)
	inspection, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if inspection.Container.Config == nil || inspection.Container.Config.Labels["syne.apollo.managed"] != "true" || inspection.Container.Config.Labels["syne.apollo.job"] != logicalName {
		return fmt.Errorf("container is not managed by Apollo")
	}
	_, err = cli.ContainerRemove(ctx, name, client.ContainerRemoveOptions{
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
	reader, err := cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{
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
