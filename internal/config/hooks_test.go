package config

import "testing"

func TestNormaliseHooksKeepsOnlyUsableOnes(t *testing.T) {
	got := normaliseHooks([]Hook{
		{Event: " PRE_TOOL ", Matcher: " ^edit$ ", Command: " gofmt -l . "},
		{Event: "post_tool", Command: "echo hi", TimeoutSecs: 9999},
		{Event: "pre_tool", Command: ""},
		{Event: "on_save", Command: "echo nope"},
		{Event: "pre_tool", Matcher: "([unclosed", Command: "echo bad"},
	})

	if len(got) != 2 {
		t.Fatalf("hooks = %+v, want the 2 usable ones", got)
	}
	first := got[0]
	if first.Event != HookPreTool || first.Matcher != "^edit$" || first.Command != "gofmt -l ." {
		t.Errorf("first hook = %+v, want trimmed and lowercased fields", first)
	}
	if first.TimeoutSecs != DefaultHookTimeoutSecs {
		t.Errorf("timeout = %d, want the default %d", first.TimeoutSecs, DefaultHookTimeoutSecs)
	}
	if got[1].TimeoutSecs != MaxHookTimeoutSecs {
		t.Errorf("timeout = %d, want it capped at %d", got[1].TimeoutSecs, MaxHookTimeoutSecs)
	}
}

func TestNormaliseHooksOfNothingIsNil(t *testing.T) {
	if got := normaliseHooks([]Hook{{Event: "bogus", Command: "x"}}); got != nil {
		t.Errorf("hooks = %+v, want nil so the field stays omitted", got)
	}
}

func TestHooksSurviveARoundTrip(t *testing.T) {
	t.Setenv(EnvHome, t.TempDir())
	cfg := Default()
	cfg.Hooks = []Hook{{Event: HookPostTool, Matcher: "^(edit|write_file)$", Command: "gofmt -w .", TimeoutSecs: 20}}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Hooks) != 1 || loaded.Hooks[0] != cfg.Hooks[0] {
		t.Errorf("hooks = %+v, want %+v", loaded.Hooks, cfg.Hooks)
	}
}
