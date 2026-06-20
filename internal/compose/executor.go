package compose

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

type Executor interface {
	Up(ctx context.Context, name string) (string, error)
	Down(ctx context.Context, name string) (string, error)
	Pull(ctx context.Context, name string) (string, error)
	Logs(ctx context.Context, name string, lines int) (string, error)
}

// start/stop/restart use the Docker SDK rather than CLI for speed and reliability.
type CLIExecutor struct {
	store Storer
}

var _ Executor = (*CLIExecutor)(nil)

func NewCLIExecutor(store Storer) *CLIExecutor {
	return &CLIExecutor{store: store}
}

func (e *CLIExecutor) run(ctx context.Context, name, configFile string, args ...string) (string, error) {
	cmdArgs := []string{"compose", "--project-name", name}
	if configFile != "" {
		cmdArgs = append(cmdArgs, "-f", configFile)
	}
	cmdArgs = append(cmdArgs, args...)

	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", cmdArgs...)
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("docker compose %v: %w\n%s", args, err, out.String())
	}
	return out.String(), nil
}

func (e *CLIExecutor) resolveFile(name string) string {
	if e.store.Exists(name) {
		return e.store.FilePath(name)
	}
	return ""
}

func (e *CLIExecutor) Up(ctx context.Context, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()
	return e.run(ctx, name, e.resolveFile(name), "up", "-d")
}

func (e *CLIExecutor) Down(ctx context.Context, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()
	return e.run(ctx, name, e.resolveFile(name), "down")
}

func (e *CLIExecutor) Pull(ctx context.Context, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()
	return e.run(ctx, name, e.resolveFile(name), "pull")
}

func (e *CLIExecutor) Logs(ctx context.Context, name string, lines int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return e.run(ctx, name, e.resolveFile(name), "logs", "--no-color", fmt.Sprintf("--tail=%d", lines))
}
