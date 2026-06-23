package handlers

import (
	"net/http"
	"os"
	"sort"
	"strconv"

	"github.com/dockertab/agent/internal/docker"
	"github.com/gin-gonic/gin"
)

// ── Legacy compose endpoints (backward compatible) ──────────────────────────

func (h *Handler) ListComposeProjects(c *gin.Context) {
	projects, err := h.Docker.ListComposeProjects(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"projects": projects,
		"count":    len(projects),
	})
}

func (h *Handler) GetComposeProject(c *gin.Context) {
	project := c.Param("project")
	projects, err := h.Docker.ListComposeProjects(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for _, p := range projects {
		if p.Name == project {
			c.JSON(http.StatusOK, p)
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
}

func (h *Handler) ComposeProjectStart(c *gin.Context)   { h.composeProjectAction(c, "start") }
func (h *Handler) ComposeProjectStop(c *gin.Context)    { h.composeProjectAction(c, "stop") }
func (h *Handler) ComposeProjectRestart(c *gin.Context) { h.composeProjectAction(c, "restart") }

func (h *Handler) composeProjectAction(c *gin.Context, action string) {
	project := c.Param("project")
	if err := h.Docker.ComposeProjectAction(c.Request.Context(), project, action); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "project " + action, "project": project})
}

func (h *Handler) ComposeServiceStart(c *gin.Context)   { h.composeServiceAction(c, "start") }
func (h *Handler) ComposeServiceStop(c *gin.Context)    { h.composeServiceAction(c, "stop") }
func (h *Handler) ComposeServiceRestart(c *gin.Context) { h.composeServiceAction(c, "restart") }

func (h *Handler) composeServiceAction(c *gin.Context, action string) {
	project := c.Param("project")
	service := c.Param("service")
	if err := h.Docker.ComposeServiceAction(c.Request.Context(), project, service, action); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "service " + action, "project": project, "service": service})
}

// ── Compose stacks API ───────────────────────────────────────────────────────

type composeStack struct {
	Name         string                 `json:"name"`
	Managed      bool                   `json:"managed"`
	State        string                 `json:"state"`
	Services     []docker.ComposeService `json:"services"`
	RunningCount int                    `json:"running_count"`
	TotalCount   int                    `json:"total_count"`
	ConfigFile string `json:"config_file,omitempty"`
}

func (h *Handler) requireCompose(c *gin.Context) bool {
	if h.ComposeStore == nil || h.ComposeExecutor == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "compose management not available"})
		return false
	}
	return true
}

func (h *Handler) buildStackList(c *gin.Context) ([]composeStack, error) {
	dockerProjects, err := h.Docker.ListComposeProjects(c.Request.Context())
	if err != nil {
		return nil, err
	}

	managedNames, err := h.ComposeStore.List()
	if err != nil {
		return nil, err
	}

	managedSet := make(map[string]bool, len(managedNames))
	for _, n := range managedNames {
		managedSet[n] = true
	}

	seen := make(map[string]bool)
	var stacks []composeStack

	for _, p := range dockerProjects {
		stacks = append(stacks, composeStack{
			Name:         p.Name,
			Managed:      managedSet[p.Name],
			State:        p.State,
			Services:     p.Services,
			RunningCount: p.RunningCount,
			TotalCount:   p.TotalCount,
			ConfigFile:   p.ConfigFile,
		})
		seen[p.Name] = true
	}

	// Managed stacks not yet running appear as stopped.
	for _, name := range managedNames {
		if !seen[name] {
			stacks = append(stacks, composeStack{
				Name:    name,
				Managed: true,
				State:   "stopped",
			})
		}
	}

	sort.Slice(stacks, func(i, j int) bool {
		return stacks[i].Name < stacks[j].Name
	})
	return stacks, nil
}

func (h *Handler) ListComposeStacks(c *gin.Context) {
	if !h.requireCompose(c) {
		return
	}
	stacks, err := h.buildStackList(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"stacks": stacks, "count": len(stacks)})
}

type createStackRequest struct {
	Name    string `json:"name" binding:"required"`
	Content string `json:"content" binding:"required"`
	Deploy  bool   `json:"deploy"`
}

func (h *Handler) CreateComposeStack(c *gin.Context) {
	if !h.requireCompose(c) {
		return
	}
	var req createStackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if err := h.ComposeStore.Create(req.Name, req.Content); err != nil {
		if h.ComposeStore.Exists(req.Name) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		}
		return
	}

	resp := gin.H{"name": req.Name, "managed": true, "message": "stack created"}

	if req.Deploy {
		output, err := h.ComposeExecutor.Up(c.Request.Context(), req.Name)
		if err != nil {
			resp["deploy_error"] = err.Error()
			resp["output"] = output
			c.JSON(http.StatusCreated, resp)
			return
		}
		resp["output"] = output
	}

	c.JSON(http.StatusCreated, resp)
}

func (h *Handler) GetComposeStack(c *gin.Context) {
	if !h.requireCompose(c) {
		return
	}
	name := c.Param("name")
	stacks, err := h.buildStackList(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for _, s := range stacks {
		if s.Name == name {
			c.JSON(http.StatusOK, s)
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "stack not found"})
}

func (h *Handler) DeleteComposeStack(c *gin.Context) {
	if !h.requireCompose(c) {
		return
	}
	name := c.Param("name")
	if !h.ComposeStore.Exists(name) {
		c.JSON(http.StatusNotFound, gin.H{"error": "stack not found or not managed by agent"})
		return
	}
	if err := h.ComposeStore.Delete(name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"name": name, "message": "stack deleted"})
}

func (h *Handler) GetComposeStackFile(c *gin.Context) {
	if !h.requireCompose(c) {
		return
	}
	name := c.Param("name")

	if h.ComposeStore.Exists(name) {
		content, err := h.ComposeStore.Read(name)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"name": name, "content": content, "readonly": false})
		return
	}

	stacks, err := h.buildStackList(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for _, s := range stacks {
		if s.Name == name && s.ConfigFile != "" {
			data, err := os.ReadFile(s.ConfigFile)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read compose file: " + err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"name": name, "content": string(data), "readonly": true})
			return
		}
	}

	c.JSON(http.StatusNotFound, gin.H{"error": "stack not found or compose file not available"})
}

type updateFileRequest struct {
	Content string `json:"content" binding:"required"`
	Deploy  bool   `json:"deploy"`
}

func (h *Handler) UpdateComposeStackFile(c *gin.Context) {
	if !h.requireCompose(c) {
		return
	}
	name := c.Param("name")
	var req updateFileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if err := h.ComposeStore.Write(name, req.Content); err != nil {
		if h.ComposeStore.Exists(name) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		} else {
			c.JSON(http.StatusNotFound, gin.H{"error": "stack not found or not managed by agent"})
		}
		return
	}

	resp := gin.H{"name": name, "message": "file updated"}

	if req.Deploy {
		output, err := h.ComposeExecutor.Up(c.Request.Context(), name)
		if err != nil {
			resp["deploy_error"] = err.Error()
			resp["output"] = output
			c.JSON(http.StatusOK, resp)
			return
		}
		resp["output"] = output
	}

	c.JSON(http.StatusOK, resp)
}

func (h *Handler) ComposeStackUp(c *gin.Context) {
	if !h.requireCompose(c) {
		return
	}
	name := c.Param("name")
	output, err := h.ComposeExecutor.Up(c.Request.Context(), name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "output": output})
		return
	}
	c.JSON(http.StatusOK, gin.H{"name": name, "message": "stack up", "output": output})
}

func (h *Handler) ComposeStackDown(c *gin.Context) {
	if !h.requireCompose(c) {
		return
	}
	name := c.Param("name")
	output, err := h.ComposeExecutor.Down(c.Request.Context(), name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "output": output})
		return
	}
	c.JSON(http.StatusOK, gin.H{"name": name, "message": "stack down", "output": output})
}

func (h *Handler) ComposeStackPull(c *gin.Context) {
	if !h.requireCompose(c) {
		return
	}
	name := c.Param("name")
	output, err := h.ComposeExecutor.Pull(c.Request.Context(), name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "output": output})
		return
	}
	c.JSON(http.StatusOK, gin.H{"name": name, "message": "pull complete", "output": output})
}

func (h *Handler) ComposeStackStart(c *gin.Context) {
	h.composeStackSDKAction(c, "start")
}

func (h *Handler) ComposeStackStop(c *gin.Context) {
	h.composeStackSDKAction(c, "stop")
}

func (h *Handler) ComposeStackRestart(c *gin.Context) {
	h.composeStackSDKAction(c, "restart")
}

func (h *Handler) composeStackSDKAction(c *gin.Context, action string) {
	if !h.requireCompose(c) {
		return
	}
	name := c.Param("name")
	if err := h.Docker.ComposeProjectAction(c.Request.Context(), name, action); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"name": name, "message": "stack " + action})
}

func (h *Handler) GetComposeStackLogs(c *gin.Context) {
	if !h.requireCompose(c) {
		return
	}
	name := c.Param("name")
	lines := 100
	if n, err := strconv.Atoi(c.DefaultQuery("lines", "100")); err == nil && n > 0 && n <= 5000 {
		lines = n
	}
	logs, err := h.ComposeExecutor.Logs(c.Request.Context(), name, lines)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "logs": logs})
		return
	}
	c.JSON(http.StatusOK, gin.H{"name": name, "logs": logs, "lines": lines})
}
