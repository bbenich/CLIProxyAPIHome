package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// GetQuotaRouting exposes a read-only global priority view of the live scheduler.
// Group filtering is deliberately presentation-only and never renumbers ranks.
func (h *Handler) GetQuotaRouting(c *gin.Context) {
	if h.runtime == nil || h.runtime.CoreManager() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "routing runtime unavailable"})
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, h.runtime.RoutingObservation(ctx))
}
