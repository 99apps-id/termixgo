package app

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/secrets"
)

// Endpoint is one OpenAI-compatible server the operator registered.
type Endpoint struct {
	ID     string
	Name   string
	URL    string
	HasKey bool
	Models []string
}

// Endpoints lists every OpenAI-compatible endpoint, the unnamed slot first and
// the named ones in name order.
func (a *App) Endpoints() []Endpoint {
	a.mu.Lock()
	baseURLs := make(map[string]string, len(a.cfg.BaseURLs))
	for id, url := range a.cfg.BaseURLs {
		baseURLs[id] = url
	}
	models := map[string][]string{}
	for id, list := range a.cfg.CustomModels {
		models[id] = append([]string(nil), list...)
	}
	store := a.store
	a.mu.Unlock()

	ids := map[string]bool{}
	for id, url := range baseURLs {
		if provider.IsCompatible(id) && strings.TrimSpace(url) != "" {
			ids[id] = true
		}
	}
	for id, list := range models {
		if provider.IsCompatible(id) && len(list) > 0 {
			ids[id] = true
		}
	}
	sorted := make([]string, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if (sorted[i] == provider.CompatibleID) != (sorted[j] == provider.CompatibleID) {
			return sorted[i] == provider.CompatibleID
		}
		return sorted[i] < sorted[j]
	})
	out := make([]Endpoint, 0, len(sorted))
	for _, id := range sorted {
		out = append(out, Endpoint{
			ID:     id,
			Name:   provider.CompatibleName(id),
			URL:    baseURLs[id],
			HasKey: provider.KeySource(store, id) != "",
			Models: models[id],
		})
	}
	return out
}

// endpointID resolves a typed endpoint name to its provider id. "default" and
// the bare provider id name the unnamed slot.
func endpointID(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("an endpoint name is required")
	}
	if strings.EqualFold(trimmed, "default") || trimmed == provider.CompatibleID {
		return provider.CompatibleID, nil
	}
	trimmed = strings.TrimPrefix(trimmed, provider.CompatibleID+"/")
	slug := provider.SlugifyEndpointName(trimmed)
	if slug == "" {
		return "", fmt.Errorf("endpoint name must contain at least one letter or number")
	}
	return provider.CompatibleProviderID(slug), nil
}

// AddEndpoint registers or updates a named OpenAI-compatible endpoint, and
// stores its key when one is given. It returns the provider id.
func (a *App) AddEndpoint(name, rawURL, key string) (string, error) {
	id, err := endpointID(name)
	if err != nil {
		return "", err
	}
	url, err := provider.NormalizeBaseURL(rawURL)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(key) != "" {
		if a.store == nil {
			return "", fmt.Errorf("the secret store is not available")
		}
		if err := a.store.Set(secrets.ProviderKey(id), strings.TrimSpace(key)); err != nil {
			return "", fmt.Errorf("save key: %w", err)
		}
	}
	err = a.UpdateConfig(func(c *config.Config) {
		if c.BaseURLs == nil {
			c.BaseURLs = map[string]string{}
		}
		c.BaseURLs[id] = url
	})
	return id, err
}

// RemoveEndpoint drops an endpoint, its models and its key.
func (a *App) RemoveEndpoint(name string) (string, error) {
	id, err := endpointID(name)
	if err != nil {
		return "", err
	}
	found := false
	a.mu.Lock()
	_, found = a.cfg.BaseURLs[id]
	if !found {
		_, found = a.cfg.CustomModels[id]
	}
	inUse := a.model.Provider == id
	a.mu.Unlock()
	if !found {
		return "", fmt.Errorf("endpoint %q is not set", id)
	}
	if inUse {
		return "", fmt.Errorf("%s is the active model's provider; switch models with /model first", id)
	}
	if a.store != nil {
		_ = a.store.Delete(secrets.ProviderKey(id))
	}
	err = a.UpdateConfig(func(c *config.Config) {
		delete(c.BaseURLs, id)
		delete(c.CustomModels, id)
		if strings.HasPrefix(c.DefaultModel, id+":") {
			c.DefaultModel = ""
		}
	})
	return id, err
}

// RefreshEndpointModels asks the endpoint for its models and stores them, so
// they appear in /model and resolve to this endpoint when picked.
func (a *App) RefreshEndpointModels(ctx context.Context, name string) (string, []string, error) {
	id, err := endpointID(name)
	if err != nil {
		return "", nil, err
	}
	a.mu.Lock()
	url := strings.TrimSpace(a.cfg.BaseURLs[id])
	store := a.store
	a.mu.Unlock()
	if url == "" {
		return id, nil, fmt.Errorf("endpoint %q has no URL; add it first", id)
	}
	models, err := provider.ListCompatibleModels(ctx, url, provider.ResolveKey(store, id))
	if err != nil {
		return id, nil, err
	}
	if len(models) == 0 {
		return id, nil, fmt.Errorf("%s lists no models", url)
	}
	err = a.UpdateConfig(func(c *config.Config) {
		if c.CustomModels == nil {
			c.CustomModels = map[string][]string{}
		}
		c.CustomModels[id] = models
	})
	return id, models, err
}
