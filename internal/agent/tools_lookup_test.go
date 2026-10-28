package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// restoreLookupEndpoints puts every lookup source back to its production URL.
func restoreLookupEndpoints() {
	lookupGeocodeEndpoint = "https://geocoding-api.open-meteo.com/v1/search"
	lookupForecastEndpoint = "https://api.open-meteo.com/v1/forecast"
	lookupWttrEndpoint = "https://wttr.in"
	lookupFXEndpoint = "https://api.frankfurter.app/latest"
	lookupCryptoEndpoint = "https://api.coingecko.com/api/v3/simple/price"
	lookupWikiEndpoint = "https://en.wikipedia.org/api/rest_v1/page/summary/"
}

func TestLookupWeather(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if strings.Contains(request.URL.Path, "geocode") {
			fmt.Fprint(writer, `{"results":[{"name":"Jakarta","country":"Indonesia","latitude":-6.2,"longitude":106.8}]}`)
			return
		}
		fmt.Fprint(writer, `{"current":{"temperature_2m":31.2,"apparent_temperature":34,"relative_humidity_2m":70,"precipitation":0,"weather_code":0,"wind_speed_10m":12},"daily":{"temperature_2m_max":[30.5],"temperature_2m_min":[24.1],"precipitation_probability_max":[60],"weather_code":[61]},"timezone":"Asia/Jakarta"}`)
	}))
	t.Cleanup(func() {
		server.Close()
		restoreLookupEndpoints()
	})
	lookupGeocodeEndpoint = server.URL + "/geocode"
	lookupForecastEndpoint = server.URL + "/forecast"

	result, err := (&lookupTool{}).Run(context.Background(), &Env{}, map[string]any{"kind": "weather", "query": "Jakarta"})
	if err != nil || result.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, result)
	}
	for _, want := range []string{"Jakarta", "31.2", "clear", "rain chance 60%"} {
		if !strings.Contains(result.Output, want) {
			t.Errorf("output is missing %q:\n%s", want, result.Output)
		}
	}
}

func TestLookupWeatherFallbackWhenPrimaryFails(t *testing.T) {
	wttrServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"current_condition":[{"temp_C":"28","FeelsLikeC":"31","humidity":"75","precipMM":"0.0","windspeedKmph":"10","weatherDesc":[{"value":"Partly Cloudy"}]}],"nearest_area":[{"areaName":[{"value":"Jakarta"}],"country":[{"value":"Indonesia"}]}]}`)
	}))
	defer wttrServer.Close()

	// Broken primary Open-Meteo endpoint (immediate 500 error server)
	brokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "TLS handshake timeout", http.StatusBadGateway)
	}))
	defer brokenServer.Close()

	lookupGeocodeEndpoint = brokenServer.URL
	lookupForecastEndpoint = brokenServer.URL
	lookupWttrEndpoint = wttrServer.URL
	t.Cleanup(func() {
		restoreLookupEndpoints()
	})

	// When primary fails, lookupWeather must fall back to wttr.in
	res, err := lookupWeather(context.Background(), "Jakarta")
	if err != nil {
		t.Fatalf("expected fallback to succeed, got error: %v", err)
	}
	if !strings.Contains(res, "(wttr.in)") || !strings.Contains(res, "28 C") {
		t.Errorf("output missing expected wttr fallback content:\n%s", res)
	}
}

func TestLookupExchangeRate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"amount":1,"base":"USD","date":"2026-10-01","rates":{"IDR":16000}}`)
	}))
	t.Cleanup(func() {
		server.Close()
		restoreLookupEndpoints()
	})
	lookupFXEndpoint = server.URL

	result, err := (&lookupTool{}).Run(context.Background(), &Env{}, map[string]any{
		"kind": "fx", "from": "usd", "to": "idr", "amount": 2,
	})
	if err != nil || result.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, result)
	}
	if !strings.Contains(result.Output, "2 USD = 32000 IDR") {
		t.Errorf("output = %q", result.Output)
	}
}

func TestLookupCrypto(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"bitcoin":{"usd":60000,"idr":960000000}}`)
	}))
	t.Cleanup(func() {
		server.Close()
		restoreLookupEndpoints()
	})
	lookupCryptoEndpoint = server.URL

	result, _ := (&lookupTool{}).Run(context.Background(), &Env{}, map[string]any{"kind": "crypto", "query": "bitcoin"})
	if result.IsError || !strings.Contains(result.Output, "USD 60000") {
		t.Errorf("output = %q", result.Output)
	}
}

func TestLookupWiki(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"title":"Go (programming language)","extract":"Go is a statically typed language."}`)
	}))
	t.Cleanup(func() {
		server.Close()
		restoreLookupEndpoints()
	})
	lookupWikiEndpoint = server.URL + "/"

	result, _ := (&lookupTool{}).Run(context.Background(), &Env{}, map[string]any{"kind": "wiki", "query": "Go programming language"})
	if result.IsError || !strings.Contains(result.Output, "statically typed") {
		t.Errorf("output = %q", result.Output)
	}
}

func TestLookupUnknownKind(t *testing.T) {
	result, _ := (&lookupTool{}).Run(context.Background(), &Env{}, map[string]any{"kind": "bogus"})
	if !result.IsError {
		t.Errorf("an unknown kind must fail")
	}
}

func TestLookupWeatherAcceptsLocationAlias(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if strings.Contains(request.URL.Path, "geocode") {
			fmt.Fprint(writer, `{"results":[{"name":"Bojonggede","country":"Indonesia","latitude":-6.4,"longitude":106.8}]}`)
			return
		}
		fmt.Fprint(writer, `{"current":{"temperature_2m":30.1,"apparent_temperature":33,"relative_humidity_2m":72,"precipitation":0,"weather_code":1,"wind_speed_10m":9},"daily":{"temperature_2m_max":[30],"temperature_2m_min":[24],"precipitation_probability_max":[20],"weather_code":[1]},"timezone":"Asia/Jakarta"}`)
	}))
	t.Cleanup(func() {
		server.Close()
		restoreLookupEndpoints()
	})
	lookupGeocodeEndpoint = server.URL + "/geocode"
	lookupForecastEndpoint = server.URL + "/forecast"

	result, err := (&lookupTool{}).Run(context.Background(), &Env{}, map[string]any{"kind": "weather", "location": "bojonggede"})
	if err != nil || result.IsError {
		t.Fatalf("a location alias must work like query: err=%v result=%+v", err, result)
	}
	if !strings.Contains(result.Output, "Bojonggede") {
		t.Errorf("output = %q", result.Output)
	}
}
