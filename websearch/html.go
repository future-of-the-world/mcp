// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package websearch

import (
	"regexp"
	"strconv"
	"strings"
)

// ExtractedLink represents a hyperlink extracted from an HTML document.
type ExtractedLink struct {
	Text string `json:"text" yaml:"text"`
	Href string `json:"href" yaml:"href"`
}

// ExtractedPage holds the result of extracting readable content from HTML.
type ExtractedPage struct {
	Title string          `json:"title" yaml:"title"`
	Text  string          `json:"text"  yaml:"text"`
	Links []ExtractedLink `json:"links" yaml:"links"`
}

// Tags whose entire opening-to-closing block should be stripped.
var stripBlockTags = []string{
	"script", "style", "noscript", "template",
	"svg", "iframe", "nav", "footer", "header", "aside", "form",
}

// Pre-compiled regexes used during extraction.
var (
	reBlockSuffix     = regexp.MustCompile(`(?i)<\s*/(p|div|section|article|li|tr|h[1-6])\s*>`)
	reBR              = regexp.MustCompile(`(?i)<\s*br\s*/?\s*>`)
	reLI              = regexp.MustCompile(`(?i)<\s*li[^>]*>`)
	reTag             = regexp.MustCompile(`<[^>]+>`)
	reTitle           = regexp.MustCompile(`(?i)<title[^>]*>([\s\S]*?)</title>`)
	reLink            = regexp.MustCompile(`(?i)<a\b[^>]*href=["']([^"']+)["'][^>]*>([\s\S]*?)</a>`)
	reSpaceCollapse   = regexp.MustCompile(`[ \t]+`)
	reNewlineCollapse = regexp.MustCompile(`\n{3,}`)
)

const (
	subMatchTitle  = 2
	subMatchLink   = 3
	maxLinks       = 50
	parseBase10    = 10
	parseBits32    = 32
	hexBase        = 16
	octalBase      = 8
	decimalBase    = 10
	hexDigitOffset = 10
	ipv4OctetCount = 4
	bitShift24     = 24
	bitShift16     = 16
	bitShift8      = 8
	ipv4MaxOctet   = 0xff
	ipv4MaxWord    = 0xffff
	ipv4MaxTriple  = 0xffffff
	ipv4PartsMin   = 1
	ipv4PartsMax   = 4
)

// blockTagRe builds a regex that matches an entire tag block (opening to
// closing) for the given tag name.
func blockTagRe(tag string) *regexp.Regexp {
	pattern := `(?i)<` + tag + `\b[^>]*>[\s\S]*?</` + tag + `>`

	return regexp.MustCompile(pattern)
}

// ExtractReadable strips unwanted HTML elements, extracts the page title,
// hyperlinks, and readable text from html. The text is truncated to maxChars
// if it exceeds that length.
func ExtractReadable(html string, maxChars int) ExtractedPage {
	work := html

	work = stripBlockElements(work)

	title := extractTitle(work)

	links := extractLinks(work)

	work = insertBlockBreaks(work)

	text := stripTags(work)

	text = decodeEntities(text)

	text = collapseWhitespace(text)

	if maxChars > 0 && len(text) > maxChars {
		text = strings.TrimRight(text[:maxChars], " \t\n") + "…"
	}

	return ExtractedPage{
		Title: title,
		Text:  text,
		Links: links,
	}
}

// stripBlockElements removes all unwanted tag blocks from html.
func stripBlockElements(html string) string {
	result := html

	for _, tag := range stripBlockTags {
		tagRegex := blockTagRe(tag)

		result = tagRegex.ReplaceAllString(result, " ")
	}

	return result
}

// extractTitle returns the content of the first <title> element, with tags
// stripped and entities decoded.
func extractTitle(html string) string {
	match := reTitle.FindStringSubmatch(html)
	if len(match) < subMatchTitle {
		return ""
	}

	raw := stripTags(match[1])

	decoded := decodeEntities(raw)

	return strings.TrimSpace(decoded)
}

// extractLinks collects up to maxLinks hyperlinks from html that have
// non-empty text and a non-javascript, non-fragment href.
func extractLinks(html string) []ExtractedLink {
	var links []ExtractedLink

	matches := reLink.FindAllStringSubmatch(html, -1)

	for _, match := range matches {
		if len(match) < subMatchLink {
			continue
		}

		href := match[1]

		text := strings.TrimSpace(decodeEntities(stripTags(match[2])))

		if isInvalidLink(text, href) {
			continue
		}

		links = append(links, ExtractedLink{Text: text, Href: href})

		if len(links) >= maxLinks {
			break
		}
	}

	return links
}

// isInvalidLink reports whether a link should be skipped.
func isInvalidLink(text, href string) bool {
	return text == "" || href == "" ||
		strings.HasPrefix(href, "javascript:") ||
		strings.HasPrefix(href, "#")
}

// insertBlockBreaks replaces structural closing tags and <br> with newlines
// so the text has reasonable line breaks.
func insertBlockBreaks(html string) string {
	const newline = "\n"

	result := reBR.ReplaceAllString(html, newline)

	result = reBlockSuffix.ReplaceAllString(result, newline)

	result = reLI.ReplaceAllString(result, "- ")

	return result
}

// stripTags removes all HTML tags from str.
func stripTags(str string) string {
	return reTag.ReplaceAllString(str, "")
}

// collapseWhitespace trims leading/trailing whitespace, collapses horizontal
// runs to a single space, and limits consecutive newlines to two.
func collapseWhitespace(str string) string {
	const space = " "

	str = reSpaceCollapse.ReplaceAllString(str, space)

	str = reNewlineCollapse.ReplaceAllString(str, "\n\n")

	return strings.TrimSpace(str)
}

// decodeEntities replaces common HTML entities with their character
// equivalents. It handles named entities, decimal numeric references, and
// hex references.
func decodeEntities(str string) string {
	str = strings.ReplaceAll(str, "&nbsp;", " ")

	str = strings.ReplaceAll(str, "&amp;", "&")

	str = strings.ReplaceAll(str, "&lt;", "<")

	str = strings.ReplaceAll(str, "&gt;", ">")

	str = strings.ReplaceAll(str, "&quot;", `"`)
	str = strings.ReplaceAll(str, "&#39;", "'")

	str = reDecEntity.ReplaceAllStringFunc(str, decodeDecimalEntity)

	str = reHexEntity.ReplaceAllStringFunc(str, decodeHexEntity)

	return str
}

var (
	reDecEntity = regexp.MustCompile(`&#(\d+);`)
	reHexEntity = regexp.MustCompile(`(?i)&#x([0-9a-f]+);`)
)

// decodeDecimalEntity converts a decimal numeric character reference.
func decodeDecimalEntity(match string) string {
	sub := reDecEntity.FindStringSubmatch(match)
	if len(sub) < subMatchTitle { // subMatchTitle == 2, same threshold
		return match
	}

	codePoint, err := strconv.ParseInt(sub[1], parseBase10, parseBits32)
	if err != nil {
		return match
	}

	return string(rune(codePoint))
}

// decodeHexEntity converts a hexadecimal numeric character reference.
func decodeHexEntity(match string) string {
	sub := reHexEntity.FindStringSubmatch(match)
	if len(sub) < subMatchTitle { // subMatchTitle == 2, same threshold
		return match
	}

	codePoint, err := strconv.ParseInt(sub[1], hexBase, parseBits32)
	if err != nil {
		return match
	}

	return string(rune(codePoint))
}
