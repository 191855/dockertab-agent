package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type registerTokenRequest struct {
	DeviceToken string `json:"device_token" binding:"required"`
	Environment string `json:"environment"` // "development" | "production"
}

// RegisterDeviceToken stores the caller's APNs token for push notifications.
// Requires JWT auth (device_id extracted from claims by middleware).
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

	// If relay is configured, forward the token so the relay can push notifications
	// to this device even when it connects via LAN or Tailscale (not relay WebSocket).
	if h.RelayRegisterToken != nil {
		h.RelayRegisterToken(deviceID, req.DeviceToken, env)
	}

	c.JSON(http.StatusOK, gin.H{"registered": true, "environment": env})
}

// UnregisterDeviceToken removes the caller's APNs token.
func (h *Handler) UnregisterDeviceToken(c *gin.Context) {
	if h.TokenStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "push notifications not configured on this agent"})
		return
	}

	deviceID := c.GetString("device_id")
	h.TokenStore.Unregister(deviceID)
	c.JSON(http.StatusOK, gin.H{"unregistered": true})
}
