package docker

import (
	"context"
	"fmt"
	"sort"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

const (
	composeLabelProject = "com.docker.compose.project"
	composeLabelService = "com.docker.compose.service"
)

type ComposeService struct {
	Name         string             `json:"name"`
	Containers   []ContainerSummary `json:"containers"`
	State        string             `json:"state"` // "running" | "partial" | "stopped"
	RunningCount int                `json:"running_count"`
	TotalCount   int                `json:"total_count"`
}

type ComposeProject struct {
	Name         string           `json:"name"`
	Services     []ComposeService `json:"services"`
	Status       string           `json:"status"` // "running" | "partial" | "stopped"
	RunningCount int              `json:"running_count"`
	TotalCount   int              `json:"total_count"`
}

func composeServiceState(running, total int) string {
	switch {
	case total == 0 || running == 0:
		return "stopped"
	case running == total:
		return "running"
	default:
		return "partial"
	}
}

func (c *Client) ListComposeProjects(ctx context.Context) ([]ComposeProject, error) {
	ctrs, err := c.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", composeLabelProject)),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list compose containers: %w", err)
	}

	// project name → service name → containers
	projectMap := make(map[string]map[string][]ContainerSummary)

	for _, ctr := range ctrs {
		project := ctr.Labels[composeLabelProject]
		service := ctr.Labels[composeLabelService]
		if project == "" {
			continue
		}
		if projectMap[project] == nil {
			projectMap[project] = make(map[string][]ContainerSummary)
		}

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
		projectMap[project][service] = append(projectMap[project][service], ContainerSummary{
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

	projects := make([]ComposeProject, 0, len(projectMap))
	for projectName, serviceMap := range projectMap {
		services := make([]ComposeService, 0, len(serviceMap))
		totalRunning, totalContainers := 0, 0

		for svcName, svcCtrs := range serviceMap {
			running := 0
			for _, ctr := range svcCtrs {
				if ctr.State == "running" {
					running++
				}
			}
			services = append(services, ComposeService{
				Name:         svcName,
				Containers:   svcCtrs,
				State:        composeServiceState(running, len(svcCtrs)),
				RunningCount: running,
				TotalCount:   len(svcCtrs),
			})
			totalRunning += running
			totalContainers += len(svcCtrs)
		}

		sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })

		projects = append(projects, ComposeProject{
			Name:         projectName,
			Services:     services,
			Status:       composeServiceState(totalRunning, totalContainers),
			RunningCount: totalRunning,
			TotalCount:   totalContainers,
		})
	}

	sort.Slice(projects, func(i, j int) bool { return projects[i].Name < projects[j].Name })
	return projects, nil
}

func (c *Client) composeServiceContainerIDs(ctx context.Context, project, service string) ([]string, error) {
	f := filters.NewArgs(
		filters.Arg("label", composeLabelProject+"="+project),
		filters.Arg("label", composeLabelService+"="+service),
	)
	ctrs, err := c.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return nil, fmt.Errorf("failed to list service containers: %w", err)
	}
	if len(ctrs) == 0 {
		return nil, fmt.Errorf("no containers found for service %q in project %q", service, project)
	}
	ids := make([]string, len(ctrs))
	for i, ctr := range ctrs {
		ids[i] = ctr.ID
	}
	return ids, nil
}

func (c *Client) StartComposeService(ctx context.Context, project, service string) error {
	ids, err := c.composeServiceContainerIDs(ctx, project, service)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := c.StartContainer(ctx, id); err != nil {
			return fmt.Errorf("failed to start container %s: %w", id[:12], err)
		}
	}
	return nil
}

func (c *Client) StopComposeService(ctx context.Context, project, service string) error {
	ids, err := c.composeServiceContainerIDs(ctx, project, service)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := c.StopContainer(ctx, id); err != nil {
			return fmt.Errorf("failed to stop container %s: %w", id[:12], err)
		}
	}
	return nil
}

func (c *Client) RestartComposeService(ctx context.Context, project, service string) error {
	ids, err := c.composeServiceContainerIDs(ctx, project, service)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := c.RestartContainer(ctx, id); err != nil {
			return fmt.Errorf("failed to restart container %s: %w", id[:12], err)
		}
	}
	return nil
}
