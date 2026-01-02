package minify_render

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"html/template"
	"io"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	lru "github.com/hashicorp/golang-lru"
	"github.com/tdewolff/minify/v2"
	mincss "github.com/tdewolff/minify/v2/css"
	minhtml "github.com/tdewolff/minify/v2/html"
)

var (
	RENDERER *Renderer
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

// --- no NewRendererWithFuncs (apenas adicionar armazenamento da pattern/funcs e store inicial) ---
func NewRendererWithFuncs(pattern, version string, ttl time.Duration, cacheSize int, funcs template.FuncMap) (*Renderer, error) {
	// --- parse initial templates as before ---
	root := template.New("").Funcs(funcs)
	tmpl, err := root.ParseGlob(pattern)
	if err != nil {
		return nil, err
	}

	m := minify.New()
	m.AddFunc("text/css", mincss.Minify)
	htmlMin := &minhtml.Minifier{
		KeepSpecialComments: true,
		KeepDocumentTags:        true,
		KeepWhitespace:          false,
	}
	m.Add("text/html", htmlMin)


	var c *lru.Cache
	if cacheSize > 0 {
		c, err = lru.New(cacheSize)
		if err != nil {
			return nil, err
		}
	} else {
		c = nil
	}

	r := &Renderer{
		templatePattern: pattern,
		funcMap:         funcs,
		autoReload:      false,
		minifier:        m,
		cache:           c,
		templateVersion: version,
		ttl:             ttl,
	}
	// store initial template atomically
	r.templatesVal.Store(tmpl)

	return r, nil
}

// ReloadTemplates reparseia os templates e substitui de forma atômica.
// Chame ClearCache() após esta chamada para evitar servir HTML antigo.
func (r *Renderer) ReloadTemplates() error {
	root := template.New("").Funcs(r.funcMap)
	tmpl, err := root.ParseGlob(r.templatePattern)
	if err != nil {
		return err
	}
	r.templatesVal.Store(tmpl)
	// evict cache to avoid serving stale pages
	r.ClearCache()
	return nil
}

func (r *Renderer) currentTemplate() *template.Template {
	v := r.templatesVal.Load()
	if v == nil {
		return nil
	}
	return v.(*template.Template)
}

// helper: checa se If-None-Match contains the ETag (handles multiple values)
func inmMatches(inm string, etag string) bool {
	if inm == "" {
		return false
	}
	// remove whitespace and split on commas
	parts := strings.Split(inm, ",")
	target := `W/"` + etag + `"`
	for _, p := range parts {
		if strings.TrimSpace(p) == target || strings.TrimSpace(p) == `"`+etag+`"` {
			return true
		}
	}
	return false
}

func (r *Renderer) RenderOnlyGet(status_http int, c *gin.Context, name string, data any) {
	if IsDebugMode() {
		if err := r.ReloadTemplates(); err != nil {
			log.Printf("reload templates failed: %v", err)
		}
	}

	if c.Request.Method != http.MethodGet {
    	tmpl := r.currentTemplate()
		
		if tmpl == nil {
			log.Printf("no template loaded")
			c.String(http.StatusInternalServerError, "template error")
			return
		}

		if err := tmpl.ExecuteTemplate(c.Writer, name, data); err != nil {
			log.Printf("template execute error: %v", err)
			c.String(http.StatusInternalServerError, "template render error")
		}
		return
	}

	r.Render(status_http, c, name, data)
}

func (r *Renderer) Render(status_http int,c *gin.Context, name string, data any) {
	if IsDebugMode() {
		if err := r.ReloadTemplates(); err != nil {
			log.Printf("reload templates failed: %v", err)
		}
	}

	contentType := "text/html; charset=utf-8"

	key := c.Request.URL.Path + "?" + c.Request.URL.RawQuery + "|tmpl:" + name + "|v:" + r.templateVersion

	// If cache is enabled, attempt read path
	if r.cache != nil {
		if v, ok := r.cache.Get(key); ok {
			ci := v.(CachedItem)
			if time.Since(ci.CreatedAt) < r.ttl {
				if inmMatches(c.GetHeader("If-None-Match"), ci.ETag) {
					c.Status(http.StatusNotModified)
					return
				}
				
				c.Header("Content-Type", contentType)
				c.Header("ETag", `W/"`+ci.ETag+`"`)
				c.Header("Cache-Control", "public, max-age=60")
				c.Header("Vary", "Accept-Encoding")
				c.Writer.WriteHeader(status_http)
				_, _ = c.Writer.Write(ci.Body)
				return
			}
			// expired -> evict
			r.cache.Remove(key)
		}
	}

	// render to buffer
	buf := &bytes.Buffer{}
	
    tmpl := r.currentTemplate()
	if tmpl == nil {
		log.Printf("no template loaded")
		c.String(http.StatusInternalServerError, "template error")
		return
	}
	if err := tmpl.ExecuteTemplate(buf, name, data); err != nil {
		log.Printf("template execute error: %v", err)
		c.String(http.StatusInternalServerError, "template render error")
		return
	}

	// minify
	dst := &bytes.Buffer{}
	if err := r.minifier.Minify("text/html", dst, bytes.NewReader(buf.Bytes())); err != nil {
		log.Printf("minify error (fallback): %v", err)
		dst.Reset()
		_, _ = io.Copy(dst, bytes.NewReader(buf.Bytes()))
	}

	// If cache enabled -> compute etag, set headers and store to cache
	if r.cache != nil {
		sum := sha256.Sum256(dst.Bytes())
		etag := hex.EncodeToString(sum[:])
		

		c.Header("Content-Type", contentType)
		c.Header("ETag", `W/"`+etag+`"`)
		c.Header("Cache-Control", "public, max-age=60")
		c.Header("Vary", "Accept-Encoding")

		ci := CachedItem{
			Body:        dst.Bytes(),
			ETag:        etag,
			ContentType: contentType,
			CreatedAt:   time.Now(),
		}
		r.cache.Add(key, ci)

		c.Writer.WriteHeader(status_http)
		_, _ = io.Copy(c.Writer, bytes.NewReader(dst.Bytes()))
		return
	}

	c.Header("Content-Type", contentType)
	c.Writer.WriteHeader(status_http)
	_, _ = io.Copy(c.Writer, bytes.NewReader(dst.Bytes()))
}

// ClearCache limpa todo o cache (se houver)
func (r *Renderer) ClearCache() {
	if r.cache == nil {
		return
	}
	r.cache.Purge()
}

// DisableCache desabilita cache em runtime (remove o objeto LRU)
func (r *Renderer) DisableCache() {
	r.cache = nil
}

// InvalidateKey remove uma chave específica (se cache ativo)
func (r *Renderer) InvalidateKey(key string) {
	if r.cache == nil {
		return
	}
	r.cache.Remove(key)
}