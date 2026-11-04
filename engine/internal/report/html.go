package report

import (
	_ "embed"
	"fmt"
	"html/template"
	"io"
)

// viewerTemplate is the whole page: inline CSS, no script, no external
// resources. It is parsed with html/template, so every report string is escaped
// for the context it lands in. The package never converts report data to
// template.HTML, template.JS, template.URL or template.CSS.
//
//go:embed viewer.html.tmpl
var viewerTemplate string

var viewerPage = template.Must(template.New("viewer").Parse(viewerTemplate))

// RenderHTML writes the page for v.
func RenderHTML(w io.Writer, v *View) error {
	if err := viewerPage.Execute(w, v); err != nil {
		return fmt.Errorf("render report page: %w", err)
	}
	return nil
}
