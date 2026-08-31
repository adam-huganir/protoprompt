package api

import (
	"protoprompt/internal/markdown"
	"protoprompt/internal/store"
)

// renderHTML fills m.Html with the escaped markdown rendering of m.Text for
// JSON payloads. Messages have no html column; it is computed in memory at
// serialization time only, so every message that becomes API JSON carries it.
func renderHTML(m *store.Message) {
	m.Html = markdown.Render(m.Text)
}

// renderHTMLAll renders every message in msgs (in place).
func renderHTMLAll(msgs []store.Message) {
	for i := range msgs {
		renderHTML(&msgs[i])
	}
}
