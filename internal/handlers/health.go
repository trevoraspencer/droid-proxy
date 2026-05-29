package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"droid-proxy/internal/version"
)

// Health returns a basic liveness response.
func Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"service": "droid-proxy",
		"version": version.Version,
	})
}
