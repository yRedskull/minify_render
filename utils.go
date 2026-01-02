package minify_render

import "github.com/gin-gonic/gin"

func IsDebugMode() bool {
	return gin.Mode() == gin.DebugMode
}
