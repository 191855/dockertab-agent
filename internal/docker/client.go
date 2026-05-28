package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/distribution/reference"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type ContainerEvent struct {
	ContainerID   string
	ContainerName string
	Action        string // "start" | "stop" | "die" | "kill"
}

// DockerClient is the security boundary for all Docker operations — only the
// operations listed here are accessible to the rest of the agent.
type DockerClient interface {
	Ping(ctx context.Context) error
	Close() error

	ListContainers(ctx context.Context) ([]ContainerSummary, error)
	GetContainer(ctx context.Context, id string) (*ContainerSummary, error)
	GetContainerStats(ctx context.Context, id string) (*ContainerStats, error)
	GetContainerLogs(ctx context.Context, id string, lines int) (string, error)
	StreamLogs(ctx context.Context, id string, lines int) (io.ReadCloser, error)
	GetHostInfo(ctx context.Context) (*HostInfo, error)
	ListImages(ctx context.Context) ([]ImageSummary, error)
	ListVolumes(ctx context.Context) ([]VolumeSummary, error)
	CheckImageUpdates(ctx context.Context, images []string) (map[string]bool, error)

	StartContainer(ctx context.Context, id string) error
	StopContainer(ctx context.Context, id string) error
	RestartContainer(ctx context.Context, id string) error

	GetContainerEnv(ctx context.Context, id string) ([]string, error)

	ExecCreate(ctx context.Context, containerID string, cmd []string, rows, cols int) (string, error)
	ExecAttach(ctx context.Context, execID string) (types.HijackedResponse, error)
	ExecResize(ctx context.Context, execID string, rows, cols int) error

	Events(ctx context.Context) (<-chan ContainerEvent, <-chan error)

	ListComposeProjects(ctx context.Context) ([]ComposeProject, error)
	ComposeProjectAction(ctx context.Context, project, action string) error
	ComposeServiceAction(ctx context.Context, project, service, action string) error
}

type ImageSummary struct {
	ID      string   `json:"id"`
	Tags    []string `json:"tags"`
	SizeMB  float64  `json:"size_mb"`
	Created int64    `json:"created"` // Unix seconds
}

type VolumeSummary struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Mountpoint string            `json:"mountpoint"`
	Scope      string            `json:"scope"`
	CreatedAt  string            `json:"created_at,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
}

const bytesPerMB = 1024 * 1024

func trimContainerName(s string) string {
	if len(s) > 0 && s[0] == '/' {
		return s[1:]
	}
	return s
}

var _ DockerClient = (*Client)(nil)

type Client struct {
	cli *client.Client
}

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

type ContainerStats struct {
	CPUUsage       float64 `json:"cpu_usage"`
	MemoryUsage    float64 `json:"memory_usage"`
	MemoryLimit    float64 `json:"memory_limit"`
	NetInput       float64 `json:"net_input"`
	NetOutput      float64 `json:"net_output"`
	BlockRead      float64 `json:"block_read"`
	BlockWrite     float64 `json:"block_write"`
	PIDs           uint64  `json:"pids"`
	CPUThrottlePct float64 `json:"cpu_throttle_pct"`
}

type PortBinding struct {
	HostPort      string `json:"host_port"`
	ContainerPort string `json:"container_port"`
	Protocol      string `json:"protocol"`
}

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

func (c *Client) ListContainers(ctx context.Context) ([]ContainerSummary, error) {
	containers, err := c.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	summaries := make([]ContainerSummary, 0, len(containers))
	for _, ctr := range containers {
		name := ""
		if len(ctr.Names) > 0 {
			name = trimContainerName(ctr.Names[0])
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

func (c *Client) GetContainer(ctx context.Context, id string) (*ContainerSummary, error) {
	inspect, err := c.cli.ContainerInspect(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("container not found: %w", err)
	}

	name := trimContainerName(inspect.Name)

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

	var createdUnix int64
	if created, err := time.Parse(time.RFC3339Nano, inspect.Created); err == nil {
		createdUnix = created.Unix()
	}
	return &ContainerSummary{
		ID:      inspect.ID[:12],
		Name:    name,
		Image:   inspect.Config.Image,
		State:   inspect.State.Status,
		Status:  inspect.State.Status,
		Created: createdUnix,
		Ports:   ports,
	}, nil
}

func (c *Client) StartContainer(ctx context.Context, id string) error {
	return c.cli.ContainerStart(ctx, id, container.StartOptions{})
}

func (c *Client) StopContainer(ctx context.Context, id string) error {
	timeout := 10
	return c.cli.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
}

func (c *Client) RestartContainer(ctx context.Context, id string) error {
	timeout := 10
	return c.cli.ContainerRestart(ctx, id, container.StopOptions{Timeout: &timeout})
}

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

// StreamLogs strips Docker's 8-byte multiplexed framing headers from the returned reader.
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
		_, err := stdcopy.StdCopy(pw, pw, raw)
		pw.CloseWithError(err)
	}()

	return pr, nil
}

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
		MemoryTotal:   float64(info.MemTotal) / bytesPerMB,
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
			ID:      img.ID[7:19], // strip "sha256:", take 12 chars
			Tags:    tags,
			SizeMB:  float64(img.Size) / bytesPerMB,
			Created: img.Created,
		})
	}
	return summaries, nil
}

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

func (c *Client) ExecResize(ctx context.Context, execID string, rows, cols int) error {
	return c.cli.ContainerExecResize(ctx, execID, container.ResizeOptions{
		Height: uint(rows),
		Width:  uint(cols),
	})
}

func (c *Client) ExecAttach(ctx context.Context, execID string) (types.HijackedResponse, error) {
	return c.cli.ContainerExecAttach(ctx, execID, container.ExecAttachOptions{Tty: true})
}

func (c *Client) GetContainerEnv(ctx context.Context, id string) ([]string, error) {
	inspect, err := c.cli.ContainerInspect(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("container not found: %w", err)
	}
	return inspect.Config.Env, nil
}

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

func (c *Client) CheckImageUpdates(ctx context.Context, images []string) (map[string]bool, error) {
	result := make(map[string]bool)
	for _, img := range images {
		if img == "" || strings.HasPrefix(img, "<none>") || strings.HasPrefix(img, "sha256:") {
			continue
		}
		update, err := c.isImageUpdateAvailable(ctx, img)
		if err != nil {
			log.Printf("[image-updates] %s: skipped (%v)", img, err)
			continue
		}
		log.Printf("[image-updates] %s: update=%v", img, update)
		result[img] = update
	}
	return result, nil
}

func (c *Client) isImageUpdateAvailable(ctx context.Context, imageRef string) (bool, error) {
	inspect, _, err := c.cli.ImageInspectWithRaw(ctx, imageRef)
	if err != nil {
		return false, err
	}
	if len(inspect.RepoDigests) == 0 {
		return false, nil
	}
	dist, err := c.cli.DistributionInspect(ctx, imageRef, "")
	if err != nil {
		return false, err
	}
	remoteDigest := string(dist.Descriptor.Digest)

	for _, d := range inspect.RepoDigests {
		if strings.Contains(d, remoteDigest) {
			return false, nil
		}
	}

	if isManifestListType(string(dist.Descriptor.MediaType)) {
		platformDigest, err := fetchPlatformDigest(ctx, imageRef, inspect.Os, inspect.Architecture)
		if err == nil && platformDigest != "" {
			for _, d := range inspect.RepoDigests {
				if strings.Contains(d, platformDigest) {
					return false, nil
				}
			}
		}
	}

	return true, nil
}

func isManifestListType(mediaType string) bool {
	return mediaType == "application/vnd.docker.distribution.manifest.list.v2+json" ||
		mediaType == "application/vnd.oci.image.index.v1+json"
}

func fetchPlatformDigest(ctx context.Context, imageRef, osName, arch string) (string, error) {
	named, err := reference.ParseNormalizedNamed(imageRef)
	if err != nil {
		return "", err
	}
	named = reference.TagNameOnly(named)
	registry := reference.Domain(named)
	repo := reference.Path(named)
	if registry == "docker.io" {
		registry = "registry-1.docker.io"
	}

	var ref string
	if tagged, ok := named.(reference.Tagged); ok {
		ref = tagged.Tag()
	} else if digested, ok := named.(reference.Digested); ok {
		ref = string(digested.Digest())
	} else {
		return "", fmt.Errorf("no tag or digest in image ref")
	}

	accept := "application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.index.v1+json"
	manifestURL := fmt.Sprintf("https://%s/v2/%s/manifests/%s", registry, repo, ref)
	body, err := registryGet(ctx, manifestURL, accept)
	if err != nil {
		return "", err
	}

	var idx ocispec.Index
	if err := json.Unmarshal(body, &idx); err != nil {
		return "", err
	}

	for _, m := range idx.Manifests {
		if m.Platform == nil {
			continue
		}
		if m.Platform.OS == osName && m.Platform.Architecture == arch {
			return string(m.Digest), nil
		}
	}
	return "", nil
}

func registryGet(ctx context.Context, rawURL, accept string) ([]byte, error) {
	hc := &http.Client{Timeout: 10 * time.Second}

	do := func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", accept)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return hc.Do(req)
	}

	resp, err := do("")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		token, err := exchangeRegistryToken(ctx, hc, resp.Header.Get("Www-Authenticate"))
		if err != nil {
			return nil, err
		}
		resp2, err := do(token)
		if err != nil {
			return nil, err
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("registry %s", resp2.Status)
		}
		return io.ReadAll(io.LimitReader(resp2.Body, 1<<20))
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

func exchangeRegistryToken(ctx context.Context, hc *http.Client, challenge string) (string, error) {
	const bearer = "Bearer "
	if !strings.HasPrefix(challenge, bearer) {
		return "", fmt.Errorf("unsupported auth challenge")
	}
	params := parseKeyValues(challenge[len(bearer):])
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("missing realm in auth challenge")
	}

	q := url.Values{}
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	if s := params["scope"]; s != "" {
		q.Set("scope", s)
	}
	tokenURL := realm
	if len(q) > 0 {
		tokenURL += "?" + q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var tr struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", err
	}
	if tr.Token != "" {
		return tr.Token, nil
	}
	if tr.AccessToken != "" {
		return tr.AccessToken, nil
	}
	return "", fmt.Errorf("no token in response")
}

func parseKeyValues(s string) map[string]string {
	out := make(map[string]string)
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		idx := strings.IndexByte(part, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(part[:idx])
		val := strings.Trim(strings.TrimSpace(part[idx+1:]), `"`)
		out[key] = val
	}
	return out
}

func parseStats(stats *types.StatsJSON) *ContainerStats {
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

	memUsage := float64(stats.MemoryStats.Usage-stats.MemoryStats.Stats["cache"]) / bytesPerMB
	memLimit := float64(stats.MemoryStats.Limit) / bytesPerMB

	var netIn, netOut float64
	for _, v := range stats.Networks {
		netIn += float64(v.RxBytes)
		netOut += float64(v.TxBytes)
	}

	var blockRead, blockWrite float64
	for _, bio := range stats.BlkioStats.IoServiceBytesRecursive {
		switch strings.ToLower(bio.Op) {
		case "read":
			blockRead += float64(bio.Value)
		case "write":
			blockWrite += float64(bio.Value)
		}
	}

	cpuThrottlePct := 0.0
	throttleData := stats.CPUStats.ThrottlingData
	if throttleData.ThrottledPeriods > 0 && throttleData.Periods > 0 {
		cpuThrottlePct = float64(throttleData.ThrottledPeriods) / float64(throttleData.Periods) * 100.0
	}

	return &ContainerStats{
		CPUUsage:       cpuPercent,
		MemoryUsage:    memUsage,
		MemoryLimit:    memLimit,
		NetInput:       netIn / bytesPerMB,
		NetOutput:      netOut / bytesPerMB,
		BlockRead:      blockRead / bytesPerMB,
		BlockWrite:     blockWrite / bytesPerMB,
		PIDs:           stats.PidsStats.Current,
		CPUThrottlePct: cpuThrottlePct,
	}
}
