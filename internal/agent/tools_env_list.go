package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// envListTool lists all environment variables, masking sensitive ones.
type envListTool struct{}

func (t *envListTool) Name() string      { return "env_list" }
func (t *envListTool) Aliases() []string { return []string{"list_env", "env"} }
func (t *envListTool) Mutating() bool    { return false }
func (t *envListTool) Risk() Risk        { return RiskCommand }
func (t *envListTool) Description() string {
	return "List all environment variables. Sensitive values (keys, secrets, tokens, passwords) are masked."
}
func (t *envListTool) Schema() map[string]any {
	return object(map[string]any{
		"prefix": strProp("Optional filter prefix, e.g. PATH or GOPATH."),
	})
}
func (t *envListTool) Label(a map[string]any) string {
	return "Listing environment"
}
func (t *envListTool) DoneLabel(a map[string]any) string {
	return "Listed environment"
}

func (t *envListTool) Run(_ context.Context, env *Env, args map[string]any) (Result, error) {
	prefix := strings.ToUpper(strings.TrimSpace(argString(args, "prefix")))
	var b strings.Builder
	for _, e := range os.Environ() {
		if prefix != "" && !strings.HasPrefix(strings.ToUpper(e), prefix) {
			continue
		}
		parts := strings.SplitN(e, "=", 2)
		if len(parts) != 2 {
			continue
		}
		name, val := parts[0], parts[1]
		if isSensitiveEnvName(name) || isStoredSecret(env, val) {
			val = "[REDACTED]"
		}
		fmt.Fprintf(&b, "%s=%s\n", name, val)
	}
	return Result{Output: b.String()}, nil
}
