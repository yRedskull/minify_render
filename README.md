### Configuração inicial
```go
var err_render error
	minify_render.RENDERER, err_render = minify_render.NewRendererWithFuncs("./templates/*.html", "v1", 2*time.Minute, 600, template.FuncMap{})
	if err_render != nil {
		log.Fatalf("failed init renderer: %v", err_render)
	}
```


### Utilização

```go
func View(c *gin.Context) {
	minify_render.RENDERER.Render(http.StatusOK, c, "index.html", nil)
}
```