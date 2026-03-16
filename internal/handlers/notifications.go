package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type registerTokenRequest struct {
	DeviceToken string `json:"device_token" binding:"required"`
	Environment string `json:"environment"` // "development" | "production"
}

func (h *Handler) RegisterDeviceToken(c *gin.Context) {
	if h.TokenStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "push notifications not configured on this agent"})
		return
	}

	var req registerTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "device_token is required"})
		return
	}

	env := req.Environment
	if env != "development" && env != "production" {
		env = "development"
	}

	deviceID := c.GetString("device_id")
	h.TokenStore.Register(deviceID, req.DeviceToken, env)

	if h.RelayRegisterToken != nil {
		h.RelayRegisterToken(deviceID, req.DeviceToken, env)
	}

	c.JSON(http.StatusOK, gin.H{"registered": true, "environment": env})
}

func (h *Handler) UnregisterDeviceToken(c *gin.Context) {
	if h.TokenStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "push notifications not configured on this agent"})
		return
	}

	deviceID := c.GetString("device_id")
	h.TokenStore.Unregister(deviceID)
	c.JSON(http.StatusOK, gin.H{"unregistered": true})
}
