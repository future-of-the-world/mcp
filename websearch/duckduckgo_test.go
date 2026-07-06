// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package websearch

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDuckDuckGoProvider_Name(t *testing.T) {
	t.Parallel()

	provider := &DuckDuckGoProvider{logger: (*slog.Logger)(nil)}
	assert.Equal(t, ddgProviderName, provider.Name())
}

func TestDuckDuckGoProvider_RequiresAPIKey(t *testing.T) {
	t.Parallel()

	provider := &DuckDuckGoProvider{logger: (*slog.Logger)(nil)}
	assert.False(t, provider.RequiresAPIKey())
}

func TestDuckDuckGoProvider_Supports(t *testing.T) {
	t.Parallel()

	provider := &DuckDuckGoProvider{logger: (*slog.Logger)(nil)}

	assert.True(t, provider.Supports(SearchKindWeb))
	assert.False(t, provider.Supports(SearchKindNews))
	assert.False(t, provider.Supports(SearchKindImages))
}

func TestDuckDuckGoProvider_Search_UnsupportedKind(t *testing.T) {
	t.Parallel()

	provider := &DuckDuckGoProvider{logger: (*slog.Logger)(nil)}
	_, err := provider.Search(t.Context(), "test", &SearchOptions{
		Kind: SearchKindNews,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported kind")
}

func TestParseDDGResults(t *testing.T) {
	t.Parallel()

	ClearIDStore()
	t.Cleanup(ClearIDStore)

	html := `
	<div>
		<a class="result__a" href="https://example.com/page1">
			<b>Example</b> Page 1
		</a>
		<a class="result__snippet">This is the first result snippet</a>
	</div>
	<div>
		<a class="result__a" href="https://example.com/page2">Page 2</a>
		<a class="result__snippet">Second result &amp; more</a>
	</div>
	<div>
		<a class="result__a"
			href="/l/?uddg=https%3A%2F%2Freal.example.com%2Fpage&amp;rut=abc">
			Redirect Page
		</a>
		<a class="result__snippet">A redirected result</a>
	</div>
	`

	results := parseDDGResults(html, 10)
	require.Len(t, results, 3)

	assert.Equal(t, "Example Page 1", results[0].Title)
	assert.Equal(t, "https://example.com/page1", results[0].URL)
	assert.Equal(t, "This is the first result snippet", results[0].Description)

	assert.Equal(t, "Page 2", results[1].Title)
	assert.Equal(t, "Second result & more", results[1].Description)

	assert.Equal(t, "Redirect Page", results[2].Title)
	assert.Equal(t, "https://real.example.com/page", results[2].URL)
}

func TestParseDDGResults_Limit(t *testing.T) {
	t.Parallel()

	var htmlBuilder strings.Builder

	for idx := range 5 {
		char := string(rune('a' + idx))
		fmt.Fprintf(
			&htmlBuilder,
			`<a class="result__a" href="https://example.com/%s">Title</a>`+
				`<a class="result__snippet">Snippet</a>`,
			char,
		)
	}

	results := parseDDGResults(htmlBuilder.String(), 3)
	assert.Len(t, results, 3)
}

func TestParseDDGResults_EmptyHTML(t *testing.T) {
	t.Parallel()

	results := parseDDGResults("", 10)
	assert.Empty(t, results)
}

func TestUnwrapDDG(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		href string
		want string
	}{
		{
			"uddg redirect",
			"/l/?uddg=https%3A%2F%2Fexample.com&rut=abc",
			"https://example.com",
		},
		{
			"protocol relative",
			"//cdn.example.com/img.jpg",
			"https://cdn.example.com/img.jpg",
		},
		{
			"direct URL",
			"https://example.com/page",
			"https://example.com/page",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, testCase.want, unwrapDDG(testCase.href))
		})
	}
}

func TestBuildDDGURL(t *testing.T) {
	t.Parallel()

	t.Run("basic query", func(t *testing.T) {
		t.Parallel()

		gotURL := buildDDGURL("golang", &SearchOptions{})
		assert.Contains(t, gotURL, "q=golang")
		assert.Contains(t, gotURL, "html.duckduckgo.com")
	})

	t.Run("with country", func(t *testing.T) {
		t.Parallel()

		gotURL := buildDDGURL("test", &SearchOptions{Country: "us"})
		assert.Contains(t, gotURL, "kl=us")
	})

	t.Run("nil opts", func(t *testing.T) {
		t.Parallel()

		gotURL := buildDDGURL("test", (*SearchOptions)(nil))
		assert.Contains(t, gotURL, "q=test")
	})
}
