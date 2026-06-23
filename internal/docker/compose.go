package docker

import (
	"context"
	"fmt"
	"sort"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

const (
	labelProject     = "com.docker.compose.project"
	labelService     = "com.docker.compose.service"
	labelConfigFiles = "com.docker.compose.project.config_files"
)

type ComposeService struct {
	Name         string             `json:"name"`
	Image        string             `json:"image,omitempty"`
	Containers   []ContainerSummary `json:"containers"`
	State        string             `json:"state"` // "running" | "partial" | "stopped"
	RunningCount int                `json:"running_count"`
	TotalCount   int                `json:"total_count"`
}

type ComposeProject struct {
	Name         string           `json:"name"`
	ConfigFile   string           `json:"config_file,omitempty"`
	Services     []ComposeService `json:"services"`
	State        string           `json:"state"` // "running" | "partial" | "stopped"
	RunningCount int              `json:"running_count"`
	TotalCount   int              `json:"total_count"`
}

func composeState(running, total int) string {
	if total == 0 || running == 0 {
		return "stopped"
	}
	if running == total {
		return "running"
	}
	return "partial"
}

func groupComposeProjects(containers []ContainerSummary) []ComposeProject {
	type serviceKey struct{ project, service string }
	serviceContainers := make(map[serviceKey][]ContainerSummary)
	configFiles := make(map[string]string)

	for _, ctr := range containers {
		project := ctr.Labels[labelProject]
		if project == "" {
			continue
		}
		service := ctr.Labels[labelService]
		key := serviceKey{project, service}
		serviceContainers[key] = append(serviceContainers[key], ctr)
		if cf := ctr.Labels[labelConfigFiles]; cf != "" {
			configFiles[project] = cf
		}
	}

	projectServices := make(map[string][]ComposeService)
	for key, ctrs := range serviceContainers {
		running := 0
		for _, c := range ctrs {
			if c.State == "running" {
				running++
			}
		}
		image := ""
		if len(ctrs) > 0 {
			image = ctrs[0].Image
		}
		svc := ComposeService{
			Name:         key.service,
			Image:        image,
			Containers:   ctrs,
			State:        composeState(running, len(ctrs)),
			RunningCount: running,
			TotalCount:   len(ctrs),
		}
		projectServices[key.project] = append(projectServices[key.project], svc)
	}

	projects := make([]ComposeProject, 0, len(projectServices))
	for name, services := range projectServices {
		sort.Slice(services, func(i, j int) bool {
			return services[i].Name < services[j].Name
		})
		running, total := 0, 0
		for _, svc := range services {
			running += svc.RunningCount
			total += svc.TotalCount
		}
		projects = append(projects, ComposeProject{
			Name:         name,
			ConfigFile:   configFiles[name],
			Services:     services,
			State:        composeState(running, total),
			RunningCount: running,
			TotalCount:   total,
		})
	}
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].Name < projects[j].Name
	})
	return projects
}

func (c *Client) ListComposeProjects(ctx context.Context) ([]ComposeProject, error) {
	containers, err := c.ListContainers(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}
	return groupComposeProjects(containers), nil
}

func (c *Client) composeContainerIDs(ctx context.Context, project, service string) ([]string, error) {
	f := filters.NewArgs(filters.Arg("label", labelProject+"="+project))
	if service != "" {
		f.Add("label", labelService+"="+service)
	}
	list, err := c.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return nil, fmt.Errorf("failed to list compose containers: %w", err)
	}
	ids := make([]string, len(list))
	for i, ctr := range list {
		ids[i] = ctr.ID
	}
	return ids, nil
}

func (c *Client) ComposeProjectAction(ctx context.Context, project, action string) error {
	return c.composeAction(ctx, project, "", action)
}

func (c *Client) ComposeServiceAction(ctx context.Context, project, service, action string) error {
	return c.composeAction(ctx, project, service, action)
}

func (c *Client) composeAction(ctx context.Context, project, service, action string) error {
	ids, err := c.composeContainerIDs(ctx, project, service)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return fmt.Errorf("no containers found for project %q", project)
	}
	var firstErr error
	for _, id := range ids {
		var opErr error
		switch action {
		case "start":
			opErr = c.StartContainer(ctx, id)
		case "stop":
			opErr = c.StopContainer(ctx, id)
		case "restart":
			opErr = c.RestartContainer(ctx, id)
		}
		if opErr != nil && firstErr == nil {
			firstErr = opErr
		}
	}
	return firstErr
}
