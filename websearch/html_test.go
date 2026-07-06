// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package websearch

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractReadable_BasicHTML(t *testing.T) {
	t.Parallel()

	html := `<html><head><title>Test Page</title></head>
<body><p>Hello <b>world</b></p></body></html>`

	page := ExtractReadable(html, 0)

	require.Equal(t, "Test Page", page.Title)
	assert.Contains(t, page.Text, "Hello world")
}

func TestExtractReadable_StripsScript(t *testing.T) {
	t.Parallel()

	html := `<html><body>
<script>alert('xss')</script>
<p>Visible</p></body></html>`

	page := ExtractReadable(html, 0)

	assert.NotContains(t, page.Text, "alert")
	assert.Contains(t, page.Text, "Visible")
}

func TestExtractReadable_StripsStyle(t *testing.T) {
	t.Parallel()

	html := `<html><body>
<style>body { color: red; }</style>
<p>Content</p></body></html>`

	page := ExtractReadable(html, 0)

	assert.NotContains(t, page.Text, "color")
	assert.Contains(t, page.Text, "Content")
}

func TestExtractReadable_StripsAllBlockTags(t *testing.T) {
	t.Parallel()

	html := `<html><body>
<script>js</script>
<style>css</style>
<noscript>nojs</noscript>
<template>tmpl</template>
<svg><circle/></svg>
<iframe>frame</iframe>
<nav>nav</nav>
<footer>foot</footer>
<header>head</header>
<aside>side</aside>
<form><input/></form>
<p>Keep this</p></body></html>`

	page := ExtractReadable(html, 0)

	assert.NotContains(t, page.Text, "js")
	assert.NotContains(t, page.Text, "css")
	assert.NotContains(t, page.Text, "nojs")
	assert.NotContains(t, page.Text, "tmpl")
	assert.NotContains(t, page.Text, "circle")
	assert.NotContains(t, page.Text, "frame")
	assert.NotContains(t, page.Text, "nav")
	assert.NotContains(t, page.Text, "foot")
	assert.NotContains(t, page.Text, "head")
	assert.NotContains(t, page.Text, "side")
	assert.NotContains(t, page.Text, "input")
	assert.Contains(t, page.Text, "Keep this")
}

func TestExtractReadable_ExtractsTitle(t *testing.T) {
	t.Parallel()

	html := `<html><head><title>My Title &amp; More</title></head><body></body></html>`

	page := ExtractReadable(html, 0)

	assert.Equal(t, "My Title & More", page.Title)
}

func TestExtractReadable_NoTitle(t *testing.T) {
	t.Parallel()

	html := `<html><body><p>No title here</p></body></html>`

	page := ExtractReadable(html, 0)

	assert.Empty(t, page.Title)
}

func TestExtractReadable_DecodesEntities(t *testing.T) {
	t.Parallel()

	html := `<p>&amp; &lt; &gt; &quot; &#39; &nbsp; &#65; &#x42;</p>`

	page := ExtractReadable(html, 0)

	assert.Contains(t, page.Text, `& < > " '`)
	assert.Contains(t, page.Text, "A")
	assert.Contains(t, page.Text, "B")
}

func TestExtractReadable_ExtractsLinks(t *testing.T) {
	t.Parallel()

	html := `<html><body>
<a href="https://example.com">Example</a>
<a href="https://test.com/page">Test Page</a>
</body></html>`

	page := ExtractReadable(html, 0)

	require.Len(t, page.Links, 2)
	assert.Equal(t, "Example", page.Links[0].Text)
	assert.Equal(t, "https://example.com", page.Links[0].Href)
	assert.Equal(t, "Test Page", page.Links[1].Text)
	assert.Equal(t, "https://test.com/page", page.Links[1].Href)
}

func TestExtractReadable_SkipsJavascriptLinks(t *testing.T) {
	t.Parallel()

	html := `<a href="javascript:alert(1)">Click</a><a href="https://ok.com">OK</a>`

	page := ExtractReadable(html, 0)

	require.Len(t, page.Links, 1)
	assert.Equal(t, "https://ok.com", page.Links[0].Href)
}

func TestExtractReadable_SkipsFragmentLinks(t *testing.T) {
	t.Parallel()

	html := `<a href="#section">Jump</a><a href="https://ok.com">OK</a>`

	page := ExtractReadable(html, 0)

	require.Len(t, page.Links, 1)
	assert.Equal(t, "https://ok.com", page.Links[0].Href)
}

func TestExtractReadable_SkipsEmptyLinks(t *testing.T) {
	t.Parallel()

	html := `<a href="https://example.com">  </a><a href="https://ok.com">OK</a>`

	page := ExtractReadable(html, 0)

	require.Len(t, page.Links, 1)
	assert.Equal(t, "OK", page.Links[0].Text)
}

func TestExtractReadable_LimitsFiftyLinks(t *testing.T) {
	t.Parallel()

	var buf strings.Builder

	for idx := range 60 {
		digit := string(rune('0' + idx%10))
		buf.WriteString("<a href=\"https://example.com/")
		buf.WriteString(digit)
		buf.WriteString("\">Link ")
		buf.WriteString(digit)
		buf.WriteString("</a>\n")
	}

	page := ExtractReadable(buf.String(), 0)

	assert.Len(t, page.Links, 50)
}

func TestExtractReadable_TruncatesText(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	for range 200 {
		buf.WriteString("word ")
	}

	html := "<p>" + buf.String() + "</p>"

	page := ExtractReadable(html, 50)

	assert.LessOrEqual(t, len(page.Text), 53)
	assert.Truef(t, strings.HasSuffix(page.Text, "…"), "expected text to end with ellipsis")
}

func TestExtractReadable_CollapsesWhitespace(t *testing.T) {
	t.Parallel()

	html := `<p>  lots   of    spaces   </p>`

	page := ExtractReadable(html, 0)

	assert.Contains(t, page.Text, "lots of spaces")
}

func TestExtractReadable_BlockBreaks(t *testing.T) {
	t.Parallel()

	html := `<p>Para 1</p><p>Para 2</p><div>Div</div><br>New line`

	page := ExtractReadable(html, 0)

	assert.Contains(t, page.Text, "Para 1")
	assert.Contains(t, page.Text, "Para 2")
	assert.Contains(t, page.Text, "Div")
	assert.Contains(t, page.Text, "New line")
}

func TestExtractReadable_EmptyHTML(t *testing.T) {
	t.Parallel()

	page := ExtractReadable("", 0)

	assert.Empty(t, page.Title)
	assert.Empty(t, page.Text)
	assert.Empty(t, page.Links)
}

func TestExtractReadable_MaxCharsZero(t *testing.T) {
	t.Parallel()

	html := `<p>Some text here</p>`

	page := ExtractReadable(html, 0)

	assert.Contains(t, page.Text, "Some text here")
}

func TestDecodeEntities_Named(t *testing.T) {
	t.Parallel()

	tests := []struct{ in, want string }{
		{"&amp;", "&"},
		{"&lt;", "<"},
		{"&gt;", ">"},
		{"&quot;", `"`},
		{"&#39;", "'"},
		{"&nbsp;", " "},
	}

	for _, tc := range tests {
		assert.Equal(t, tc.want, decodeEntities(tc.in))
	}
}

func TestDecodeEntities_NumericDecimal(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "A", decodeEntities("&#65;"))
	assert.Equal(t, "Z", decodeEntities("&#90;"))
}

func TestDecodeEntities_NumericHex(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "A", decodeEntities("&#x41;"))
	assert.Equal(t, "a", decodeEntities("&#x61;"))
}

func TestStripTags(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "hello world", stripTags("<b>hello</b> <i>world</i>"))
	assert.Equal(t, "text", stripTags("text"))
	assert.Empty(t, stripTags("<br>"))
}
