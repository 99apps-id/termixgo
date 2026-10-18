package config

// ModelPrice is an operator-supplied token price, in US dollars per million
// tokens. It exists so a model the built-in table does not know, most often one
// behind a custom endpoint, still gets a working cost budget.
//
// It lives in this package rather than provider to avoid an import cycle:
// provider already imports config for base URLs and trust.
type ModelPrice struct {
	InputPerMillion  float64 `json:"inputPerMillion"`
	OutputPerMillion float64 `json:"outputPerMillion"`
	// CacheReadMultiplier and CacheWriteMultiplier override the vendor's
	// cached-input rate as a fraction of input, for a model whose cache is
	// priced differently. Zero means "use the provider default".
	CacheReadMultiplier  float64 `json:"cacheReadMultiplier,omitempty"`
	CacheWriteMultiplier float64 `json:"cacheWriteMultiplier,omitempty"`
}

// Known reports whether either rate is set above zero.
func (p ModelPrice) Known() bool { return p.InputPerMillion > 0 || p.OutputPerMillion > 0 }

// PriceFor returns the operator's price for a model id, or false when none is
// configured. The lookup is case-insensitive on the key so a hand-written
// config is forgiving about capitalisation.
func (c Config) PriceFor(modelID string) (ModelPrice, bool) {
	if modelID == "" {
		return ModelPrice{}, false
	}
	if price, ok := c.ModelPricing[modelID]; ok && price.Known() {
		return price, true
	}
	for key, price := range c.ModelPricing {
		if equalFold(key, modelID) && price.Known() {
			return price, true
		}
	}
	return ModelPrice{}, false
}

// equalFold compares two short ASCII strings without allocating.
func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := 0; index < len(a); index++ {
		left, right := a[index], b[index]
		if left >= 'A' && left <= 'Z' {
			left += 'a' - 'A'
		}
		if right >= 'A' && right <= 'Z' {
			right += 'a' - 'A'
		}
		if left != right {
			return false
		}
	}
	return true
}
