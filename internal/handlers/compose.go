package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

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
	name := c.Param("project")
	projects, err := h.Docker.ListComposeProjects(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for _, p := range projects {
		if p.Name == name {
			c.JSON(http.StatusOK, p)
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
}

func (h *Handler) StartComposeService(c *gin.Context) {
	project, service := c.Param("project"), c.Param("service")
	if err := h.Docker.StartComposeService(c.Request.Context(), project, service); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "service started", "project": project, "service": service})
}

func (h *Handler) StopComposeService(c *gin.Context) {
	project, service := c.Param("project"), c.Param("service")
	if err := h.Docker.StopComposeService(c.Request.Context(), project, service); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "service stopped", "project": project, "service": service})
}

func (h *Handler) RestartComposeService(c *gin.Context) {
	project, service := c.Param("project"), c.Param("service")
	if err := h.Docker.RestartComposeService(c.Request.Context(), project, service); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "service restarted", "project": project, "service": service})
}
