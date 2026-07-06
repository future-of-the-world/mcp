// Copyright (c) 2026 amidman. All rights reserved.
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package websearch

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
)

// ProviderFactory holds registered search providers and resolves them by name.
type ProviderFactory struct {
	mu          sync.RWMutex
	logger      *slog.Logger
	providers   map[string]SearchProvider
	defaultName string
}

// NewProviderFactory creates an empty ProviderFactory.
// If logger is nil, logging is disabled.
func NewProviderFactory(logger *slog.Logger) *ProviderFactory {
	return &ProviderFactory{
		mu:          sync.RWMutex{},
		logger:      logger,
		providers:   make(map[string]SearchProvider),
		defaultName: "",
	}
}

// Add registers a provider. If isDefault is true (or no default exists yet),
// the provider becomes the default.
//
//nolint:revive // flag-parameter is acceptable for registration API
func (factory *ProviderFactory) Add(ctx context.Context, provider SearchProvider, isDefault bool) {
	name := strings.ToLower(provider.Name())

	factory.mu.Lock()
	defer factory.mu.Unlock()

	factory.providers[name] = provider

	if isDefault || factory.defaultName == "" {
		if factory.logger != nil {
			factory.logger.InfoContext(ctx,
				"search provider registered as default",
				"provider", provider.Name(),
			)
		}

		factory.defaultName = name
	} else if factory.logger != nil {
		factory.logger.InfoContext(ctx,
			"search provider registered",
			"provider", provider.Name(),
		)
	}
}

// Get returns the provider with the given case-insensitive name.
func (factory *ProviderFactory) Get(name string) (SearchProvider, bool) {
	key := strings.ToLower(name)

	factory.mu.RLock()
	defer factory.mu.RUnlock()

	provider, ok := factory.providers[key]

	return provider, ok
}

// GetDefault returns the default provider, or nil if none is registered.
func (factory *ProviderFactory) GetDefault() SearchProvider {
	factory.mu.RLock()
	defer factory.mu.RUnlock()

	provider, ok := factory.providers[factory.defaultName]
	if !ok {
		return nil
	}

	return provider
}

// SetDefault sets the default provider by name. Returns false if the provider
// is not registered.
func (factory *ProviderFactory) SetDefault(ctx context.Context, name string) bool {
	factory.mu.Lock()
	defer factory.mu.Unlock()

	key := strings.ToLower(name)
	if _, ok := factory.providers[key]; !ok {
		return false
	}

	factory.defaultName = key

	if factory.logger != nil {
		factory.logger.InfoContext(ctx,
			"search provider set as default",
			"provider", name,
		)
	}

	return true
}

// Names returns all registered provider names.
func (factory *ProviderFactory) Names() []string {
	factory.mu.RLock()
	defer factory.mu.RUnlock()

	names := make([]string, 0, len(factory.providers))
	for name := range factory.providers {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

// SetupDefaults registers the standard set of providers. If apiKey is non-empty,
// Brave is registered as the default; DuckDuckGo is always registered as fallback.
func (factory *ProviderFactory) SetupDefaults(ctx context.Context, apiKey string) {
	if apiKey != "" {
		brave := &BraveProvider{apiKey: apiKey, logger: factory.logger}
		factory.Add(ctx, brave, true)
	}

	ddg := &DuckDuckGoProvider{logger: factory.logger}
	factory.Add(ctx, ddg, apiKey == "")
}
