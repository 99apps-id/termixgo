package provider

import (
	"strings"

	"github.com/99apps-id/termixgo/internal/config"
)

// Pricing is the cost of a model in US dollars per million tokens.
//
// These are published list prices, kept for budgeting rather than billing.
// Vendors change them, and discounts, caching and batch tiers are not modelled,
// so treat a figure from here as an estimate with a margin of error, never as
// an invoice. Update the table when a number visibly drifts.
type Pricing struct {
	InputPerMillion  float64
	OutputPerMillion float64
	// CacheReadMultiplier is the fraction of the input rate charged for a
	// cache read. Zero means the default (0.10), which is Anthropic, OpenAI
	// GPT-5/6 and Gemini's published rate. A model that prices a cached read at
	// 50% of input (some earlier OpenAI tiers) records 0.50 here.
	CacheReadMultiplier float64
	// CacheWriteMultiplier is the fraction of the input rate charged for a
	// cache write. Zero means the default (1.25), Anthropic's published rate.
	// Most non-Anthropic providers report no cache write at all.
	CacheWriteMultiplier float64
}

// Known reports whether a price is recorded for this model. A zero price is
// legitimate for a local model, so callers need this to tell "free" from
// "unknown" before showing a number.
func (p Pricing) Known() bool { return p.InputPerMillion > 0 || p.OutputPerMillion > 0 }

// pricingTable maps a model id or wire id to its price.
//
// Figures are the vendors' published list prices at the time of writing, in
// dollars per million tokens. Long-context tiers are not modelled: a vendor that
// doubles the rate above a prompt threshold is recorded at the base rate, so a
// very large prompt is under-estimated rather than over-estimated.
var pricingTable = map[string]Pricing{
	// OpenAI.
	"gpt-6-astra":   {InputPerMillion: 10.00, OutputPerMillion: 50.00},
	"gpt-6-sol":     {InputPerMillion: 2.00, OutputPerMillion: 10.00},
	"gpt-6-luna":    {InputPerMillion: 0.10, OutputPerMillion: 0.50},
	"gpt-5.6-terra": {InputPerMillion: 2.00, OutputPerMillion: 12.00},
	"gpt-5.6-sol":   {InputPerMillion: 2.00, OutputPerMillion: 10.00},
	"gpt-5.6-luna":  {InputPerMillion: 0.15, OutputPerMillion: 0.75},
	"gpt-5.5":       {InputPerMillion: 5.00, OutputPerMillion: 30.00},
	"gpt-5.4":       {InputPerMillion: 2.50, OutputPerMillion: 15.00},
	"gpt-5.4-mini":  {InputPerMillion: 0.75, OutputPerMillion: 4.50},
	"gpt-5.3-codex": {InputPerMillion: 1.75, OutputPerMillion: 14.00},

	// Anthropic.
	"claude-fable-5-1":  {InputPerMillion: 10.00, OutputPerMillion: 50.00},
	"claude-opus-5-5":   {InputPerMillion: 4.00, OutputPerMillion: 20.00},
	"claude-opus-5":     {InputPerMillion: 5.00, OutputPerMillion: 25.00},
	"claude-sonnet-5-5": {InputPerMillion: 2.00, OutputPerMillion: 10.00},
	"claude-sonnet-5":   {InputPerMillion: 2.00, OutputPerMillion: 10.00},
	"claude-sonnet-4-6": {InputPerMillion: 3.00, OutputPerMillion: 15.00},
	"claude-haiku-4-5":  {InputPerMillion: 1.00, OutputPerMillion: 5.00},

	// Google.
	"gemini-3.1-pro-preview": {InputPerMillion: 2.00, OutputPerMillion: 12.00},
	"gemini-3.8-flash":       {InputPerMillion: 0.75, OutputPerMillion: 3.75},
	"gemini-3.7-flash":       {InputPerMillion: 0.75, OutputPerMillion: 3.75},
	"gemini-3.5-flash":       {InputPerMillion: 1.50, OutputPerMillion: 9.00},
	"gemini-3.5-flash-lite":  {InputPerMillion: 0.30, OutputPerMillion: 2.50},

	// xAI.
	"grok-4.7":       {InputPerMillion: 2.00, OutputPerMillion: 6.00},
	"grok-4.6":       {InputPerMillion: 2.00, OutputPerMillion: 6.00},
	"grok-4.5":       {InputPerMillion: 2.00, OutputPerMillion: 6.00},
	"grok-build-0.1": {InputPerMillion: 1.00, OutputPerMillion: 2.00},

	// DeepSeek. The published table has an off-peak rate at half price; the
	// peak (standard) rate is recorded, so a budget errs high.
	"deepseek-v4-pro":        {InputPerMillion: 1.32, OutputPerMillion: 3.96},
	"deepseek-v4-pro-0813":   {InputPerMillion: 1.32, OutputPerMillion: 3.96},
	"deepseek-v4.1-flash":    {InputPerMillion: 0.30, OutputPerMillion: 1.20},
	"deepseek-v4-flash":      {InputPerMillion: 0.30, OutputPerMillion: 1.20},
	"deepseek-v4-flash-0731": {InputPerMillion: 0.30, OutputPerMillion: 1.20},

	// StepFun.
	"step-3.7-flash": {InputPerMillion: 0.20, OutputPerMillion: 1.15},
	"step-3.5-flash": {InputPerMillion: 0.10, OutputPerMillion: 0.30},

	// Moonshot.
	"kimi-k3":        {InputPerMillion: 3.00, OutputPerMillion: 15.00},
	"kimi-k2.7-code": {InputPerMillion: 0.95, OutputPerMillion: 4.00},
	"kimi-k2.6":      {InputPerMillion: 0.95, OutputPerMillion: 4.00},

	// MiniMax.
	"minimax-m3":   {InputPerMillion: 0.30, OutputPerMillion: 1.20},
	"minimax-m2.7": {InputPerMillion: 0.30, OutputPerMillion: 1.20},

	// Zhipu.
	"glm-5.3":       {InputPerMillion: 1.40, OutputPerMillion: 4.40},
	"glm-5.3-flash": {InputPerMillion: 0.15, OutputPerMillion: 0.50},
	"glm-5.2":       {InputPerMillion: 0.40, OutputPerMillion: 2.00},
	"glm-4.7":       {InputPerMillion: 0.60, OutputPerMillion: 2.20},

	// Alibaba Qwen.
	"qwen3.8-max":   {InputPerMillion: 2.00, OutputPerMillion: 6.00},
	"qwen3.8-27b":   {InputPerMillion: 0.50, OutputPerMillion: 3.00},
	"qwen3.8-flash": {InputPerMillion: 0.30, OutputPerMillion: 1.20},
	"qwen3.7-max":   {InputPerMillion: 2.50, OutputPerMillion: 7.50},
	"qwen3.6-flash": {InputPerMillion: 0.20, OutputPerMillion: 0.80},

	// Mistral.
	"mistral-large-2512": {InputPerMillion: 0.50, OutputPerMillion: 1.50},
	"devstral-2512":      {InputPerMillion: 0.40, OutputPerMillion: 2.00},
	"mistral-medium-3-5": {InputPerMillion: 1.50, OutputPerMillion: 7.50},
	"codestral-2508":     {InputPerMillion: 0.30, OutputPerMillion: 0.90},

	// Baidu.
	"ernie-4.5-300b-a47b": {InputPerMillion: 0.90, OutputPerMillion: 3.60},

	// Volcengine.
	"doubao-seed-2-1-pro":   {InputPerMillion: 0.60, OutputPerMillion: 3.00},
	"doubao-seed-2-1-turbo": {InputPerMillion: 0.30, OutputPerMillion: 1.50},

	// Fast inference hosts.
	"openai/gpt-oss-120b":     {InputPerMillion: 0.15, OutputPerMillion: 0.75},
	"openai/gpt-oss-20b":      {InputPerMillion: 0.10, OutputPerMillion: 0.50},
	"llama-3.3-70b-versatile": {InputPerMillion: 0.59, OutputPerMillion: 0.79},
	"qwen/qwen3.8-27b":        {InputPerMillion: 0.80, OutputPerMillion: 4.00},
	"gpt-oss-120b":            {InputPerMillion: 0.35, OutputPerMillion: 0.75},
	"qwen-3.8-27b":            {InputPerMillion: 0.35, OutputPerMillion: 0.75},

	// Third-party hosts of open weights.
	"together/kimi-k3":            {InputPerMillion: 3.00, OutputPerMillion: 15.00},
	"together/qwen3.8-27b":        {InputPerMillion: 0.42, OutputPerMillion: 3.00},
	"deepinfra/kimi-k3":           {InputPerMillion: 2.85, OutputPerMillion: 14.25},
	"deepinfra/qwen3.8-27b":       {InputPerMillion: 0.20, OutputPerMillion: 2.50},
	"fireworks/deepseek-v4-pro":   {InputPerMillion: 0.90, OutputPerMillion: 3.60},
	"fireworks/kimi-k3":           {InputPerMillion: 3.00, OutputPerMillion: 15.00},
	"siliconflow/deepseek-v4-pro": {InputPerMillion: 1.50, OutputPerMillion: 3.14},
	"novita/deepseek-v4-pro":      {InputPerMillion: 1.60, OutputPerMillion: 3.20},
	"nvidia/kimi-k3":              {InputPerMillion: 3.00, OutputPerMillion: 15.00},
	"nebius/kimi-k3":              {InputPerMillion: 2.80, OutputPerMillion: 14.00},
	"sambanova/minimax-m2.7":      {InputPerMillion: 0.60, OutputPerMillion: 2.40},
	"hyperbolic/qwen3.8-27b":      {InputPerMillion: 0.40, OutputPerMillion: 2.50},
	"huggingface/glm-5.3":         {InputPerMillion: 1.40, OutputPerMillion: 4.40},
	"vercel/minimax-m3":           {InputPerMillion: 0.24, OutputPerMillion: 0.96},
	"github/gpt-6-astra":          {InputPerMillion: 10.00, OutputPerMillion: 50.00},

	// Aggregators.
	"anthropic/claude-opus-5.5": {InputPerMillion: 4.00, OutputPerMillion: 20.00},
	"openai/gpt-6-astra":        {InputPerMillion: 10.00, OutputPerMillion: 50.00},
	"moonshotai/kimi-k3":        {InputPerMillion: 3.00, OutputPerMillion: 15.00},
	"z-ai/glm-5.3":              {InputPerMillion: 1.40, OutputPerMillion: 4.40},
	"qwen/qwen3.8-max":          {InputPerMillion: 2.00, OutputPerMillion: 6.00},
	"deepseek/deepseek-v4-pro":  {InputPerMillion: 0.94, OutputPerMillion: 1.87},

	// Search and enterprise endpoints.
	"sonar-pro":              {InputPerMillion: 3.00, OutputPerMillion: 15.00},
	"sonar-deep-research":    {InputPerMillion: 2.00, OutputPerMillion: 8.00},
	"command-a-plus-05-2026": {InputPerMillion: 2.50, OutputPerMillion: 10.00},
}

// PlanInfo describes a subscription that bills in credits rather than in
// dollars per token. A plan model has no dollar price: the operator has already
// paid for a quota, so the marginal dollar cost of a token is not recorded.
type PlanInfo struct {
	// Name is the product name shown to the operator.
	Name string
	// CreditUnit is the unit the plan deducts, such as "Credits".
	CreditUnit string
	// USDPerCredit is the marginal value of one credit, taken from the plan's
	// Credit Pack price. It is zero when the vendor does not publish one, which
	// means a credit cannot be converted to dollars at all.
	USDPerCredit float64
}

// planProviders are the subscriptions that bill in credits. Their models are
// deliberately absent from the dollar table: a plan is a prepaid quota, and a
// per-token dollar figure is money the operator never pays.
//
// Sources:
//   - Qwen Cloud Token Plan, https://docs.qwencloud.com/token-plan/overview and
//     /token-plan/personal/token-plan-personal-overview. A Credit Pack is $15
//     for 20,000 Credits. Per-request Credits are dynamic, so no per-model rate
//     is published; the quota alone (Lite 11,500 to Pro 180,000 per month) is.
//   - StepFun Step Plan, https://platform.stepfun.ai/step-plan. Mini 400M,
//     Plus 1,600M, Pro 8,000M and Max 40,000M credits per month; no credit pack
//     price is published.
var planProviders = map[string]PlanInfo{
	"qwen-token-plan": {Name: "Qwen Cloud Token Plan", CreditUnit: "Credits", USDPerCredit: 15.0 / 20000.0},
	"stepfun-plan":    {Name: "StepFun Step Plan", CreditUnit: "M Credits"},
	// An OAuth login is a subscription, not a per-token bill: a ChatGPT or
	// SuperGrok plan has no dollar rate per model, and inventing one would be
	// money the operator never pays.
	"openai-codex":   {Name: "ChatGPT (Codex)", CreditUnit: "subscription"},
	"xai-oauth":      {Name: "SuperGrok", CreditUnit: "subscription"},
	"claude-oauth":   {Name: "Claude", CreditUnit: "subscription"},
	"antigravity":    {Name: "Google Antigravity", CreditUnit: "subscription"},
	"github-copilot": {Name: "GitHub Copilot", CreditUnit: "subscription"},
	"muse":           {Name: "Meta Muse Code", CreditUnit: "subscription"},
}

// Plan returns the subscription a model is served under, when its provider
// bills in credits instead of dollars.
func (m Model) Plan() (PlanInfo, bool) {
	info, ok := planProviders[m.Provider]
	return info, ok
}

// PlanBilled reports whether the model is served under a credit subscription.
func (m Model) PlanBilled() bool {
	_, ok := planProviders[m.Provider]
	return ok
}

// Local providers run on the operator's own hardware, so their marginal cost
// is zero. Listing them explicitly keeps "unknown" distinct from "free".
var localProviders = map[string]bool{
	"ollama": true, "lmstudio": true, "mlx": true,
}

// routingModels bill at whichever model the router picks, so no list price
// exists for them. They are the one honest gap in the table: a cost budget
// cannot be trusted for them, and saying so beats inventing a number.
var routingModels = map[string]bool{
	"openrouter/auto": true,
}

// PriceVaries reports whether the model is a router whose cost depends on the
// model it selects, which means no list price applies to it.
func (m Model) PriceVaries() bool { return routingModels[m.ID] }

// Pricing returns the recorded price for a model, or the zero value when none
// is known.
func (m Model) Pricing() Pricing {
	return m.PricingWith(nil)
}

// PricingWith returns the price to use for a model, preferring an operator
// override over the built-in table.
//
// The override is consulted by stable id first and wire id second, so a config
// entry keeps working when a vendor renames the model behind a stable id. This
// is what makes a cost budget meaningful for a model the table does not list.
func (m Model) PricingWith(overrides map[string]Pricing) Pricing {
	if price, ok := overrides[m.ID]; ok && price.Known() {
		return price
	}
	if wire := m.WireID(); wire != m.ID {
		if price, ok := overrides[wire]; ok && price.Known() {
			return price
		}
	}
	// A subscription plan bills in credits, not dollars per token, so its
	// models have no dollar price unless the operator set an override above.
	if m.PlanBilled() {
		return Pricing{}
	}
	// A local server has no vendor price. Its wire name can collide with a
	// cloud model of the same name, and billing someone for tokens their own
	// machine produced would be plain wrong.
	if m.Free() {
		return Pricing{}
	}
	if price, ok := pricingTable[m.ID]; ok {
		return price
	}
	if wire := m.WireID(); wire != m.ID {
		if price, ok := pricingTable[wire]; ok {
			return price
		}
	}
	return Pricing{}
}

// Cost estimates the dollars a usage report costs at this price. An unknown
// price yields zero, and callers use Known() to say unknown, not zero.
//
// A cache read and write are subtracted from the prompt total before the
// regular rate is applied, so a token that is reported in both PromptTokens and
// the cache counters is charged once, not twice. The cached parts are billed at
// p.CacheReadMultiplier and p.CacheWriteMultiplier of the input rate, falling
// back to the Anthropic/GPT/Gemini defaults of a tenth and 1.25x.
func (p Pricing) Cost(usage Usage) float64 {
	inputTokens := usage.PromptTokens
	cacheRead := usage.CacheReadTokens
	cacheWrite := usage.CacheWriteTokens
	if cacheRead < 0 {
		cacheRead = 0
	}
	if cacheWrite < 0 {
		cacheWrite = 0
	}
	if cacheRead+cacheWrite > inputTokens {
		// Defensive: never let the cached parts exceed the total, which would
		// make the regular part negative.
		cacheRead = inputTokens
		cacheWrite = 0
	}
	regularInput := inputTokens - cacheRead - cacheWrite

	readMultiplier := p.CacheReadMultiplier
	if readMultiplier == 0 {
		readMultiplier = DefaultCacheReadMultiplier
	}
	writeMultiplier := p.CacheWriteMultiplier
	if writeMultiplier == 0 {
		writeMultiplier = DefaultCacheWriteMultiplier
	}

	return (float64(regularInput)*p.InputPerMillion +
		float64(cacheRead)*p.InputPerMillion*readMultiplier +
		float64(cacheWrite)*p.InputPerMillion*writeMultiplier +
		float64(usage.CompletionTokens)*p.OutputPerMillion) / 1_000_000
}

// Default cache price multipliers relative to the standard input rate. They
// are Anthropic's published figures, shared by OpenAI GPT-5/6 (cached input at
// a tenth) and Gemini 3 (cached input at a tenth). A model that deviates
// records its own rate on the Pricing entry instead.
const (
	DefaultCacheReadMultiplier  = 0.10
	DefaultCacheWriteMultiplier = 1.25
)

// Cost estimates the dollars a usage report costs on this model. An unknown
// price yields zero, and callers use Pricing().Known() to say "unknown"
// rather than "$0.00".
func (m Model) Cost(usage Usage) float64 {
	return m.Pricing().Cost(usage)
}

// CostModel resolves the price to use for a model and whether a price is
// actually known for it.
//
// The second return value is what separates three cases that all look like
// zero dollars: a local model that is genuinely free, a model with a recorded
// price, and a model nobody has priced. Only the last should tell the operator
// that a cost budget cannot work.
func (m Model) CostModel(overrides map[string]Pricing) (Pricing, bool) {
	price := m.PricingWith(overrides)
	if price.Known() {
		return price, true
	}
	if m.Free() {
		return Pricing{}, true
	}
	return Pricing{}, false
}

// Free reports whether the model runs locally, where tokens cost nothing
// beyond electricity.
func (m Model) Free() bool { return localProviders[m.Provider] }

// OverridesFrom converts the configured price list into the shape the resolver
// takes. A nil result means the operator has configured nothing, which keeps
// the common case free of an allocation.
func OverridesFrom(prices map[string]config.ModelPrice) map[string]Pricing {
	if len(prices) == 0 {
		return nil
	}
	overrides := make(map[string]Pricing, len(prices))
	for id, price := range prices {
		overrides[id] = Pricing{
			InputPerMillion:      price.InputPerMillion,
			OutputPerMillion:     price.OutputPerMillion,
			CacheReadMultiplier:  price.CacheReadMultiplier,
			CacheWriteMultiplier: price.CacheWriteMultiplier,
		}
	}
	return overrides
}

// PricedFor resolves what a model costs under the operator's own price list,
// and whether that figure is real.
//
// A caller needs the second value because it separates three states that all
// look like zero: a local model that is genuinely free, a model whose price is
// known, and one nobody has priced. Only the last means a cost cap cannot
// work, and that is worth telling the operator about.
func PricedFor(cfg config.Config, model Model) (Pricing, bool) {
	return model.CostModel(OverridesFrom(cfg.ModelPricing))
}

// PricedByID resolves the same question for a model id, including one the
// catalogue does not list. It is what lets a diagnostic answer "can this cap
// fire at all" before a single token has been spent.
func PricedByID(cfg config.Config, id string) (Pricing, bool) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return Pricing{}, false
	}
	if model, ok := ModelByID(trimmed); ok {
		return PricedFor(cfg, model)
	}

	model := Model{ID: trimmed}
	// A local server's tags keep their colon: it belongs to the tag, not to a
	// provider prefix.
	if index := strings.Index(trimmed, ":"); index > 0 && !IsLocal(trimmed) {
		model.Provider = trimmed[:index]
	}
	if price, known := PricedFor(cfg, model); known {
		return price, true
	}
	// A hand-edited config can hold the prefixed spelling of a model the app
	// would store bare, so the unprefixed form is tried as well. Without this
	// the same model would be priced or not depending on who wrote the config.
	if index := strings.Index(trimmed, ":"); index > 0 && model.Provider != "" {
		bare := Model{ID: trimmed[index+1:], Provider: model.Provider}
		return PricedFor(cfg, bare)
	}
	return Pricing{}, false
}
