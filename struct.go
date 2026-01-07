package minify_render

import (
	"html/template"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru"
	"github.com/tdewolff/minify/v2"
)

// CachedItem guarda o corpo minificado e o etag
type CachedItem struct {
	Body        []byte
	ETag        string
	ContentType string
	CreatedAt   time.Time
}

type Renderer struct {
	// old field removed or kept for backward compat — we'll use atomic storage
	// templates *template.Template   // NOT used directly anymore
	templatesVal    atomic.Value // stores *template.Template
	templatePattern string
	funcMap         template.FuncMap
	autoReload      bool

	minifier        *minify.M
	cache           *lru.Cache
	templateVersion string
	ttl             time.Duration
}

type RenderParams struct {
	StatusHttp int
	Template   string
	Data       any
}
