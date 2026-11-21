package app

import (
	"regexp"
	"strconv"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
)

// An empty session's gauge counts the schemas a request carries, not the
// whole catalogue: with tool search on that is the always-on subset.
func TestContextGaugeCountsOnlyAdvertisedTools(t *testing.T) {
	application := newTestApp(t)
	if !shouldDeferTools(len(application.tools.Tools())) {
		t.Skip("the registry is under the defer threshold")
	}
	full := agent.EstimateToolSchemas(application.tools.Definitions())
	advertised := agent.EstimateToolSchemas(agent.AdvertisedTools(application.tools, true, nil).Definitions())
	if advertised >= full {
		t.Fatalf("the advertised subset (%d) should be smaller than the catalogue (%d)", advertised, full)
	}

	gauge := application.ContextUsage()
	match := regexp.MustCompile(`ctx: ~([0-9.]+)k`).FindStringSubmatch(gauge)
	if match == nil {
		t.Fatalf("gauge = %q", gauge)
	}
	shown, _ := strconv.ParseFloat(match[1], 64)
	system := agent.EstimateTokens(agent.BuildSystem(application.env(), application.CurrentModel().ID))
	if int(shown*1000) >= system+full-500 {
		t.Fatalf("the gauge (%s) still counts the whole catalogue (%d)", gauge, system+full)
	}
}

func TestAdvertisedToolsFollowsDiscovery(t *testing.T) {
	registry := agent.DefaultRegistry()
	without := len(agent.AdvertisedTools(registry, true, nil).Tools())
	with := len(agent.AdvertisedTools(registry, true, []string{"generate_image"}).Tools())
	if with != without+1 {
		t.Fatalf("a discovered tool should be advertised: %d then %d", without, with)
	}
	if len(agent.AdvertisedTools(registry, false, nil).Tools()) != len(registry.Tools()) {
		t.Fatalf("without tool search every tool is advertised")
	}
}
