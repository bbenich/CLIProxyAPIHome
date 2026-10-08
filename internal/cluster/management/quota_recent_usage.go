package management

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

func (h *Handler) GetQuotaRecentUsage(c *gin.Context) {
	ctx, cancel := h.requestContext(c)
	defer cancel()
	if h.repo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "repository unavailable"})
		return
	}
	usage, errUsage := h.repo.QuotaRecentUsage(ctx, time.Now())
	if errUsage != nil {
		respondError(c, http.StatusInternalServerError, "quota_usage_unavailable", errUsage)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, usage)
}
