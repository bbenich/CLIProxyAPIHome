package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (h *Handler) GetQuotaUserScopes(c *gin.Context) {
	ctx, cancel := h.requestContext(c)
	defer cancel()
	if h.repo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "repository unavailable"})
		return
	}
	users, errUsers := h.repo.QuotaUserScopes(ctx)
	if errUsers != nil {
		respondError(c, http.StatusInternalServerError, "quota_user_scopes_unavailable", errUsers)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"users": users})
}
