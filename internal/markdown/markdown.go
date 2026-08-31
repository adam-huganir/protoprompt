// Package markdown renders message text to escaped HTML for display.
package markdown

import (
	"io"

	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/ast"
	"github.com/gomarkdown/markdown/html"
)

// Render converts markdown text to HTML. Raw HTML in the source is escaped
// rather than passed through (the default renderer passes it through), so the
// output is safe to inject into a page; do not enable raw-HTML passthrough here.
func Render(text string) string {
	opts := html.RendererOptions{
		Flags:          html.CommonFlags,
		RenderNodeHook: escapeRawHTML,
	}
	return string(markdown.ToHTML([]byte(text), nil, html.NewRenderer(opts)))
}

// escapeRawHTML intercepts raw-HTML nodes and emits them escaped instead of
// passing them through. Returning didHandle=true suppresses the default
// (unescaped) rendering.
func escapeRawHTML(w io.Writer, node ast.Node, entering bool) (ast.WalkStatus, bool) {
	switch n := node.(type) {
	case *ast.HTMLBlock:
		if entering {
			html.EscapeHTML(w, n.Literal)
			w.Write([]byte("\n"))
		}
		return ast.SkipChildren, true
	case *ast.HTMLSpan:
		html.EscapeHTML(w, n.Literal)
		return ast.GoToNext, true
	default:
		return ast.GoToNext, false
	}
}
