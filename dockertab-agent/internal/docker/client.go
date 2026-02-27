package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// ContainerEvent is a simplified Docker container event.
type ContainerEvent struct {
	ContainerID   string
	ContainerName string
	Action        string // "start" | "stop" | "die" | "kill"
}

// DockerClient defines the ONLY operations the agent is allowed to perform.
// This acts as a security boundary — no exec, no image pull, no volume/network management.
type DockerClient interface {
	Ping(ctx context.Context) error
	Close() error

	// Read operations
	ListContainers(ctx context.Context) ([]ContainerSummary, error)
	GetContainer(ctx context.Context, id string) (*ContainerSummary, error)
	GetContainerStats(ctx context.Context, id string) (*ContainerStats, error)
	GetContainerLogs(ctx context.Context, id string, lines int) (string, error)
	StreamLogs(ctx context.Context, id string, lines int) (io.ReadCloser, error)
	GetHostInfo(ctx context.Context) (*HostInfo, error)
	ListImages(ctx context.Context) ([]ImageSummary, error)
	ListVolumes(ctx context.Context) ([]VolumeSummary, error)

	// Write operations (container lifecycle only)
	StartContainer(ctx context.Context, id string) error
	StopContainer(ctx context.Context, id string) error
	RestartContainer(ctx context.Context, id string) error

	// Inspection
	GetContainerEnv(ctx context.Context, id string) ([]string, error)

	// Exec (interactive shell)
	ExecCreate(ctx context.Context, containerID string, cmd []string, rows, cols int) (string, error)
	ExecAttach(ctx context.Context, execID string) (types.HijackedResponse, error)
	ExecResize(ctx context.Context, execID string, rows, cols int) error

	// Event streaming (used by the notification watcher)
	Events(ctx context.Context) (<-chan ContainerEvent, <-chan error)
}

// ImageSummary is the simplified image model sent to the iOS app.
type ImageSummary struct {
	ID      string   `json:"id"`
	Tags    []string `json:"tags"`
	SizeMB  float64  `json:"size_mb"`
	Created int64    `json:"created"` // Unix seconds
}

// VolumeSummary is the simplified volume model sent to the iOS app.
type VolumeSummary struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Mountpoint string            `json:"mountpoint"`
	Scope      string            `json:"scope"`
	CreatedAt  string            `json:"created_at,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
}

// Compile-time check: *Client implements DockerClient.
var _ DockerClient = (*Client)(nil)

// Client wraps the Docker SDK client with DockerTab-specific methods.
type Client struct {
	cli *client.Client
}

// ContainerSummary is the simplified container model sent to the iOS app.
type ContainerSummary struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	State   string            `json:"state"`
	Status  string            `json:"status"`
	Created int64             `json:"created"`
	Ports   []PortBinding     `json:"ports"`
	Labels  map[string]string `json:"labels,omitempty"`
	Stats   *ContainerStats   `json:"stats,omitempty"`
}

// ContainerStats holds live resource usage metrics.
type ContainerStats struct {
	CPUUsage        float64 `json:"cpu_usage"`
	MemoryUsage     float64 `json:"memory_usage"`
	MemoryLimit     float64 `json:"memory_limit"`
	NetInput        float64 `json:"net_input"`
	NetOutput       float64 `json:"net_output"`
	BlockRead       float64 `json:"block_read"`        // MB read from disk
	BlockWrite      float64 `json:"block_write"`       // MB written to disk
	PIDs            uint64  `json:"pids"`              // Number of processes
	CPUThrottlePct  float64 `json:"cpu_throttle_pct"` // % of time CPU was throttled
}

// PortBinding represents a container port mapping.
type PortBinding struct {
	HostPort      string `json:"host_port"`
	ContainerPort string `json:"container_port"`
	Protocol      string `json:"protocol"`
}

// HostInfo holds system-level information.
type HostInfo struct {
	Hostname      string  `json:"hostname"`
	OS            string  `json:"os"`
	Architecture  string  `json:"architecture"`
	CPUs          int     `json:"cpus"`
	MemoryTotal   float64 `json:"memory_total"`
	DockerVersion string  `json:"docker_version"`
	Containers    int     `json:"containers"`
	Running       int     `json:"running"`
	Stopped       int     `json:"stopped"`
	Paused        int     `json:"paused"`
	Images        int     `json:"images"`
	Volumes       int     `json:"volumes"`
}

func NewClient(socketPath string) (*Client, error) {
	cli, err := client.NewClientWithOpts(
		client.WithHost("unix://"+socketPath),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	// Verify connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := cli.Ping(ctx); err != nil {
		return nil, fmt.Errorf("failed to connect to Docker: %w", err)
	}

	return &Client{cli: cli}, nil
}

func (c *Client) Close() error {
	return c.cli.Close()
}

// ListContainers returns all containers with their current state.
func (c *Client) ListContainers(ctx context.Context) ([]ContainerSummary, error) {
	containers, err := c.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	summaries := make([]ContainerSummary, 0, len(containers))
	for _, ctr := range containers {
		name := ""
		if len(ctr.Names) > 0 {
			name = ctr.Names[0]
			if len(name) > 0 && name[0] == '/' {
				name = name[1:] // Strip leading slash
			}
		}

		ports := make([]PortBinding, 0)
		for _, p := range ctr.Ports {
			ports = append(ports, PortBinding{
				HostPort:      fmt.Sprintf("%d", p.PublicPort),
				ContainerPort: fmt.Sprintf("%d", p.PrivatePort),
				Protocol:      p.Type,
			})
		}

		summaries = append(summaries, ContainerSummary{
			ID:      ctr.ID[:12],
			Name:    name,
			Image:   ctr.Image,
			State:   ctr.State,
			Status:  ctr.Status,
			Created: ctr.Created,
			Ports:   ports,
			Labels:  ctr.Labels,
		})
	}

	return summaries, nil
}

// GetContainer returns details for a single container.
func (c *Client) GetContainer(ctx context.Context, id string) (*ContainerSummary, error) {
	inspect, err := c.cli.ContainerInspect(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("container not found: %w", err)
	}

	name := inspect.Name
	if len(name) > 0 && name[0] == '/' {
		name = name[1:]
	}

	ports := make([]PortBinding, 0)
	for containerPort, bindings := range inspect.NetworkSettings.Ports {
		for _, b := range bindings {
			ports = append(ports, PortBinding{
				HostPort:      b.HostPort,
				ContainerPort: string(containerPort),
				Protocol:      containerPort.Proto(),
			})
		}
	}

	return &ContainerSummary{
		ID:     inspect.ID[:12],
		Name:   name,
		Image:  inspect.Config.Image,
		State:  inspect.State.Status,
		Status: inspect.State.Status,
		Created: func() int64 {
			t, _ := time.Parse(time.RFC3339Nano, inspect.Created)
			return t.UnixNano()
		}(),
		Ports: ports,
	}, nil
}

// StartContainer starts a stopped container.
func (c *Client) StartContainer(ctx context.Context, id string) error {
	return c.cli.ContainerStart(ctx, id, container.StartOptions{})
}

// StopContainer stops a running container with a 10-second timeout.
func (c *Client) StopContainer(ctx context.Context, id string) error {
	timeout := 10
	return c.cli.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
}

// RestartContainer restarts a container with a 10-second timeout.
func (c *Client) RestartContainer(ctx context.Context, id string) error {
	timeout := 10
	return c.cli.ContainerRestart(ctx, id, container.StopOptions{Timeout: &timeout})
}

// GetContainerStats returns a one-shot resource usage snapshot.
func (c *Client) GetContainerStats(ctx context.Context, id string) (*ContainerStats, error) {
	statsResp, err := c.cli.ContainerStats(ctx, id, false)
	if err != nil {
		return nil, fmt.Errorf("failed to get stats: %w", err)
	}
	defer statsResp.Body.Close()

	var statsJSON types.StatsJSON
	if err := json.NewDecoder(statsResp.Body).Decode(&statsJSON); err != nil {
		return nil, fmt.Errorf("failed to decode stats: %w", err)
	}

	return parseStats(&statsJSON), nil
}

// GetContainerLogs returns the last N lines of a container's logs.
func (c *Client) GetContainerLogs(ctx context.Context, id string, lines int) (string, error) {
	opts := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       fmt.Sprintf("%d", lines),
		Timestamps: true,
	}

	reader, err := c.cli.ContainerLogs(ctx, id, opts)
	if err != nil {
		return "", fmt.Errorf("failed to get logs: %w", err)
	}
	defer reader.Close()

	var buf bytes.Buffer
	if _, err := stdcopy.StdCopy(&buf, &buf, reader); err != nil {
		return "", fmt.Errorf("failed to read logs: %w", err)
	}

	return buf.String(), nil
}

// StreamLogs returns a reader for live-tailing container logs.
// The returned reader has Docker's 8-byte multiplexed headers stripped,
// so it yields clean UTF-8 log text.
func (c *Client) StreamLogs(ctx context.Context, id string, lines int) (io.ReadCloser, error) {
	opts := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       fmt.Sprintf("%d", lines),
		Timestamps: true,
		Follow:     true,
	}

	raw, err := c.cli.ContainerLogs(ctx, id, opts)
	if err != nil {
		return nil, err
	}

	pr, pw := io.Pipe()
	go func() {
		defer raw.Close()
		// StdCopy demultiplexes the Docker stream, stripping the 8-byte headers.
		// Both stdout and stderr are merged into pw.
		_, err := stdcopy.StdCopy(pw, pw, raw)
		pw.CloseWithError(err)
	}()

	return pr, nil
}

// GetHostInfo returns system-level information about the Docker host.
func (c *Client) GetHostInfo(ctx context.Context) (*HostInfo, error) {
	info, err := c.cli.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get host info: %w", err)
	}

	version, err := c.cli.ServerVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get Docker version: %w", err)
	}

	volResp, _ := c.cli.VolumeList(ctx, volume.ListOptions{})

	return &HostInfo{
		Hostname:      info.Name,
		OS:            info.OperatingSystem,
		Architecture:  info.Architecture,
		CPUs:          info.NCPU,
		MemoryTotal:   float64(info.MemTotal) / 1024 / 1024, // Convert to MB
		DockerVersion: version.Version,
		Containers:    info.Containers,
		Running:       info.ContainersRunning,
		Stopped:       info.ContainersStopped,
		Paused:        info.ContainersPaused,
		Images:        info.Images,
		Volumes:       len(volResp.Volumes),
	}, nil
}

func (c *Client) Ping(ctx context.Context) error {
	_, err := c.cli.Ping(ctx)
	return err
}

// ListImages returns a summary of all local Docker images.
func (c *Client) ListImages(ctx context.Context) ([]ImageSummary, error) {
	images, err := c.cli.ImageList(ctx, image.ListOptions{All: false})
	if err != nil {
		return nil, fmt.Errorf("failed to list images: %w", err)
	}

	summaries := make([]ImageSummary, 0, len(images))
	for _, img := range images {
		tags := img.RepoTags
		if len(tags) == 0 {
			tags = []string{"<none>:<none>"}
		}
		summaries = append(summaries, ImageSummary{
			ID:      img.ID[7:19], // strip "sha256:" prefix, take 12 chars
			Tags:    tags,
			SizeMB:  float64(img.Size) / 1024 / 1024,
			Created: img.Created,
		})
	}
	return summaries, nil
}

// ListVolumes returns a summary of all local Docker volumes.
func (c *Client) ListVolumes(ctx context.Context) ([]VolumeSummary, error) {
	resp, err := c.cli.VolumeList(ctx, volume.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list volumes: %w", err)
	}

	summaries := make([]VolumeSummary, 0, len(resp.Volumes))
	for _, v := range resp.Volumes {
		summaries = append(summaries, VolumeSummary{
			Name:       v.Name,
			Driver:     v.Driver,
			Mountpoint: v.Mountpoint,
			Scope:      v.Scope,
			CreatedAt:  v.CreatedAt,
			Labels:     v.Labels,
		})
	}
	return summaries, nil
}

// ExecCreate creates a TTY exec instance in the container and returns its ID.
func (c *Client) ExecCreate(ctx context.Context, containerID string, cmd []string, rows, cols int) (string, error) {
	resp, err := c.cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
		Cmd:          cmd,
		ConsoleSize:  &[2]uint{uint(rows), uint(cols)},
	})
	if err != nil {
		return "", fmt.Errorf("exec create failed: %w", err)
	}
	return resp.ID, nil
}

// ExecResize resizes the TTY of a running exec instance.
func (c *Client) ExecResize(ctx context.Context, execID string, rows, cols int) error {
	return c.cli.ContainerExecResize(ctx, execID, container.ResizeOptions{
		Height: uint(rows),
		Width:  uint(cols),
	})
}

// ExecAttach attaches to a previously created exec instance (TTY mode).
func (c *Client) ExecAttach(ctx context.Context, execID string) (types.HijackedResponse, error) {
	return c.cli.ContainerExecAttach(ctx, execID, container.ExecAttachOptions{Tty: true})
}

// GetContainerEnv returns the environment variables for a container.
func (c *Client) GetContainerEnv(ctx context.Context, id string) ([]string, error) {
	inspect, err := c.cli.ContainerInspect(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("container not found: %w", err)
	}
	return inspect.Config.Env, nil
}

// Events streams container lifecycle events (start, stop, die, kill).
// The caller should select on both returned channels and stop when ctx is done.
func (c *Client) Events(ctx context.Context) (<-chan ContainerEvent, <-chan error) {
	outEvents := make(chan ContainerEvent, 16)
	outErrs := make(chan error, 1)

	f := filters.NewArgs(
		filters.Arg("type", "container"),
		filters.Arg("event", "start"),
		filters.Arg("event", "stop"),
		filters.Arg("event", "die"),
		filters.Arg("event", "kill"),
	)
	msgs, errs := c.cli.Events(ctx, events.ListOptions{Filters: f})

	go func() {
		defer close(outEvents)
		defer close(outErrs)
		for {
			select {
			case <-ctx.Done():
				return
			case err, ok := <-errs:
				if !ok {
					return
				}
				if err != nil {
					outErrs <- err
				}
				return
			case msg, ok := <-msgs:
				if !ok {
					return
				}
				outEvents <- ContainerEvent{
					ContainerID:   msg.Actor.ID,
					ContainerName: msg.Actor.Attributes["name"],
					Action:        string(msg.Action),
				}
			}
		}
	}()

	return outEvents, outErrs
}

func parseStats(stats *types.StatsJSON) *ContainerStats {
	// CPU percentage calculation
	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(stats.CPUStats.SystemUsage - stats.PreCPUStats.SystemUsage)
	cpuCount := float64(stats.CPUStats.OnlineCPUs)
	if cpuCount == 0 {
		cpuCount = float64(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}

	cpuPercent := 0.0
	if systemDelta > 0 && cpuDelta > 0 {
		cpuPercent = (cpuDelta / systemDelta) * cpuCount * 100.0
	}

	// Memory usage
	memUsage := float64(stats.MemoryStats.Usage-stats.MemoryStats.Stats["cache"]) / 1024 / 1024
	memLimit := float64(stats.MemoryStats.Limit) / 1024 / 1024

	// Network I/O
	var netIn, netOut float64
	for _, v := range stats.Networks {
		netIn += float64(v.RxBytes)
		netOut += float64(v.TxBytes)
	}

	// Block I/O
	var blockRead, blockWrite float64
	for _, bio := range stats.BlkioStats.IoServiceBytesRecursive {
		switch strings.ToLower(bio.Op) {
		case "read":
			blockRead += float64(bio.Value)
		case "write":
			blockWrite += float64(bio.Value)
		}
	}

	// CPU throttling
	cpuThrottlePct := 0.0
	throttleData := stats.CPUStats.ThrottlingData
	if throttleData.ThrottledPeriods > 0 && throttleData.Periods > 0 {
		cpuThrottlePct = float64(throttleData.ThrottledPeriods) / float64(throttleData.Periods) * 100.0
	}

	return &ContainerStats{
		CPUUsage:       cpuPercent,
		MemoryUsage:    memUsage,
		MemoryLimit:    memLimit,
		NetInput:       netIn / 1024 / 1024,   // MB
		NetOutput:      netOut / 1024 / 1024,  // MB
		BlockRead:      blockRead / 1024 / 1024,
		BlockWrite:     blockWrite / 1024 / 1024,
		PIDs:           stats.PidsStats.Current,
		CPUThrottlePct: cpuThrottlePct,
	}
}
