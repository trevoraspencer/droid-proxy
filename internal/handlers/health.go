package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/trevoraspencer/droid-proxy/internal/version"
)

// Health returns a basic liveness response.
func Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"service": "droid-proxy",
		"version": version.Version,
	})
}
