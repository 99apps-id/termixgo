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
	// OAuth marks a provider whose credential comes from a login (a stored
	// OAuth token) rather than a pasted API key.
	OAuth bool
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
	// OpenAI: the GPT-6 and GPT-5.6 families share a 1.05M window.
	"gpt-6-astra":   1050000,
	"gpt-6-sol":     1050000,
	"gpt-6-luna":    1050000,
	"gpt-5.6-terra": 1050000,
	"gpt-5.6-sol":   1050000,
	"gpt-5.6-luna":  1050000,
	"gpt-5.5":       1050000,
	"gpt-5.4":       1050000,
	"gpt-5.4-mini":  400000,
	"gpt-5.3-codex": 400000,

	// Anthropic: everything current is 1M.
	"claude-fable-5-1":  1000000,
	"claude-opus-5-5":   1000000,
	"claude-opus-5":     1000000,
	"claude-sonnet-5":   1000000,
	"claude-sonnet-4-6": 1000000,
	"claude-haiku-4-5":  200000,

	// Google: the Flash and Pro lines are all around 1M.
	"gemini-3.1-pro-preview": 1048576,
	"gemini-3.8-flash":       1048576,
	"gemini-3.7-flash":       1048576,
	"gemini-3.5-flash":       1048576,
	"gemini-3.5-flash-lite":  1048576,

	// OAuth catalogue entries keep a distinct local id; their window follows
	// the model they map to.
	"codex-gpt-6-astra":         1050000,
	"codex-gpt-6-sol":           1050000,
	"codex-gpt-6-luna":          1050000,
	"codex-gpt-5.6-terra":       1050000,
	"codex-gpt-5.6-sol":         1050000,
	"codex-gpt-5.6-luna":        1050000,
	"codex-gpt-5.5":             1050000,
	"codex-gpt-5.4":             1050000,
	"codex-gpt-5.4-codex":       400000,
	"codex-gpt-5.3-codex":       400000,
	"codex-gpt-5.3-codex-spark": 400000,
	"codex-gpt-5.2-codex":       400000,
	"codex-gpt-5.1-codex-max":   400000,
	"codex-gpt-5.1-codex":       400000,
	"codex-gpt-5-codex":         400000,
	"grok-4.7-oauth":            500000,
	"grok-4.5-oauth":            500000,
	"grok-build-0.1-oauth":      256000,

	// xAI.
	"grok-4.7":       500000,
	"grok-4.6":       500000,
	"grok-4.5":       500000,
	"grok-build-0.1": 256000,

	// DeepSeek: 1M across the V4 family.
	"deepseek-v4-pro":     1048576,
	"deepseek-v4.1-flash": 1048576,
	"deepseek-v4-flash":   1048576,

	// Chinese vendors.
	"step-3.7-flash": 262144,
	"step-3.5-flash": 262144,

	// The same StepFun models served through the plan endpoint.
	"stepfun-plan/step-3.7-flash": 262144,
	"stepfun-plan/step-3.5-flash": 262144,

	"kimi-k3":        1048576,
	"kimi-k2.7-code": 262144,
	"kimi-k2.6":      262144,

	"minimax-m3":   1048576,
	"minimax-m2.7": 204800,

	"glm-5.3":       1310720,
	"glm-5.3-flash": 1310720,
	"glm-4.7":       204800,

	"qwen3.8-max": 1000000,
	"qwen3.8-27b": 1000000,
	"qwen3.7-max": 1000000,

	// The models served through the Qwen Cloud token plan endpoint.
	"qwen-token-plan/qwen3.8-max":            1000000,
	"qwen-token-plan/qwen3.8-27b":            1000000,
	"qwen-token-plan/qwen3.7-max":            1000000,
	"qwen-token-plan/qwen3.8-flash":          1000000,
	"qwen-token-plan/qwen3.6-flash":          1000000,
	"qwen-token-plan/deepseek-v4.1-flash":    1048576,
	"qwen-token-plan/deepseek-v4-pro-0813":   1048576,
	"qwen-token-plan/deepseek-v4-pro":        1048576,
	"qwen-token-plan/deepseek-v4-flash-0731": 1048576,
	"qwen-token-plan/glm-5.3":                1310720,
	"qwen-token-plan/glm-5.2":                1310720,

	"ernie-4.5-300b-a47b":   131072,
	"doubao-seed-2-1-pro":   262144,
	"doubao-seed-2-1-turbo": 262144,

	"mistral-large-2512": 262144,
	"devstral-2512":      262144,
	"mistral-medium-3-5": 262144,
	"codestral-2508":     256000,

	// Hosted open weights.
	"openai/gpt-oss-120b":     131072,
	"openai/gpt-oss-20b":      131072,
	"llama-3.3-70b-versatile": 131072,
	"qwen/qwen3.8-27b":        131072,
	"gpt-oss-120b":            131072,
	"qwen-3.8-27b":            128000,
	"together/kimi-k3":        1048576,
	"deepinfra/kimi-k3":       262144,
	"nvidia/kimi-k3":          262144,
	"nebius/kimi-k3":          1048576,
	"sambanova/minimax-m2.7":  196608,
	"huggingface/glm-5.3":     1310720,
	"github/gpt-6-astra":      1050000,

	// Aggregators inherit the underlying model's window.
	"anthropic/claude-opus-5.5": 1000000,
	"openai/gpt-6-astra":        1050000,
	"moonshotai/kimi-k3":        1048576,
	"z-ai/glm-5.3":              1310720,
	"qwen/qwen3.8-max":          1000000,
	"deepseek/deepseek-v4-pro":  1048576,

	"sonar-pro":              200000,
	"sonar-deep-research":    128000,
	"command-a-plus-05-2026": 192000,
}

// providerContextWindows is the fallback per provider.
var providerContextWindows = map[string]int{
	"openai":          400000,
	"anthropic":       200000,
	"google":          1048576,
	"deepseek":        128000,
	"groq":            131072,
	"xai":             131072,
	"cerebras":        131072,
	"mistral":         131072,
	"openrouter":      131072,
	"qwen":            131072,
	"qwen-token-plan": 131072,
	"stepfun":         262144,
	"stepfun-plan":    262144,
	"zhipu":           131072,
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
//
// The order is deliberate: the vendors a coding agent is usually pointed at
// come first, then the aggregators, then the specialist and self-hosted
// endpoints. The setup wizard walks this list, so the order is also the order
// an operator reads.
//
// Endpoints are the OpenAI-compatible paths where a vendor offers one, because
// that is the protocol the default client speaks. A vendor whose URL carries an
// account or resource id (Cloudflare, Azure) has no default and must be
// configured through `baseUrls`; the field is left empty on purpose so the
// failure is a clear "no endpoint configured" rather than a request to a
// placeholder host.
func Providers() []Provider {
	return []Provider{
		// Frontier vendors, each with its own wire protocol.
		{ID: "openai", Label: "OpenAI", Kind: KindOpenAI, DefaultBaseURL: "https://api.openai.com/v1", ConsoleURL: "https://platform.openai.com/api-keys", NeedsKey: true, KeyPrefix: "sk-", EnvKeys: []string{"OPENAI_API_KEY"}},
		{ID: "anthropic", Label: "Anthropic", Kind: KindAnthropic, DefaultBaseURL: "https://api.anthropic.com", ConsoleURL: "https://console.anthropic.com/settings/keys", NeedsKey: true, KeyPrefix: "sk-ant-", EnvKeys: []string{"ANTHROPIC_API_KEY"}},
		{ID: "google", Label: "Google Gemini", Kind: KindGoogle, DefaultBaseURL: "https://generativelanguage.googleapis.com", ConsoleURL: "https://aistudio.google.com/apikey", NeedsKey: true, EnvKeys: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}},
		{ID: "xai", Label: "xAI Grok", Kind: KindOpenAI, DefaultBaseURL: "https://api.x.ai/v1", ConsoleURL: "https://console.x.ai/", NeedsKey: true, KeyPrefix: "xai-", EnvKeys: []string{"XAI_API_KEY"}},
		{ID: "xai-oauth", Label: "xAI Grok (OAuth)", Kind: KindOpenAI, DefaultBaseURL: "https://api.x.ai/v1", ConsoleURL: "https://x.ai/", NeedsKey: true, OAuth: true},
		{ID: "openai-codex", Label: "OpenAI Codex (ChatGPT)", Kind: KindOpenAI, DefaultBaseURL: "https://chatgpt.com/backend-api/codex", ConsoleURL: "https://chatgpt.com/", NeedsKey: true, OAuth: true},

		// Vendors that matter most for agentic coding, cheapest strong models
		// first so the wizard's default order is also a sensible one.
		{ID: "deepseek", Label: "DeepSeek", Kind: KindOpenAI, DefaultBaseURL: "https://api.deepseek.com/v1", ConsoleURL: "https://platform.deepseek.com/api_keys", NeedsKey: true, KeyPrefix: "sk-", EnvKeys: []string{"DEEPSEEK_API_KEY"}},
		{ID: "stepfun", Label: "StepFun", Kind: KindOpenAI, DefaultBaseURL: "https://api.stepfun.com/v1", ConsoleURL: "https://platform.stepfun.com/interface-key", NeedsKey: true, EnvKeys: []string{"STEPFUN_API_KEY", "STEPFUN_CN_API_KEY"}},
		{ID: "stepfun-plan", Label: "StepFun Plan", Kind: KindOpenAI, DefaultBaseURL: "https://api.stepfun.ai/step_plan/v1", ConsoleURL: "https://platform.stepfun.com/interface-key", NeedsKey: true, EnvKeys: []string{"STEPFUN_PLAN_API_KEY"}},
		{ID: "moonshot", Label: "Moonshot Kimi", Kind: KindOpenAI, DefaultBaseURL: "https://api.moonshot.cn/v1", ConsoleURL: "https://platform.moonshot.cn/console/api-keys", NeedsKey: true, KeyPrefix: "sk-", EnvKeys: []string{"MOONSHOT_API_KEY", "KIMI_API_KEY"}},
		{ID: "minimax", Label: "MiniMax", Kind: KindOpenAI, DefaultBaseURL: "https://api.minimax.io/v1", ConsoleURL: "https://platform.minimax.io/user-center/basic-information/interface-key", NeedsKey: true, EnvKeys: []string{"MINIMAX_API_KEY"}},
		{ID: "zhipu", Label: "Zhipu GLM", Kind: KindOpenAI, DefaultBaseURL: "https://open.bigmodel.cn/api/paas/v4", ConsoleURL: "https://z.ai/model-api", NeedsKey: true, EnvKeys: []string{"ZHIPU_API_KEY", "GLM_API_KEY"}},
		{ID: "qwen", Label: "Alibaba Qwen", Kind: KindOpenAI, DefaultBaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", ConsoleURL: "https://bailian.console.aliyun.com/", NeedsKey: true, EnvKeys: []string{"DASHSCOPE_API_KEY", "QWEN_API_KEY"}},
		{ID: "qwen-token-plan", Label: "Qwen Cloud Token Plan", Kind: KindOpenAI, DefaultBaseURL: "https://token-plan.maas.qwencloudapi.com/compatible-mode/v1", ConsoleURL: "https://bailian.console.aliyun.com/", NeedsKey: true, EnvKeys: []string{"QWEN_TOKEN_PLAN_API_KEY"}},
		{ID: "mistral", Label: "Mistral", Kind: KindOpenAI, DefaultBaseURL: "https://api.mistral.ai/v1", ConsoleURL: "https://console.mistral.ai/api-keys/", NeedsKey: true, EnvKeys: []string{"MISTRAL_API_KEY"}},
		{ID: "baidu", Label: "Baidu Qianfan", Kind: KindOpenAI, DefaultBaseURL: "https://qianfan.baidubce.com/v2", ConsoleURL: "https://console.bce.baidu.com/iam/#/iam/apikey/list", NeedsKey: true, EnvKeys: []string{"QIANFAN_API_KEY", "BAIDU_API_KEY"}},
		{ID: "volcengine", Label: "Volcengine Ark (Doubao)", Kind: KindOpenAI, DefaultBaseURL: "https://ark.cn-beijing.volces.com/api/v3", ConsoleURL: "https://console.volcengine.com/ark", NeedsKey: true, EnvKeys: []string{"ARK_API_KEY", "VOLCENGINE_API_KEY"}},
		{ID: "iflow", Label: "iFlow", Kind: KindOpenAI, DefaultBaseURL: "https://apis.iflow.cn/v1", ConsoleURL: "https://iflow.cn/", NeedsKey: true, EnvKeys: []string{"IFLOW_API_KEY"}},

		// Fast inference hosts: the same open weights, served quickly.
		{ID: "groq", Label: "Groq", Kind: KindOpenAI, DefaultBaseURL: "https://api.groq.com/openai/v1", ConsoleURL: "https://console.groq.com/keys", NeedsKey: true, KeyPrefix: "gsk_", EnvKeys: []string{"GROQ_API_KEY"}},
		{ID: "cerebras", Label: "Cerebras", Kind: KindOpenAI, DefaultBaseURL: "https://api.cerebras.ai/v1", ConsoleURL: "https://cloud.cerebras.ai/", NeedsKey: true, KeyPrefix: "csk-", EnvKeys: []string{"CEREBRAS_API_KEY"}},
		{ID: "sambanova", Label: "SambaNova", Kind: KindOpenAI, DefaultBaseURL: "https://api.sambanova.ai/v1", ConsoleURL: "https://cloud.sambanova.ai/apis", NeedsKey: true, EnvKeys: []string{"SAMBANOVA_API_KEY"}},
		{ID: "fireworks", Label: "Fireworks", Kind: KindOpenAI, DefaultBaseURL: "https://api.fireworks.ai/inference/v1", ConsoleURL: "https://fireworks.ai/account/api-keys", NeedsKey: true, EnvKeys: []string{"FIREWORKS_API_KEY"}},
		{ID: "together", Label: "Together AI", Kind: KindOpenAI, DefaultBaseURL: "https://api.together.xyz/v1", ConsoleURL: "https://api.together.ai/settings/api-keys", NeedsKey: true, EnvKeys: []string{"TOGETHER_API_KEY"}},
		{ID: "deepinfra", Label: "DeepInfra", Kind: KindOpenAI, DefaultBaseURL: "https://api.deepinfra.com/v1/openai", ConsoleURL: "https://deepinfra.com/dash/api_keys", NeedsKey: true, EnvKeys: []string{"DEEPINFRA_API_KEY"}},
		{ID: "novita", Label: "Novita AI", Kind: KindOpenAI, DefaultBaseURL: "https://api.novita.ai/v3/openai", ConsoleURL: "https://novita.ai/settings/key-management", NeedsKey: true, EnvKeys: []string{"NOVITA_API_KEY"}},
		{ID: "siliconflow", Label: "SiliconFlow", Kind: KindOpenAI, DefaultBaseURL: "https://api.siliconflow.com/v1", ConsoleURL: "https://cloud.siliconflow.com/account/ak", NeedsKey: true, EnvKeys: []string{"SILICONFLOW_API_KEY"}},
		{ID: "nebius", Label: "Nebius AI Studio", Kind: KindOpenAI, DefaultBaseURL: "https://api.studio.nebius.ai/v1", ConsoleURL: "https://studio.nebius.ai/", NeedsKey: true, EnvKeys: []string{"NEBIUS_API_KEY"}},
		{ID: "nvidia", Label: "NVIDIA NIM", Kind: KindOpenAI, DefaultBaseURL: "https://integrate.api.nvidia.com/v1", ConsoleURL: "https://build.nvidia.com/settings/api-keys", NeedsKey: true, KeyPrefix: "nvapi-", EnvKeys: []string{"NVIDIA_API_KEY", "NVIDIA_NIM_API_KEY"}},
		{ID: "hyperbolic", Label: "Hyperbolic", Kind: KindOpenAI, DefaultBaseURL: "https://api.hyperbolic.xyz/v1", ConsoleURL: "https://app.hyperbolic.xyz/settings", NeedsKey: true, EnvKeys: []string{"HYPERBOLIC_API_KEY"}},
		{ID: "lambda", Label: "Lambda Labs", Kind: KindOpenAI, DefaultBaseURL: "https://api.lambdalabs.com/v1", ConsoleURL: "https://cloud.lambdalabs.com/api-keys", NeedsKey: true, EnvKeys: []string{"LAMBDA_API_KEY"}},
		{ID: "lepton", Label: "Lepton AI", Kind: KindOpenAI, DefaultBaseURL: "https://api.lepton.ai/v1", ConsoleURL: "https://lepton.ai/", NeedsKey: true, EnvKeys: []string{"LEPTON_API_KEY"}},
		{ID: "chutes", Label: "Chutes", Kind: KindOpenAI, DefaultBaseURL: "https://llm.chutes.ai/v1", ConsoleURL: "https://chutes.ai/app/api", NeedsKey: true, EnvKeys: []string{"CHUTES_API_KEY"}},
		{ID: "huggingface", Label: "Hugging Face Router", Kind: KindOpenAI, DefaultBaseURL: "https://router.huggingface.co/v1", ConsoleURL: "https://huggingface.co/settings/tokens", NeedsKey: true, KeyPrefix: "hf_", EnvKeys: []string{"HF_TOKEN", "HUGGINGFACE_API_KEY"}},

		// Aggregators: one key, many vendors. Useful when a single budget has to
		// cover several model families.
		{ID: "openrouter", Label: "OpenRouter", Kind: KindOpenAI, DefaultBaseURL: "https://openrouter.ai/api/v1", ConsoleURL: "https://openrouter.ai/keys", NeedsKey: true, KeyPrefix: "sk-or-", EnvKeys: []string{"OPENROUTER_API_KEY"}},
		{ID: "vercel", Label: "Vercel AI Gateway", Kind: KindOpenAI, DefaultBaseURL: "https://ai-gateway.vercel.sh/v1", ConsoleURL: "https://vercel.com/dashboard/ai-gateway", NeedsKey: true, EnvKeys: []string{"AI_GATEWAY_API_KEY"}},
		{ID: "github", Label: "GitHub Models", Kind: KindOpenAI, DefaultBaseURL: "https://models.github.ai/inference", ConsoleURL: "https://github.com/settings/tokens", NeedsKey: true, KeyPrefix: "gh", EnvKeys: []string{"GITHUB_MODELS_TOKEN", "GITHUB_TOKEN"}},
		{ID: "kilocode", Label: "Kilo Code", Kind: KindOpenAI, DefaultBaseURL: "https://api.kilo.ai/api/openrouter", ConsoleURL: "https://kilocode.ai/", NeedsKey: true, EnvKeys: []string{"KILOCODE_API_KEY"}},
		{ID: "venice", Label: "Venice AI", Kind: KindOpenAI, DefaultBaseURL: "https://api.venice.ai/api/v1", ConsoleURL: "https://venice.ai/settings/api", NeedsKey: true, EnvKeys: []string{"VENICE_API_KEY"}},
		{ID: "blackbox", Label: "Blackbox AI", Kind: KindOpenAI, DefaultBaseURL: "https://api.blackbox.ai/v1", ConsoleURL: "https://app.blackbox.ai/", NeedsKey: true, EnvKeys: []string{"BLACKBOX_API_KEY"}},

		// Search- and tool-centric endpoints. Useful for research rather than
		// for the edit loop, so they sit below the coding hosts.
		{ID: "perplexity", Label: "Perplexity", Kind: KindOpenAI, DefaultBaseURL: "https://api.perplexity.ai", ConsoleURL: "https://www.perplexity.ai/settings/api", NeedsKey: true, KeyPrefix: "pplx-", EnvKeys: []string{"PERPLEXITY_API_KEY"}},
		{ID: "cohere", Label: "Cohere", Kind: KindOpenAI, DefaultBaseURL: "https://api.cohere.ai/compatibility/v1", ConsoleURL: "https://dashboard.cohere.com/api-keys", NeedsKey: true, EnvKeys: []string{"COHERE_API_KEY", "CO_API_KEY"}},
		{ID: "ai21", Label: "AI21 Labs", Kind: KindOpenAI, DefaultBaseURL: "https://api.ai21.com/studio/v1", ConsoleURL: "https://studio.ai21.com/account/api-key", NeedsKey: true, EnvKeys: []string{"AI21_API_KEY"}},

		// Endpoints whose URL contains an account or resource id, so there is no
		// usable default. Set them under `baseUrls` in the config.
		{ID: "cloudflare", Label: "Cloudflare Workers AI", Kind: KindOpenAI, DefaultBaseURL: "", ConsoleURL: "https://dash.cloudflare.com/?to=/:account/ai/workers-ai", NeedsKey: true, EnvKeys: []string{"CLOUDFLARE_API_TOKEN", "CLOUDFLARE_API_KEY"}},
		{ID: "azure", Label: "Azure OpenAI", Kind: KindOpenAI, DefaultBaseURL: "", ConsoleURL: "https://portal.azure.com/", NeedsKey: true, EnvKeys: []string{"AZURE_OPENAI_API_KEY"}},
		{ID: "openai-compatible", Label: "OpenAI Compatible", Kind: KindOpenAI, DefaultBaseURL: "", ConsoleURL: "https://platform.openai.com/docs/api-reference", EnvKeys: []string{"OPENAI_COMPATIBLE_API_KEY", "OPENAI_BASE_URL_KEY"}},

		// Local model servers. No key, no network: the model runs on the same
		// machine as the agent.
		{ID: "ollama", Label: "Ollama", Kind: KindOpenAI, DefaultBaseURL: "http://localhost:11434/v1", ConsoleURL: "https://ollama.com/download"},
		{ID: "lmstudio", Label: "LM Studio", Kind: KindOpenAI, DefaultBaseURL: "http://localhost:1234/v1", ConsoleURL: "https://lmstudio.ai/docs/basics/server"},
		{ID: "mlx", Label: "MLX", Kind: KindOpenAI, DefaultBaseURL: "http://localhost:8080/v1", ConsoleURL: "https://github.com/ml-explore/mlx-lm/blob/main/mlx_lm/SERVER.md"},
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
//
// This is a curated list, not the full catalogue of every provider: the picker
// is for choosing a default, and a list of several hundred entries is worse at
// that than a list of eight. Vendors retire and rename models constantly, so a
// model that is absent here is still usable by typing its id, and `modelPricing`
// gives an unpriced one a price. The entries follow the vendors' own
// documentation, and open-weight models are listed under the host that serves
// them rather than under whoever trained them.
func Models() []Model {
	return []Model{
		// OpenAI. Astra is the flagship, Sol the middle, Luna the fast tier.
		{ID: "gpt-6-astra", Provider: "openai", Label: "GPT-6 Astra", Description: "Flagship for the hardest end-to-end work.", Tags: []string{"reasoning", "tools", "vision", "coding"}},
		{ID: "gpt-6-sol", Provider: "openai", Label: "GPT-6 Sol", Description: "Balances intelligence and cost.", Tags: []string{"reasoning", "tools", "vision", "coding"}},
		{ID: "gpt-6-luna", Provider: "openai", Label: "GPT-6 Luna", Description: "Efficient tier for high-volume work.", Tags: []string{"fast", "tools", "coding"}},
		{ID: "gpt-5.6-terra", Provider: "openai", Label: "GPT-5.6 Terra", Description: "Previous balanced generation.", Tags: []string{"reasoning", "tools"}},
		{ID: "gpt-5.6-sol", Provider: "openai", Label: "GPT-5.6 Sol", Description: "Balanced previous generation.", Tags: []string{"reasoning", "tools"}},
		{ID: "gpt-5.6-luna", Provider: "openai", Label: "GPT-5.6 Luna", Description: "Efficient previous generation.", Tags: []string{"fast", "tools"}},
		{ID: "gpt-5.5", Provider: "openai", Label: "GPT-5.5", Description: "Previous frontier generation.", Tags: []string{"reasoning", "tools"}},
		{ID: "gpt-5.4", Provider: "openai", Label: "GPT-5.4", Description: "Long-context workhorse.", Tags: []string{"tools", "vision"}},
		{ID: "gpt-5.4-mini", Provider: "openai", Label: "GPT-5.4 mini", Description: "Fast and inexpensive.", Tags: []string{"fast", "tools"}},
		{ID: "gpt-5.3-codex", Provider: "openai", Label: "GPT-5.3 Codex", Description: "Tuned for agentic software engineering.", Tags: []string{"coding", "tools"}},
		// The models served through a ChatGPT login (Codex OAuth). The local id
		// is distinct because the catalogue keys by id alone; APIID is what goes
		// on the wire. The newest Codex generation first.
		{ID: "codex-gpt-6-astra", Provider: "openai-codex", Label: "GPT-6 Astra (ChatGPT)", APIID: "gpt-6-astra", Description: "Flagship through a ChatGPT login.", Tags: []string{"reasoning", "tools", "coding"}},
		{ID: "codex-gpt-6-sol", Provider: "openai-codex", Label: "GPT-6 Sol (ChatGPT)", APIID: "gpt-6-sol", Description: "Balanced through a ChatGPT login.", Tags: []string{"reasoning", "tools"}},
		{ID: "codex-gpt-6-luna", Provider: "openai-codex", Label: "GPT-6 Luna (ChatGPT)", APIID: "gpt-6-luna", Description: "Efficient through a ChatGPT login.", Tags: []string{"fast", "tools"}},
		{ID: "codex-gpt-5.6-terra", Provider: "openai-codex", Label: "GPT-5.6 Terra (ChatGPT)", APIID: "gpt-5.6-terra", Description: "Previous balanced generation through a ChatGPT login.", Tags: []string{"reasoning", "tools"}},
		{ID: "codex-gpt-5.6-sol", Provider: "openai-codex", Label: "GPT-5.6 Sol (ChatGPT)", APIID: "gpt-5.6-sol", Description: "Previous balanced generation through a ChatGPT login.", Tags: []string{"reasoning", "tools"}},
		{ID: "codex-gpt-5.6-luna", Provider: "openai-codex", Label: "GPT-5.6 Luna (ChatGPT)", APIID: "gpt-5.6-luna", Description: "Previous efficient generation through a ChatGPT login.", Tags: []string{"fast", "tools"}},
		{ID: "codex-gpt-5.5", Provider: "openai-codex", Label: "GPT-5.5 (ChatGPT)", APIID: "gpt-5.5", Description: "Latest frontier generation through a ChatGPT login.", Tags: []string{"reasoning", "tools", "coding"}},
		{ID: "codex-gpt-5.4", Provider: "openai-codex", Label: "GPT-5.4 (ChatGPT)", APIID: "gpt-5.4", Description: "Long-context workhorse through a ChatGPT login.", Tags: []string{"reasoning", "tools"}},
		{ID: "codex-gpt-5.4-codex", Provider: "openai-codex", Label: "GPT-5.4 Codex", APIID: "gpt-5.4-codex", Description: "Newest Codex-tuned model.", Tags: []string{"coding", "tools"}},
		{ID: "codex-gpt-5.3-codex", Provider: "openai-codex", Label: "GPT-5.3 Codex", APIID: "gpt-5.3-codex", Description: "Agentic software engineering.", Tags: []string{"coding", "tools"}},
		{ID: "codex-gpt-5.3-codex-spark", Provider: "openai-codex", Label: "GPT-5.3 Codex Spark", APIID: "gpt-5.3-codex-spark", Description: "Fast Codex variant, on the ChatGPT Pro entitlement.", Tags: []string{"coding", "fast"}},
		{ID: "codex-gpt-5.2-codex", Provider: "openai-codex", Label: "GPT-5.2 Codex", APIID: "gpt-5.2-codex", Description: "Previous Codex generation.", Tags: []string{"coding", "tools"}},
		{ID: "codex-gpt-5.1-codex-max", Provider: "openai-codex", Label: "GPT-5.1 Codex Max", APIID: "gpt-5.1-codex-max", Description: "Long-horizon Codex model.", Tags: []string{"coding", "tools"}},
		{ID: "codex-gpt-5.1-codex", Provider: "openai-codex", Label: "GPT-5.1 Codex", APIID: "gpt-5.1-codex", Description: "Earlier Codex model.", Tags: []string{"coding", "tools"}},
		{ID: "codex-gpt-5-codex", Provider: "openai-codex", Label: "GPT-5 Codex", APIID: "gpt-5-codex", Description: "First Codex generation.", Tags: []string{"coding", "tools"}},

		// Anthropic. Fable is the Mythos tier, above Opus.
		{ID: "claude-fable-5-1", Provider: "anthropic", Label: "Claude Fable 5.1", Description: "Deepest reasoning and long-horizon agentic work.", Tags: []string{"reasoning", "tools", "coding"}},
		{ID: "claude-opus-5-5", Provider: "anthropic", Label: "Claude Opus 5.5", Description: "Long-running agentic coding and knowledge work.", Tags: []string{"reasoning", "tools", "coding"}},
		{ID: "claude-sonnet-5", Provider: "anthropic", Label: "Claude Sonnet 5", Description: "Best combination of speed and intelligence.", Tags: []string{"reasoning", "tools", "coding"}},
		{ID: "claude-haiku-4-5", Provider: "anthropic", Label: "Claude Haiku 4.5", Description: "Fastest Claude, near-frontier.", Tags: []string{"fast", "tools"}},
		{ID: "claude-opus-5", Provider: "anthropic", Label: "Claude Opus 5", Description: "Previous flagship, still available.", Tags: []string{"reasoning", "tools"}},
		{ID: "claude-sonnet-4-6", Provider: "anthropic", Label: "Claude Sonnet 4.6", Description: "Previous Sonnet generation.", Tags: []string{"tools", "coding"}},

		// Google. The Flash line ships far more often than the Pro one.
		{ID: "gemini-3.1-pro-preview", Provider: "google", Label: "Gemini 3.1 Pro", Description: "Frontier reasoning with a large window.", Tags: []string{"reasoning", "tools", "vision"}},
		{ID: "gemini-3.8-flash", Provider: "google", Label: "Gemini 3.8 Flash", Description: "Newest Flash: software engineering and agentic tasks.", Tags: []string{"tools", "vision", "fast"}},
		{ID: "gemini-3.7-flash", Provider: "google", Label: "Gemini 3.7 Flash", Description: "Fast agentic workflows and coding.", Tags: []string{"tools", "fast"}},
		{ID: "gemini-3.5-flash", Provider: "google", Label: "Gemini 3.5 Flash", Description: "Near-Pro coding at Flash cost.", Tags: []string{"tools", "fast"}},
		{ID: "gemini-3.5-flash-lite", Provider: "google", Label: "Gemini 3.5 Flash Lite", Description: "Subagents and focused tasks.", Tags: []string{"fast"}},

		// xAI. Build is the coding-tuned line.
		{ID: "grok-4.7", Provider: "xai", Label: "Grok 4.7", Description: "Flagship for coding and agentic tasks.", Tags: []string{"reasoning", "tools", "coding"}},
		{ID: "grok-4.6", Provider: "xai", Label: "Grok 4.6", Description: "Previous flagship.", Tags: []string{"reasoning", "tools"}},
		{ID: "grok-4.5", Provider: "xai", Label: "Grok 4.5", Description: "Frontier coding and STEM.", Tags: []string{"reasoning", "tools"}},
		{ID: "grok-build-0.1", Provider: "xai", Label: "Grok Build 0.1", Description: "Fast model tuned for agentic coding.", Tags: []string{"coding", "fast"}},
		// The Grok models served through a SuperGrok login (OAuth).
		{ID: "grok-4.7-oauth", Provider: "xai-oauth", Label: "Grok 4.7 (OAuth)", APIID: "grok-4.7", Description: "Flagship coding model through a Grok login.", Tags: []string{"reasoning", "tools", "coding"}},
		{ID: "grok-4.5-oauth", Provider: "xai-oauth", Label: "Grok 4.5 (OAuth)", APIID: "grok-4.5", Description: "Frontier coding and STEM through a Grok login.", Tags: []string{"reasoning", "tools"}},
		{ID: "grok-build-0.1-oauth", Provider: "xai-oauth", Label: "Grok Build 0.1 (OAuth)", APIID: "grok-build-0.1", Description: "Fast agentic coding through a Grok login.", Tags: []string{"coding", "fast"}},

		// DeepSeek. Pro is the large MoE, Flash the cheap one.
		{ID: "deepseek-v4-pro", Provider: "deepseek", Label: "DeepSeek V4 Pro", Description: "Large MoE for advanced reasoning and coding.", Tags: []string{"reasoning", "tools", "coding"}},
		{ID: "deepseek-v4.1-flash", Provider: "deepseek", Label: "DeepSeek V4.1 Flash", APIID: "deepseek-flash", Description: "Sparse MoE, cheap and fast.", Tags: []string{"fast", "tools", "coding"}},
		{ID: "deepseek-v4-flash", Provider: "deepseek", Label: "DeepSeek V4 Flash", Description: "Cheapest DeepSeek tier.", Tags: []string{"fast", "coding"}},

		// StepFun. Three point seven Flash is the current efficient model.
		{ID: "step-3.7-flash", Provider: "stepfun", Label: "Step 3.7 Flash", Description: "Multimodal MoE for agentic coding.", Tags: []string{"tools", "vision", "coding"}},
		{ID: "step-3.5-flash", Provider: "stepfun", Label: "Step 3.5 Flash", Description: "Previous efficient generation.", Tags: []string{"fast", "tools"}},

		// StepFun Plan. The same models through the plan endpoint, so the plan
		// is a provider of its own rather than a hand-configured custom
		// endpoint. The host prefix keeps the id unique.
		{ID: "stepfun-plan/step-3.7-flash", Provider: "stepfun-plan", Label: "Step 3.7 Flash (Plan)", APIID: "step-3.7-flash", Description: "Step 3.7 Flash on the StepFun plan endpoint.", Tags: []string{"tools", "coding"}},
		{ID: "stepfun-plan/step-3.5-flash", Provider: "stepfun-plan", Label: "Step 3.5 Flash (Plan)", APIID: "step-3.5-flash", Description: "Step 3.5 Flash on the StepFun plan endpoint.", Tags: []string{"fast", "tools"}},

		// Moonshot. K3 is the open-weight flagship.
		{ID: "kimi-k3", Provider: "moonshot", Label: "Kimi K3", Description: "Open-weight multimodal reasoning at scale.", Tags: []string{"reasoning", "tools", "coding"}},
		{ID: "kimi-k2.7-code", Provider: "moonshot", Label: "Kimi K2.7 Code", Description: "Coding-focused, long contexts.", Tags: []string{"coding", "tools"}},
		{ID: "kimi-k2.6", Provider: "moonshot", Label: "Kimi K2.6", Description: "Long-horizon coding and UI generation.", Tags: []string{"coding", "tools"}},

		// MiniMax. M3 is multimodal with a 1M window.
		{ID: "minimax-m3", Provider: "minimax", Label: "MiniMax M3", Description: "Multimodal, long-horizon agentic work.", Tags: []string{"tools", "vision", "coding"}},
		{ID: "minimax-m2.7", Provider: "minimax", Label: "MiniMax M2.7", Description: "Compact agentic model.", Tags: []string{"tools", "fast"}},

		// Zhipu. Flash keeps a 1.3M window at a low price.
		{ID: "glm-5.3", Provider: "zhipu", Label: "GLM 5.3", Description: "Long-horizon software engineering.", Tags: []string{"reasoning", "tools", "coding"}},
		{ID: "glm-5.3-flash", Provider: "zhipu", Label: "GLM 5.3 Flash", Description: "Efficient coding with a very large window.", Tags: []string{"fast", "tools", "coding"}},
		{ID: "glm-4.7", Provider: "zhipu", Label: "GLM 4.7", Description: "Previous generation, cheap.", Tags: []string{"tools"}},

		// Alibaba Qwen.
		{ID: "qwen3.8-max", Provider: "qwen", Label: "Qwen3.8 Max", Description: "Flagship MoE, text image and video.", Tags: []string{"reasoning", "tools", "vision"}},
		{ID: "qwen3.8-27b", Provider: "qwen", Label: "Qwen3.8 27B", Description: "Open-weight dense vision-language model.", Tags: []string{"tools", "vision"}},
		{ID: "qwen3.7-max", Provider: "qwen", Label: "Qwen3.7 Max", Description: "Previous flagship.", Tags: []string{"tools"}},

		// Qwen Cloud Token Plan. The plan bundles Qwen, DeepSeek and GLM
		// models behind one endpoint, so each entry carries the host prefix and
		// the wire id the plan expects.
		{ID: "qwen-token-plan/qwen3.8-max", Provider: "qwen-token-plan", Label: "Qwen3.8 Max (Token Plan)", APIID: "qwen3.8-max", Description: "Qwen3.8 Max on the Qwen Cloud token plan.", Tags: []string{"reasoning", "tools", "vision"}},
		{ID: "qwen-token-plan/qwen3.8-27b", Provider: "qwen-token-plan", Label: "Qwen3.8 27B (Token Plan)", APIID: "qwen3.8-27b", Description: "Qwen3.8 27B on the Qwen Cloud token plan.", Tags: []string{"tools", "vision"}},
		{ID: "qwen-token-plan/qwen3.7-max", Provider: "qwen-token-plan", Label: "Qwen3.7 Max (Token Plan)", APIID: "qwen3.7-max", Description: "Qwen3.7 Max on the Qwen Cloud token plan.", Tags: []string{"tools"}},
		{ID: "qwen-token-plan/qwen3.8-flash", Provider: "qwen-token-plan", Label: "Qwen3.8 Flash (Token Plan)", APIID: "qwen3.8-flash", Description: "Fast Qwen3.8 tier on the Qwen Cloud token plan.", Tags: []string{"fast", "tools", "coding"}},
		{ID: "qwen-token-plan/qwen3.6-flash", Provider: "qwen-token-plan", Label: "Qwen3.6 Flash (Token Plan)", APIID: "qwen3.6-flash", Description: "Efficient Qwen3.6 tier on the Qwen Cloud token plan.", Tags: []string{"fast", "tools"}},
		{ID: "qwen-token-plan/deepseek-v4.1-flash", Provider: "qwen-token-plan", Label: "DeepSeek V4.1 Flash (Token Plan)", APIID: "deepseek-v4.1-flash", Description: "Sparse MoE on the Qwen Cloud token plan.", Tags: []string{"fast", "tools", "coding"}},
		{ID: "qwen-token-plan/deepseek-v4-pro-0813", Provider: "qwen-token-plan", Label: "DeepSeek V4 Pro 0813 (Token Plan)", APIID: "deepseek-v4-pro-0813", Description: "Pinned DeepSeek V4 Pro build on the Qwen Cloud token plan.", Tags: []string{"reasoning", "tools", "coding"}},
		{ID: "qwen-token-plan/deepseek-v4-pro", Provider: "qwen-token-plan", Label: "DeepSeek V4 Pro (Token Plan)", APIID: "deepseek-v4-pro", Description: "DeepSeek V4 Pro on the Qwen Cloud token plan.", Tags: []string{"reasoning", "tools", "coding"}},
		{ID: "qwen-token-plan/deepseek-v4-flash-0731", Provider: "qwen-token-plan", Label: "DeepSeek V4 Flash 0731 (Token Plan)", APIID: "deepseek-v4-flash-0731", Description: "Pinned DeepSeek V4 Flash build on the Qwen Cloud token plan.", Tags: []string{"fast", "coding"}},
		{ID: "qwen-token-plan/glm-5.3", Provider: "qwen-token-plan", Label: "GLM 5.3 (Token Plan)", APIID: "glm-5.3", Description: "Zhipu GLM 5.3 on the Qwen Cloud token plan.", Tags: []string{"reasoning", "tools", "coding"}},
		{ID: "qwen-token-plan/glm-5.2", Provider: "qwen-token-plan", Label: "GLM 5.2 (Token Plan)", APIID: "glm-5.2", Description: "Zhipu GLM 5.2 on the Qwen Cloud token plan.", Tags: []string{"tools", "coding"}},

		// Mistral. Large 3 and Devstral are the ones that matter here.
		{ID: "mistral-large-2512", Provider: "mistral", Label: "Mistral Large 3", Description: "Most capable Mistral.", Tags: []string{"tools", "coding"}},
		{ID: "devstral-2512", Provider: "mistral", Label: "Devstral 2", Description: "Open-weight agentic coding model.", Tags: []string{"coding", "tools"}},
		{ID: "mistral-medium-3-5", Provider: "mistral", Label: "Mistral Medium 3.5", Description: "Balanced agentic workhorse.", Tags: []string{"tools", "fast"}},
		{ID: "codestral-2508", Provider: "mistral", Label: "Codestral 2508", Description: "Low-latency code completion and repair.", Tags: []string{"coding", "fast"}},

		// Baidu.
		{ID: "ernie-4.5-300b-a47b", Provider: "baidu", Label: "ERNIE 4.5 300B", Description: "Baidu MoE flagship.", Tags: []string{"tools"}},

		// Volcengine.
		{ID: "doubao-seed-2-1-pro", Provider: "volcengine", Label: "Doubao Seed 2.1 Pro", Description: "ByteDance flagship on Volcengine.", Tags: []string{"tools", "vision"}},
		{ID: "doubao-seed-2-1-turbo", Provider: "volcengine", Label: "Doubao Seed 2.1 Turbo", Description: "Faster Seed tier.", Tags: []string{"fast", "tools"}},

		// Fast inference hosts: open weights, served quickly.
		{ID: "openai/gpt-oss-120b", Provider: "groq", Label: "GPT-OSS 120B (Groq)", Description: "Open-weight flagship at high speed.", Tags: []string{"reasoning", "tools", "fast"}},
		{ID: "openai/gpt-oss-20b", Provider: "groq", Label: "GPT-OSS 20B (Groq)", Description: "Small open-weight model.", Tags: []string{"fast"}},
		{ID: "llama-3.3-70b-versatile", Provider: "groq", Label: "Llama 3.3 70B (Groq)", Description: "Open model at very high speed.", Tags: []string{"fast", "tools"}},
		{ID: "qwen/qwen3.8-27b", Provider: "groq", Label: "Qwen3.8 27B (Groq)", Description: "Open vision-language model on Groq.", Tags: []string{"tools", "fast"}},

		{ID: "gpt-oss-120b", Provider: "cerebras", Label: "GPT-OSS 120B (Cerebras)", Description: "Open weights at wafer scale.", Tags: []string{"reasoning", "tools", "fast"}},
		{ID: "qwen-3.8-27b", Provider: "cerebras", Label: "Qwen3.8 27B (Cerebras)", Description: "Open vision-language model, very fast.", Tags: []string{"tools", "fast"}},

		// Aggregators and research endpoints.
		{ID: "openrouter/auto", Provider: "openrouter", Label: "OpenRouter Auto", Description: "Routes to the best model for the prompt.", Tags: []string{"tools"}},
		{ID: "anthropic/claude-opus-5.5", Provider: "openrouter", Label: "Claude Opus 5.5 (OR)", Description: "Anthropic via OpenRouter.", Tags: []string{"reasoning", "tools"}},
		{ID: "openai/gpt-6-astra", Provider: "openrouter", Label: "GPT-6 Astra (OR)", Description: "OpenAI via OpenRouter.", Tags: []string{"reasoning", "tools"}},
		{ID: "moonshotai/kimi-k3", Provider: "openrouter", Label: "Kimi K3 (OR)", Description: "Open weights via OpenRouter.", Tags: []string{"reasoning", "tools"}},
		{ID: "z-ai/glm-5.3", Provider: "openrouter", Label: "GLM 5.3 (OR)", Description: "Zhipu via OpenRouter.", Tags: []string{"tools"}},
		{ID: "qwen/qwen3.8-max", Provider: "openrouter", Label: "Qwen3.8 Max (OR)", Description: "Alibaba via OpenRouter.", Tags: []string{"tools", "vision"}},
		{ID: "deepseek/deepseek-v4-pro", Provider: "openrouter", Label: "DeepSeek V4 Pro (OR)", Description: "DeepSeek via OpenRouter.", Tags: []string{"reasoning"}},

		// Open weights on a third-party host.
		//
		// The stable id is prefixed with the host because several hosts serve
		// the same model: two catalogue entries sharing one id would make
		// ModelByID ambiguous, and the picker would silently switch provider.
		// APIID carries the name that host actually expects on the wire.
		{ID: "together/kimi-k3", Provider: "together", Label: "Kimi K3 (Together)", APIID: "moonshotai/Kimi-K3", Description: "Kimi K3 on Together AI.", Tags: []string{"reasoning", "tools"}},
		{ID: "together/qwen3.8-27b", Provider: "together", Label: "Qwen3.8 27B (Together)", APIID: "Qwen/Qwen3.8-27B", Description: "Open vision-language model.", Tags: []string{"tools"}},
		{ID: "deepinfra/kimi-k3", Provider: "deepinfra", Label: "Kimi K3 (DeepInfra)", APIID: "moonshotai/Kimi-K3", Description: "Kimi K3 on DeepInfra.", Tags: []string{"reasoning", "tools"}},
		{ID: "deepinfra/qwen3.8-27b", Provider: "deepinfra", Label: "Qwen3.8 27B (DeepInfra)", APIID: "Qwen/Qwen3.8-27B", Description: "Open vision-language model.", Tags: []string{"tools"}},
		{ID: "fireworks/deepseek-v4-pro", Provider: "fireworks", Label: "DeepSeek V4 Pro (Fireworks)", APIID: "accounts/fireworks/models/deepseek-v4-pro", Description: "DeepSeek hosted by Fireworks.", Tags: []string{"reasoning", "tools"}},
		{ID: "fireworks/kimi-k3", Provider: "fireworks", Label: "Kimi K3 (Fireworks)", APIID: "accounts/fireworks/models/kimi-k3", Description: "Kimi hosted by Fireworks.", Tags: []string{"reasoning", "tools"}},
		{ID: "siliconflow/deepseek-v4-pro", Provider: "siliconflow", Label: "DeepSeek V4 Pro (SiliconFlow)", APIID: "deepseek-ai/DeepSeek-V4-Pro", Description: "DeepSeek on SiliconFlow.", Tags: []string{"reasoning"}},
		{ID: "novita/deepseek-v4-pro", Provider: "novita", Label: "DeepSeek V4 Pro (Novita)", APIID: "deepseek/deepseek-v4-pro", Description: "DeepSeek on Novita.", Tags: []string{"reasoning"}},
		{ID: "nvidia/kimi-k3", Provider: "nvidia", Label: "Kimi K3 (NVIDIA NIM)", APIID: "moonshotai/kimi-k3", Description: "Kimi K3 on NVIDIA NIM.", Tags: []string{"reasoning", "tools"}},
		{ID: "nebius/kimi-k3", Provider: "nebius", Label: "Kimi K3 (Nebius)", APIID: "moonshotai/Kimi-K3", Description: "Kimi K3 on Nebius.", Tags: []string{"reasoning", "tools"}},
		{ID: "sambanova/minimax-m2.7", Provider: "sambanova", Label: "MiniMax M2.7 (SambaNova)", APIID: "MiniMax-M2.7", Description: "Agentic model on SambaNova.", Tags: []string{"tools", "fast"}},
		{ID: "hyperbolic/qwen3.8-27b", Provider: "hyperbolic", Label: "Qwen3.8 27B (Hyperbolic)", APIID: "Qwen/Qwen3.8-27B", Description: "Open vision-language model.", Tags: []string{"tools"}},
		{ID: "huggingface/glm-5.3", Provider: "huggingface", Label: "GLM 5.3 (HF Router)", APIID: "zai-org/GLM-5.3", Description: "Zhipu via the Hugging Face router.", Tags: []string{"tools"}},
		{ID: "vercel/minimax-m3", Provider: "vercel", Label: "MiniMax M3 (AI Gateway)", APIID: "minimax/minimax-m3", Description: "One key across many vendors.", Tags: []string{"tools"}},
		{ID: "github/gpt-6-astra", Provider: "github", Label: "GPT-6 Astra (GitHub)", APIID: "openai/gpt-6-astra", Description: "Frontier models billed to a GitHub account.", Tags: []string{"reasoning", "tools"}},

		{ID: "sonar-pro", Provider: "perplexity", Label: "Sonar Pro", Description: "Search-backed answers with citations.", Tags: []string{"search"}},
		{ID: "sonar-deep-research", Provider: "perplexity", Label: "Sonar Deep Research", Description: "Multi-step retrieval and synthesis.", Tags: []string{"search", "reasoning"}},

		{ID: "command-a-plus-05-2026", Provider: "cohere", Label: "Command A+", Description: "Enterprise agentic workflows.", Tags: []string{"tools"}},

		// Local servers. These ids are what the servers themselves use, so they
		// resolve without the operator adding anything to the config. The list
		// covers the tags people actually pull, which is why a couple of older
		// ones are kept: an operator who already has the model should find it.
		{ID: "qwen3.8:27b", Provider: "ollama", Label: "Qwen3.8 27B", Description: "Local vision-language model.", Tags: []string{"local", "tools"}},
		{ID: "qwen3-coder:30b", Provider: "ollama", Label: "Qwen3 Coder 30B", Description: "Local coding model.", Tags: []string{"local", "coding"}},
		{ID: "gpt-oss:20b", Provider: "ollama", Label: "GPT-OSS 20B", Description: "Local open-weight model.", Tags: []string{"local", "tools"}},
		{ID: "glm-4.7-flash:latest", Provider: "ollama", Label: "GLM 4.7 Flash", Description: "Local agentic coding model.", Tags: []string{"local", "coding"}},
		{ID: "deepseek-v4-flash:latest", Provider: "ollama", Label: "DeepSeek V4 Flash", Description: "Local DeepSeek.", Tags: []string{"local", "coding"}},
		{ID: "qwen2.5-coder:latest", Provider: "ollama", Label: "Qwen2.5 Coder", Description: "Older local coding model, still widely pulled.", Tags: []string{"local", "coding"}},
		{ID: "llama3.2:latest", Provider: "ollama", Label: "Llama 3.2", Description: "Older local general model.", Tags: []string{"local"}},

		{ID: "qwen3.8-27b-lmstudio", Provider: "lmstudio", Label: "Qwen3.8 27B (LM Studio)", APIID: "qwen3.8-27b", Description: "Loaded in LM Studio.", Tags: []string{"local", "tools"}},
		{ID: "qwen3.8-27b-mlx", Provider: "mlx", Label: "Qwen3.8 27B (MLX)", APIID: "qwen3.8-27b", Description: "Loaded in MLX on Apple silicon.", Tags: []string{"local", "tools"}},
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
