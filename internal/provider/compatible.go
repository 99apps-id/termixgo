package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// SlugifyEndpointName turns an operator-typed name into the instance name of
// an openai-compatible/<name> provider: lower case, letters, digits, dot,
// dash and underscore, with runs of whitespace folded to one dash.
func SlugifyEndpointName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	prevDash := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case r == '.' || r == '_' || r == '-':
			b.WriteRune(r)
			prevDash = true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if !prevDash {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// ListCompatibleModels asks an OpenAI-compatible server for its models with
// GET {base}/models. It reads the OpenAI shape ({"data":[{"id"}]}) and the
// Ollama shape ({"models":[{"name"}]}), and returns sorted unique ids.
func ListCompatibleModels(ctx context.Context, baseURL, key string) ([]string, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("the endpoint has no base URL")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(key) != "" {
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(key))
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %v", base, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		text := strings.TrimSpace(string(data))
		if len(text) > 200 {
			text = text[:200] + "..."
		}
		return nil, fmt.Errorf("%s/models answered %s: %s", base, response.Status, text)
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
			ID    string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("%s/models did not answer with a model list", base)
	}
	seen := map[string]bool{}
	var ids []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, item := range payload.Data {
		add(item.ID)
	}
	for _, item := range payload.Models {
		switch {
		case item.ID != "":
			add(item.ID)
		case item.Model != "":
			add(item.Model)
		default:
			add(item.Name)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// CompatibleID is the default (unnamed) OpenAI-compatible provider id.
const CompatibleID = "openai-compatible"

// IsCompatible reports whether id is the openai-compatible provider or one of
// its named instances (openai-compatible/<name>). A bare "openai-compatible/"
// with no name is not a real instance, so it is rejected.
func IsCompatible(id string) bool {
	if id == CompatibleID {
		return true
	}
	name, ok := strings.CutPrefix(id, CompatibleID+"/")
	return ok && name != ""
}

// CompatibleName returns the instance name, or "" for the default slot.
func CompatibleName(id string) string {
	if !strings.HasPrefix(id, CompatibleID+"/") {
		return ""
	}
	return strings.TrimPrefix(id, CompatibleID+"/")
}

// CompatibleProviderID builds the provider id for a named instance; "" is the
// default slot.
func CompatibleProviderID(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return CompatibleID
	}
	return CompatibleID + "/" + name
}

// CompatibleLabel is the display label for a named instance.
func CompatibleLabel(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "OpenAI Compatible"
	}
	return "OpenAI Compatible (" + name + ")"
}
