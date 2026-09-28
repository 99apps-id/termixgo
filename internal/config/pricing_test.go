package config

import "testing"

func TestPriceForFindsAConfiguredModel(t *testing.T) {
	cfg := Default()
	cfg.ModelPricing = map[string]ModelPrice{
		"my-custom-model": {InputPerMillion: 1.5, OutputPerMillion: 6},
	}

	price, ok := cfg.PriceFor("my-custom-model")
	if !ok {
		t.Fatalf("the configured model should resolve")
	}
	if price.InputPerMillion != 1.5 || price.OutputPerMillion != 6 {
		t.Errorf("price = %+v", price)
	}
	if !price.Known() {
		t.Errorf("a price above zero should report as known")
	}
}

func TestPriceForIsCaseInsensitive(t *testing.T) {
	cfg := Default()
	cfg.ModelPricing = map[string]ModelPrice{"My-Model": {InputPerMillion: 2}}

	if _, ok := cfg.PriceFor("my-model"); !ok {
		t.Errorf("a hand-written config should not be case sensitive")
	}
	if _, ok := cfg.PriceFor("MY-MODEL"); !ok {
		t.Errorf("upper case should match too")
	}
}

func TestPriceForIgnoresAnEmptyEntry(t *testing.T) {
	cfg := Default()
	cfg.ModelPricing = map[string]ModelPrice{"listed-but-zero": {}}

	// An entry of zeroes is indistinguishable from no entry, so it must not
	// be treated as a real price of zero dollars.
	if _, ok := cfg.PriceFor("listed-but-zero"); ok {
		t.Errorf("a zero price should not count as configured")
	}
	if _, ok := cfg.PriceFor(""); ok {
		t.Errorf("an empty id must not resolve")
	}
	if _, ok := cfg.PriceFor("not-configured"); ok {
		t.Errorf("an unconfigured model must not resolve")
	}
}

func TestModelPriceSurvivesARoundTrip(t *testing.T) {
	t.Setenv(EnvHome, t.TempDir())

	cfg := Default()
	cfg.ModelPricing = map[string]ModelPrice{
		"gateway/llama-3": {InputPerMillion: 0.2, OutputPerMillion: 0.4},
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	price, ok := loaded.PriceFor("gateway/llama-3")
	if !ok {
		t.Fatalf("the price did not survive the round trip: %+v", loaded.ModelPricing)
	}
	if price.InputPerMillion != 0.2 || price.OutputPerMillion != 0.4 {
		t.Errorf("price = %+v", price)
	}
}

func TestEqualFold(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"abc", "ABC", true},
		{"abc", "abc", true},
		{"abc", "abd", false},
		{"abc", "ab", false},
		{"", "", true},
		{"a", "", false},
		{"MiXeD", "mixed", true},
	}
	for _, testCase := range cases {
		if got := equalFold(testCase.a, testCase.b); got != testCase.want {
			t.Errorf("equalFold(%q, %q) = %v, want %v", testCase.a, testCase.b, got, testCase.want)
		}
	}
}
