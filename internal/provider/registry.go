package provider

import (
	"sort"
	"strings"
)

// Kind is the wire protocol a provider speaks.
type Kind string

const (
	KindOpenAI    Kind = "openai"
	KindAnthropic Kind = "anthropic"
	KindGoogle    Kind = "google"
)

// Provider describes one BYOK provider.
type Provider struct {
	ID             string
	Label          string
	Kind           Kind
	DefaultBaseURL string
	ConsoleURL     string
	NeedsKey       bool
	KeyPrefix      string
	EnvKeys        []string
}

// Model is one entry in the local catalogue. APIID is what goes on the wire
// when a vendor renames a model but the stable local id must not change.
type Model struct {
	ID          string
	Provider    string
	Label       string
	APIID       string
	Description string
	Tags        []string
}

// DefaultContextWindow is the window assumed for a model the catalogue does
// not list. It is deliberately conservative: over-estimating overflows the
// provider and loses the whole turn, while under-estimating only trims a
// little early.
const DefaultContextWindow = 128000

// contextWindows holds the input window per model id, where it differs from
// the provider default.
var contextWindows = map[string]int{
	"gpt-5.4-mini":             400000,
	"gpt-5.4":                  400000,
	"gpt-5.6":                  400000,
	"gpt-4.1":                  1047576,
	"o4-mini":                  200000,
	"claude-sonnet-4-5":        200000,
	"claude-opus-4-1":          200000,
	"claude-haiku-4-5":         200000,
	"claude-3-7-sonnet-latest": 200000,
	"gemini-3-pro":             1048576,
	"gemini-2.5-pro":           1048576,
	"gemini-2.5-flash":         1048576,
	"deepseek-chat":            128000,
	"deepseek-reasoner":        65536,
	"grok-4":                   256000,
	"grok-3-mini":              131072,
	"llama-3.3-70b-versatile":  131072,
	"openai/gpt-oss-120b":      131072,
	"qwen/qwen3-32b":           131072,
	"codestral-latest":         262144,
}

// providerContextWindows is the fallback per provider.
var providerContextWindows = map[string]int{
	"openai":     400000,
	"anthropic":  200000,
	"google":     1048576,
	"deepseek":   128000,
	"groq":       131072,
	"xai":        131072,
	"cerebras":   131072,
	"mistral":    131072,
	"openrouter": 131072,
	"qwen":       131072,
	"zhipu":      131072,
	// A local server usually runs a quantised model with a small window.
	"ollama":            32768,
	"lmstudio":          32768,
	"mlx":               32768,
	"openai-compatible": DefaultContextWindow,
}

// Window returns the model's input context window in tokens.
func (m Model) Window() int {
	if limit := contextWindows[m.ID]; limit > 0 {
		return limit
	}
	if limit := providerContextWindows[m.Provider]; limit > 0 {
		return limit
	}
	return DefaultContextWindow
}

// WireID is the model id sent to the provider.
func (m Model) WireID() string {
	if strings.TrimSpace(m.APIID) != "" {
		return m.APIID
	}
	return m.ID
}

// Providers lists every provider in display order.
func Providers() []Provider {
	return []Provider{
		{ID: "openai", Label: "OpenAI", Kind: KindOpenAI, DefaultBaseURL: "https://api.openai.com/v1", ConsoleURL: "https://platform.openai.com/api-keys", NeedsKey: true, KeyPrefix: "sk-", EnvKeys: []string{"OPENAI_API_KEY"}},
		{ID: "anthropic", Label: "Anthropic", Kind: KindAnthropic, DefaultBaseURL: "https://api.anthropic.com", ConsoleURL: "https://console.anthropic.com/settings/keys", NeedsKey: true, KeyPrefix: "sk-ant-", EnvKeys: []string{"ANTHROPIC_API_KEY"}},
		{ID: "google", Label: "Google", Kind: KindGoogle, DefaultBaseURL: "https://generativelanguage.googleapis.com", ConsoleURL: "https://aistudio.google.com/apikey", NeedsKey: true, EnvKeys: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}},
		{ID: "xai", Label: "xAI", Kind: KindOpenAI, DefaultBaseURL: "https://api.x.ai/v1", ConsoleURL: "https://console.x.ai/", NeedsKey: true, KeyPrefix: "xai-", EnvKeys: []string{"XAI_API_KEY"}},
		{ID: "cerebras", Label: "Cerebras", Kind: KindOpenAI, DefaultBaseURL: "https://api.cerebras.ai/v1", ConsoleURL: "https://cloud.cerebras.ai/", NeedsKey: true, KeyPrefix: "csk-", EnvKeys: []string{"CEREBRAS_API_KEY"}},
		{ID: "groq", Label: "Groq", Kind: KindOpenAI, DefaultBaseURL: "https://api.groq.com/openai/v1", ConsoleURL: "https://console.groq.com/keys", NeedsKey: true, KeyPrefix: "gsk_", EnvKeys: []string{"GROQ_API_KEY"}},
		{ID: "deepseek", Label: "DeepSeek", Kind: KindOpenAI, DefaultBaseURL: "https://api.deepseek.com/v1", ConsoleURL: "https://platform.deepseek.com/api_keys", NeedsKey: true, KeyPrefix: "sk-", EnvKeys: []string{"DEEPSEEK_API_KEY"}},
		{ID: "qwen", Label: "Qwen", Kind: KindOpenAI, DefaultBaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", ConsoleURL: "https://bailian.console.aliyun.com/", NeedsKey: true, EnvKeys: []string{"DASHSCOPE_API_KEY", "QWEN_API_KEY"}},
		{ID: "zhipu", Label: "Zhipu AI (GLM)", Kind: KindOpenAI, DefaultBaseURL: "https://open.bigmodel.cn/api/paas/v4", ConsoleURL: "https://z.ai/model-api", NeedsKey: true, EnvKeys: []string{"ZHIPU_API_KEY"}},
		{ID: "mistral", Label: "Mistral", Kind: KindOpenAI, DefaultBaseURL: "https://api.mistral.ai/v1", ConsoleURL: "https://console.mistral.ai/api-keys/", NeedsKey: true, EnvKeys: []string{"MISTRAL_API_KEY"}},
		{ID: "openrouter", Label: "OpenRouter", Kind: KindOpenAI, DefaultBaseURL: "https://openrouter.ai/api/v1", ConsoleURL: "https://openrouter.ai/keys", NeedsKey: true, KeyPrefix: "sk-or-", EnvKeys: []string{"OPENROUTER_API_KEY"}},
		{ID: "openai-compatible", Label: "OpenAI Compatible", Kind: KindOpenAI, DefaultBaseURL: "", ConsoleURL: "https://platform.openai.com/docs/api-reference", EnvKeys: []string{"OPENAI_COMPATIBLE_API_KEY", "OPENAI_BASE_URL_KEY"}},
		{ID: "lmstudio", Label: "LM Studio", Kind: KindOpenAI, DefaultBaseURL: "http://localhost:1234/v1", ConsoleURL: "https://lmstudio.ai/docs/basics/server"},
		{ID: "mlx", Label: "MLX", Kind: KindOpenAI, DefaultBaseURL: "http://localhost:8080/v1", ConsoleURL: "https://github.com/ml-explore/mlx-lm/blob/main/mlx_lm/SERVER.md"},
		{ID: "ollama", Label: "Ollama", Kind: KindOpenAI, DefaultBaseURL: "http://localhost:11434/v1", ConsoleURL: "https://ollama.com/download"},
	}
}

// ByID finds a provider.
func ByID(id string) (Provider, bool) {
	for _, provider := range Providers() {
		if provider.ID == id {
			return provider, true
		}
	}
	return Provider{}, false
}

// IsLocal reports whether a provider serves a local model server.
func IsLocal(id string) bool {
	switch id {
	case "ollama", "lmstudio", "mlx":
		return true
	}
	return false
}

// Models lists the built-in catalogue.
func Models() []Model {
	return []Model{
		{ID: "gpt-5.4-mini", Provider: "openai", Label: "GPT-5.4 mini", Description: "Fast, inexpensive default for everyday coding.", Tags: []string{"tools", "fast"}},
		{ID: "gpt-5.4", Provider: "openai", Label: "GPT-5.4", Description: "Balanced flagship for agentic work.", Tags: []string{"reasoning", "tools", "vision"}},
		{ID: "gpt-5.6", Provider: "openai", Label: "GPT-5.6 Sol", Description: "Frontier model for complex professional work.", Tags: []string{"reasoning", "tools", "vision"}},
		{ID: "gpt-4.1", Provider: "openai", Label: "GPT-4.1", Description: "Long-context model with strong tool use.", Tags: []string{"tools", "vision"}},
		{ID: "o4-mini", Provider: "openai", Label: "o4-mini", Description: "Small reasoning model.", Tags: []string{"reasoning"}},

		{ID: "claude-sonnet-4-5", Provider: "anthropic", Label: "Claude Sonnet 4.5", Description: "Best balance of speed and coding ability.", Tags: []string{"reasoning", "tools", "coding"}},
		{ID: "claude-opus-4-1", Provider: "anthropic", Label: "Claude Opus 4.1", Description: "Deepest reasoning for hard problems.", Tags: []string{"reasoning", "tools"}},
		{ID: "claude-haiku-4-5", Provider: "anthropic", Label: "Claude Haiku 4.5", Description: "Fast and cheap.", Tags: []string{"fast", "tools"}},
		{ID: "claude-3-7-sonnet-latest", Provider: "anthropic", Label: "Claude 3.7 Sonnet", Description: "Previous generation workhorse.", Tags: []string{"tools"}},

		{ID: "gemini-3-pro", Provider: "google", Label: "Gemini 3 Pro", Description: "Multimodal flagship with a large window.", Tags: []string{"reasoning", "tools", "vision"}},
		{ID: "gemini-2.5-pro", Provider: "google", Label: "Gemini 2.5 Pro", Description: "Strong reasoning and long context.", Tags: []string{"reasoning", "tools"}},
		{ID: "gemini-2.5-flash", Provider: "google", Label: "Gemini 2.5 Flash", Description: "Fast and inexpensive.", Tags: []string{"fast", "tools"}},

		{ID: "deepseek-chat", Provider: "deepseek", Label: "DeepSeek Chat", Description: "General chat and coding model.", Tags: []string{"tools", "coding"}},
		{ID: "deepseek-reasoner", Provider: "deepseek", Label: "DeepSeek Reasoner", Description: "Step-by-step reasoning model.", Tags: []string{"reasoning"}},

		{ID: "llama-3.3-70b-versatile", Provider: "groq", Label: "Llama 3.3 70B", Description: "Very fast open model.", Tags: []string{"fast", "tools"}},
		{ID: "openai/gpt-oss-120b", Provider: "groq", Label: "GPT-OSS 120B", Description: "Open-weight large model on Groq.", Tags: []string{"reasoning", "tools"}},
		{ID: "qwen/qwen3-32b", Provider: "groq", Label: "Qwen3 32B", Description: "Open model with reasoning.", Tags: []string{"reasoning"}},

		{ID: "grok-4", Provider: "xai", Label: "Grok 4", Description: "xAI flagship.", Tags: []string{"reasoning", "tools"}},
		{ID: "grok-3-mini", Provider: "xai", Label: "Grok 3 mini", Description: "Fast xAI model.", Tags: []string{"fast"}},

		{ID: "llama-3.3-70b", Provider: "cerebras", Label: "Llama 3.3 70B", Description: "Open model at Cerebras speed.", Tags: []string{"fast"}},
		{ID: "qwen-3-32b", Provider: "cerebras", Label: "Qwen3 32B", Description: "Open reasoning model.", Tags: []string{"reasoning"}},

		{ID: "mistral-large-latest", Provider: "mistral", Label: "Mistral Large", Description: "Mistral flagship.", Tags: []string{"tools"}},
		{ID: "codestral-latest", Provider: "mistral", Label: "Codestral", Description: "Code-focused model.", Tags: []string{"coding"}},

		{ID: "openrouter/auto", Provider: "openrouter", Label: "OpenRouter Auto", Description: "Routes to the best available model.", Tags: []string{"tools"}},
		{ID: "anthropic/claude-sonnet-4.5", Provider: "openrouter", Label: "Claude Sonnet 4.5 (OR)", Description: "Anthropic model via OpenRouter.", Tags: []string{"reasoning", "tools"}},

		{ID: "qwen2.5-coder:latest", Provider: "ollama", Label: "Qwen2.5 Coder", Description: "Local coding model.", Tags: []string{"coding"}},
		{ID: "llama3.2:latest", Provider: "ollama", Label: "Llama 3.2", Description: "Local general model.", Tags: []string{"local"}},
		{ID: "deepseek-coder-v2:latest", Provider: "ollama", Label: "DeepSeek Coder v2", Description: "Local code model.", Tags: []string{"coding"}},
	}
}

// ModelsFor returns one provider's models in catalogue order.
func ModelsFor(providerID string) []Model {
	models := make([]Model, 0, 8)
	for _, model := range Models() {
		if model.Provider == providerID {
			models = append(models, model)
		}
	}
	return models
}

// ModelByID finds a model by its stable id.
func ModelByID(id string) (Model, bool) {
	for _, model := range Models() {
		if model.ID == id {
			return model, true
		}
	}
	return Model{}, false
}

// ModelFromQuery resolves free text to a model: a catalogue id, a label, a
// substring, or "provider:id" for anything the catalogue does not list.
//
// Both the command line and the app go through this, so a model selected either
// way is stored as the same string. That matters because the stored id is what
// doctor, the status bar and the price overrides all key on.
func ModelFromQuery(query string) (Model, bool) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return Model{}, false
	}
	// A local server's model tags contain a colon of their own
	// (qwen2.5-coder:latest), so the prefixed form is only recognised when the
	// prefix names a real provider.
	if strings.Contains(trimmed, ":") && !IsLocal(trimmed) {
		parts := strings.SplitN(trimmed, ":", 2)
		if _, ok := ByID(parts[0]); ok {
			return Model{ID: parts[1], Provider: parts[0], Label: parts[1], APIID: parts[1]}, true
		}
	}
	return FindModel(trimmed)
}

// ProvidersRequiringKey lists key-based providers, sorted by label. The setup
// wizard walks this list.
func ProvidersRequiringKey() []Provider {
	out := make([]Provider, 0, len(Providers()))
	for _, provider := range Providers() {
		if provider.NeedsKey {
			out = append(out, provider)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// FindModel resolves free text the operator typed to a model: exact id,
// exact wire id, case-insensitive label, then a unique id/label substring.
func FindModel(query string) (Model, bool) {
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return Model{}, false
	}
	for _, model := range Models() {
		if model.ID == needle || strings.ToLower(model.WireID()) == needle {
			return model, true
		}
	}
	for _, model := range Models() {
		if strings.ToLower(model.Label) == needle {
			return model, true
		}
	}
	var matches []Model
	for _, model := range Models() {
		haystack := strings.ToLower(model.ID + " " + model.Label + " " + model.Provider)
		if strings.Contains(haystack, needle) {
			matches = append(matches, model)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return Model{}, false
}
