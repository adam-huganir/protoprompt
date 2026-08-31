package markdown

import (
	"strings"
	"testing"
)

func TestRenderEscapesRawHTML(t *testing.T) {
	out := Render("<script>alert(1)</script>")
	if strings.Contains(out, "<script>") {
		t.Fatalf("raw HTML block not escaped: %q", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("expected escaped script tag, got %q", out)
	}

	out = Render("hello <b>world</b> there")
	if strings.Contains(out, "<b>") {
		t.Fatalf("inline HTML span not escaped: %q", out)
	}
	if !strings.Contains(out, "world") {
		t.Fatalf("content lost: %q", out)
	}

	out = Render("a < b && c > d")
	if strings.Contains(out, "< b") || strings.Contains(out, "> d") {
		t.Fatalf("angle brackets in text not escaped: %q", out)
	}
	if !strings.Contains(out, "a &lt; b &amp;&amp; c &gt; d") {
		t.Fatalf("unexpected escaping of plain text: %q", out)
	}
}

func TestRenderCodeKeepsEscapedAngleBrackets(t *testing.T) {
	out := Render("```go\nif a < b && c > d {\n}\n```\n")
	if !strings.Contains(out, "<pre><code") {
		t.Fatalf("expected code block, got %q", out)
	}
	if !strings.Contains(out, "a &lt; b &amp;&amp; c &gt; d") {
		t.Fatalf("code block content not escaped exactly once: %q", out)
	}
	if strings.Contains(out, "&amp;lt;") {
		t.Fatalf("double-escaped code content: %q", out)
	}
}

func TestRenderCommonConstructs(t *testing.T) {
	out := Render("# Title\n\nSome **bold** and *italic* text.\n\n- one\n- two\n\n`x<y`\n\n[link](https://example.com)\n")
	for _, want := range []string{
		"<h1>", "Title</h1>",
		"<strong>bold</strong>", "<em>italic</em>",
		"<ul>", "<li>one</li>", "<li>two</li>",
		"<code>x&lt;y</code>",
		`<a href="https://example.com">link</a>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderEmpty(t *testing.T) {
	if got := Render(""); got != "" {
		t.Fatalf("expected empty output, got %q", got)
	}
}
