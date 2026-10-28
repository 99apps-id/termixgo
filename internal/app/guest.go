package app

import (
	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
)

// guestDeniedTools is the admin-only class: running commands and background
// processes, spawning workers or subagents, mutating git or checkpoints,
// outbound requests and browser sessions, GitHub actions, environment and
// secret lookups, and memory or skill mutation.
//
// A guest keeps the read tools and the write tools that still pass an operator
// approval. The rule is enforced here, in the registry the model is offered,
// not only in the prompt: a tool the model cannot see is a tool it cannot ask
// for.
var guestDeniedTools = []string{
	"run_command", "run_checks", "run_background", "logs", "wait",
	"list_processes", "kill",
	"orchestrate", "list_pipelines", "code_worker", "parallel_batch", "subagent",
	"git_add", "git_commit", "git_push", "git_pull", "git_branch", "git_restore",
	"git_worktree", "git_stash", "git_stash_pop",
	"checkpoint", "rewind",
	"env_get",
	"http_request", "probe_url", "dns_recon", "port_probe", "tls_inspect", "http_headers_audit",
	"browser_open", "browser_screenshot", "browser_dump_dom",
	"browser_session_open", "browser_session_read", "browser_session_close",
	"github_create_pr", "github_get_pr", "github_list_prs", "github_review_pr",
	"github_comment_pr", "github_merge_pr",
	"ask_user", "remember", "install_skill", "propose_skill",
	"send_channel",
}

// denyList is the effective tool deny-list of one run: the agent's own list,
// plus the admin-only class for a guest. A subagent inherits it, so a nested
// run cannot reach a tool its parent was denied.
func (a *App) denyList(agentName string, guest bool) []string {
	deny := append([]string(nil), a.Config().Agent(agentName).Tools.Deny...)
	if guest {
		deny = append(deny, guestDeniedTools...)
	}
	return deny
}

// toolsFor returns the tool registry one run is offered. MCP tools are
// excluded for guests as a group, because an MCP server is a process this
// program did not write and its tools are arbitrary.
func (a *App) toolsFor(agentName string, guest bool) *agent.Registry {
	registry := a.Tools()
	if guest {
		registry = registry.WithoutPrefix("mcp_")
	}
	if deny := a.denyList(agentName, guest); len(deny) > 0 {
		registry = registry.Without(deny...)
	}
	return registry
}

// policyFor returns the approval policy one run uses. A guest always asks, and
// never inherits the operator's tool-level "always allow" answers: those are
// the operator's consent, not a stranger's.
func (a *App) policyFor(agentName string, guest bool) *agent.ApprovalPolicy {
	base := a.Policy()
	if guest {
		return &agent.ApprovalPolicy{
			Mode:           agent.ApprovalAsk,
			AlwaysAllowed:  map[string]bool{},
			SessionAllowed: map[string]bool{},
			Memory:         base.Memory,
		}
	}
	mode := agent.ApprovalMode(a.Config().AgentApprovalMode(agentName))
	if mode == base.Mode {
		return base
	}
	clone := *base
	clone.Mode = mode
	return &clone
}

// envForRun builds the run environment: the agent's workspace and persona,
// with a guest's secrets and inherited grants removed.
func (a *App) envForRun(agentName string, guest bool) *agent.Env {
	runEnv := a.envFor(agentName)
	runEnv.Grounding = a.Config().AgentGrounding(agentName)
	runEnv.ToolDeny = a.denyList(agentName, guest)
	runEnv.DelegationMaxChildren, runEnv.DelegationMaxDepth = a.Config().DelegationLimits(agentName)
	if guest {
		runEnv.Guest = true
		// A guest gets the strict rule regardless of the agent's setting: an
		// unsupported answer in a public chat is a promise nobody verified.
		runEnv.Grounding = config.GroundingStrict
		runEnv.Secrets = nil
		runEnv.SendChannel = nil
		runEnv.AlwaysAllowed = map[string]bool{}
		runEnv.SessionAllowed = map[string]bool{}
	}
	return runEnv
}

// isolatedEnvForRun is the environment of a run that owns its session: a
// throwaway todo store and no folding into the operator's live totals.
func (a *App) isolatedEnvForRun(agentName string, guest bool) *agent.Env {
	runEnv := a.envForRun(agentName, guest)
	runEnv.Todos = agent.NewTodoStore()
	runEnv.Emit = func(event agent.Event) {
		a.emitInto(event, false)
	}
	return runEnv
}
