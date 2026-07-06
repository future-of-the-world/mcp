// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package websearch

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewProviderFactory(t *testing.T) {
	t.Parallel()

	factory := NewProviderFactory((*slog.Logger)(nil))
	assert.NotNil(t, factory)
	assert.Nil(t, factory.GetDefault())
}

func TestProviderFactory_Add(t *testing.T) {
	t.Parallel()

	factory := NewProviderFactory((*slog.Logger)(nil))
	provider := &BraveProvider{apiKey: "test", logger: (*slog.Logger)(nil)}

	factory.Add(t.Context(), provider, true)

	got, ok := factory.Get("brave search")
	require.Truef(t, ok, "should find Brave provider")
	assert.Equal(t, braveProvider, got.Name())
}

func TestProviderFactory_Add_CaseInsensitive(t *testing.T) {
	t.Parallel()

	factory := NewProviderFactory((*slog.Logger)(nil))
	provider := &DuckDuckGoProvider{logger: (*slog.Logger)(nil)}

	factory.Add(t.Context(), provider, true)

	_, ok := factory.Get("duckduckgo")
	assert.Truef(t, ok, "should find lowercase")

	_, ok = factory.Get("DuckDuckGo")
	assert.Truef(t, ok, "should find mixed case")

	_, ok = factory.Get("DUCKDUCKGO")
	assert.Truef(t, ok, "should find uppercase")
}

func TestProviderFactory_Get_NotFound(t *testing.T) {
	t.Parallel()

	factory := NewProviderFactory((*slog.Logger)(nil))
	_, ok := factory.Get("nonexistent")
	assert.False(t, ok)
}

func TestProviderFactory_Add_DefaultBehavior(t *testing.T) {
	t.Parallel()

	t.Run("first provider becomes default", func(t *testing.T) {
		t.Parallel()

		factory := NewProviderFactory((*slog.Logger)(nil))
		factory.Add(t.Context(), &DuckDuckGoProvider{logger: (*slog.Logger)(nil)}, false)

		def := factory.GetDefault()
		require.NotNil(t, def)
		assert.Equal(t, ddgProviderName, def.Name())
	})

	t.Run("isDefault overrides", func(t *testing.T) {
		t.Parallel()

		factory := NewProviderFactory((*slog.Logger)(nil))
		factory.Add(t.Context(), &DuckDuckGoProvider{logger: (*slog.Logger)(nil)}, true)
		factory.Add(t.Context(), &BraveProvider{apiKey: "key", logger: (*slog.Logger)(nil)}, true)

		def := factory.GetDefault()
		require.NotNil(t, def)
		assert.Equal(t, braveProvider, def.Name())
	})

	t.Run("non-default does not override", func(t *testing.T) {
		t.Parallel()

		factory := NewProviderFactory((*slog.Logger)(nil))
		factory.Add(t.Context(), &BraveProvider{apiKey: "key", logger: (*slog.Logger)(nil)}, true)
		factory.Add(t.Context(), &DuckDuckGoProvider{logger: (*slog.Logger)(nil)}, false)

		def := factory.GetDefault()
		require.NotNil(t, def)
		assert.Equal(t, braveProvider, def.Name())
	})
}

func TestProviderFactory_GetDefault_Empty(t *testing.T) {
	t.Parallel()

	factory := NewProviderFactory((*slog.Logger)(nil))
	assert.Nil(t, factory.GetDefault())
}

func TestProviderFactory_SetupDefaults_WithAPIKey(t *testing.T) {
	t.Parallel()

	factory := NewProviderFactory((*slog.Logger)(nil))
	factory.SetupDefaults(t.Context(), "test-api-key")

	_, ok := factory.Get("brave search")
	assert.Truef(t, ok, "Brave should be registered")

	_, ok = factory.Get("duckduckgo")
	assert.Truef(t, ok, "DuckDuckGo should be registered")

	def := factory.GetDefault()
	require.NotNil(t, def)
	assert.Equalf(t, braveProvider, def.Name(),
		"Brave should be default when API key is set",
	)
}

func TestProviderFactory_SetupDefaults_NoAPIKey(t *testing.T) {
	t.Parallel()

	factory := NewProviderFactory((*slog.Logger)(nil))
	factory.SetupDefaults(t.Context(), "")

	_, ok := factory.Get("brave search")
	assert.Falsef(t, ok, "Brave should not be registered without API key")

	_, ok = factory.Get("duckduckgo")
	assert.Truef(t, ok, "DuckDuckGo should be registered")

	def := factory.GetDefault()
	require.NotNil(t, def)
	assert.Equalf(t, ddgProviderName, def.Name(),
		"DuckDuckGo should be default when no API key",
	)
}

func TestProviderFactory_SetupDefaults_BothProviders(t *testing.T) {
	t.Parallel()

	factory := NewProviderFactory((*slog.Logger)(nil))
	factory.SetupDefaults(t.Context(), "my-key")

	braveProvider, ok := factory.Get("brave search")
	require.Truef(t, ok, "should find Brave")
	assert.True(t, braveProvider.RequiresAPIKey())
	assert.True(t, braveProvider.Supports(SearchKindWeb))
	assert.True(t, braveProvider.Supports(SearchKindNews))

	ddgProvider, ok := factory.Get("duckduckgo")
	require.Truef(t, ok, "should find DuckDuckGo")
	assert.False(t, ddgProvider.RequiresAPIKey())
	assert.True(t, ddgProvider.Supports(SearchKindWeb))
	assert.False(t, ddgProvider.Supports(SearchKindNews))
}
