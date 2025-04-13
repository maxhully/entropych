package entropy

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"path/filepath"

	"github.com/oxtoacart/bpool"
)

// This is a little abstraction around template.Template to make a "base" layout
// template work.
//
// This is inspired mainly by staring at the pkgsite source code:
// https://github.com/golang/pkgsite/blob/master/internal/frontend/templates/templates.go
type Renderer struct {
	templates        map[string]*template.Template
	baseTemplateName string
	bufpool          *bpool.BufferPool
}

func (r *Renderer) ExecuteTemplate(w io.Writer, name string, data any) error {
	// We render to a buffer (from the buffer pool) so that we can handle template
	// execution errors (without sending half a template response first).
	t := r.templates[name]
	buf := r.bufpool.Get()
	defer r.bufpool.Put(buf)
	err := t.ExecuteTemplate(w, r.baseTemplateName, data)
	if err != nil {
		return err
	}
	buf.WriteTo(w)
	return nil
}

const baseTemplatePath = "templates/base.html"

//go:embed templates/*.html
var templateFS embed.FS

func NewRenderer() (*Renderer, error) {
	renderer := Renderer{
		templates:        make(map[string]*template.Template),
		baseTemplateName: filepath.Base(baseTemplatePath),
		bufpool:          bpool.NewBufferPool(48),
	}
	paths, err := fs.Glob(templateFS, "templates/*.html")
	fmt.Printf("paths: %v\n", paths)
	if err != nil {
		return nil, err
	}
	// An awful lot of template.Must going on here...
	// That's probably fine. Crashing on startup is kinda what we want anyway.
	baseTemplate := template.Must(template.ParseFS(templateFS, baseTemplatePath))
	for _, path := range paths {
		name := filepath.Base(path)
		t := template.Must(baseTemplate.Clone())
		t = template.Must(t.ParseFS(templateFS, path))
		renderer.templates[name] = t
	}
	return &renderer, nil
}
