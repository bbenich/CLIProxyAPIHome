package managementhttp

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Redirect old bookmarks to the quota route inside the native Home layout.
func servePanelNavigation(c *gin.Context) bool {
	if c.Request.URL.Path != "/quota-dashboard.html" {
		return false
	}
	c.Header("Cache-Control", "no-cache")
	c.Redirect(http.StatusFound, "/management.html#/admin/quota")
	return true
}
