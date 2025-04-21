package entropy

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	mathrand "math/rand"
	"net/http"
	"path/filepath"

	"github.com/gorilla/csrf"
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

func dummyCSRFField() template.HTML {
	return template.HTML("")
}

var ctas []string = []string{
	"shout into the void",
	"SHOUT INTO THE VOID",
	"join the chaos",
	"tell 'em what it is",
	"what's up?",
	"what up doe",
	"say something",
	"make some noise!!",
	"say your piece",
	"give your two cents",
	"add to the conversation",
	"join the discourse",
	"add your voice",
	"what's bothering you?",
	"what's something you need to get off your chest?",
	"what's on your mind?",
	"anything on your mind?",
	"got any ideas?",
	"what do you think?",
	"write a post",
	"give it a go",
	"everyone is talking about...",
	"no one is talking about...",
	"tell me something I don't know:",
	"what you got?",
	"go off:",
	"keep it real:",
	"ADFOIJRTHOI%#MBOIADFMBIVJ",
}

func postCallToAction() string {
	i := mathrand.Intn(len(ctas))
	return ctas[i]
}

func (r *Renderer) ExecuteTemplate(w http.ResponseWriter, req *http.Request, name string, data any) error {
	// We render to a buffer (from the buffer pool) so that we can handle template
	// execution errors (without sending half a template response first).
	t := r.templates[name]
	if t == nil {
		return fmt.Errorf("ExecuteTemplate: template not found: %v; templates=%+v", name, r.templates)
	}
	tclone := template.Must(t.Clone())
	csrfField := csrf.TemplateField(req)
	tclone.Funcs(template.FuncMap{"csrf_field": func() template.HTML { return csrfField }})

	buf := r.bufpool.Get()
	defer r.bufpool.Put(buf)
	if err := tclone.ExecuteTemplate(w, r.baseTemplateName, data); err != nil {
		return err
	}
	buf.WriteTo(w)
	return nil
}

const baseTemplatePath = "templates/base.html"

//go:embed templates/*.html
//go:embed templates/components/*.html
var templateFS embed.FS

func NewRenderer() (*Renderer, error) {
	renderer := Renderer{
		templates:        make(map[string]*template.Template),
		baseTemplateName: filepath.Base(baseTemplatePath),
		bufpool:          bpool.NewBufferPool(48),
	}
	paths, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	// An awful lot of template.Must going on here...
	// That's probably fine. Crashing on startup is kinda what we want anyway.
	//
	// We include the helpers in templates/components/ too, so that everything defined
	// there is usable in the child templates.
	baseTemplate := template.New("")
	baseTemplate.Funcs(template.FuncMap{"csrf_field": dummyCSRFField, "post_cta": postCallToAction})
	template.Must(baseTemplate.ParseFS(templateFS, "templates/components/*.html", baseTemplatePath))
	// We override this func at execution time
	for _, path := range paths {
		name := filepath.Base(path)
		t := template.Must(baseTemplate.Clone())
		t = template.Must(t.ParseFS(templateFS, path))
		renderer.templates[name] = t
	}
	return &renderer, nil
}
