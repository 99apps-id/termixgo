package main

import (
	"fmt"
	"io"
	"strings"
)

// completionShells is the accepted set for `termixgo completion`.
var completionShells = []string{"bash", "zsh", "fish", "powershell"}

func runCompletion(args []string, stdout io.Writer) error {
	shell := "bash"
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		shell = strings.ToLower(strings.TrimSpace(args[0]))
	}
	script, ok := completionScript(shell)
	if !ok {
		return fmt.Errorf("unknown shell %q; use one of %s", shell, strings.Join(completionShells, ", "))
	}
	_, err := io.WriteString(stdout, script)
	return err
}

func completionScript(shell string) (string, bool) {
	const commands = "setup run models model trust approval harness mcp secret telegram serve service doctor completion version help"
	switch shell {
	case "bash":
		return fmt.Sprintf(`# termixgo completion for bash. Install with:
#   termixgo completion bash >> ~/.bash_completion
_termixgo() {
  local cur cmds
  cmds="%s"
  cur="${COMP_WORDS[COMP_CWORD]}"
  if [ "$COMP_CWORD" -eq 1 ]; then
    COMPREPLY=($(compgen -W "$cmds" -- "$cur"))
    return 0
  fi
  case "${COMP_WORDS[1]}" in
    approval) COMPREPLY=($(compgen -W "ask edits all plan" -- "$cur")) ;;
    trust) COMPREPLY=($(compgen -W "on off" -- "$cur")) ;;
    completion) COMPREPLY=($(compgen -W "bash zsh fish powershell" -- "$cur")) ;;
    service) COMPREPLY=($(compgen -W "install uninstall status" -- "$cur")) ;;
  esac
}
complete -F _termixgo termixgo
`, commands), true
	case "zsh":
		return fmt.Sprintf(`#compdef termixgo
# termixgo completion for zsh. Install with:
#   termixgo completion zsh > ~/.zsh/completions/_termixgo
_termixgo() {
  local -a cmds
  cmds=(%s)
  if (( CURRENT == 2 )); then
    _describe 'command' cmds
    return
  fi
  case "${words[2]}" in
    approval) _describe 'mode' '(ask edits all plan)' ;;
    trust) _describe 'trust' '(on off)' ;;
    completion) _describe 'shell' '(bash zsh fish powershell)' ;;
    service) _describe 'action' '(install uninstall status)' ;;
  esac
}
_termixgo
`, strings.ReplaceAll(commands, " ", " ")), true
	case "fish":
		return `# termixgo completion for fish. Install with:
#   termixgo completion fish > ~/.config/fish/completions/termixgo.fish
set -l cmds setup run models model trust approval harness mcp secret telegram serve service doctor completion version help
complete -c termixgo -f -n '__fish_use_subcommand' -a "$cmds"
complete -c termixgo -f -n '__fish_seen_subcommand_from approval' -a "ask edits all plan"
complete -c termixgo -f -n '__fish_seen_subcommand_from trust' -a "on off"
complete -c termixgo -f -n '__fish_seen_subcommand_from completion' -a "bash zsh fish powershell"
complete -c termixgo -f -n '__fish_seen_subcommand_from service' -a "install uninstall status"
`, true
	case "powershell":
		return `# termixgo completion for PowerShell. Install with:
#   termixgo completion powershell | Out-String | Invoke-Expression
Register-ArgumentCompleter -Native -CommandName termixgo -ScriptBlock {
  param($wordToComplete, $commandAst, $cursorPosition)
  $cmds = @('setup','run','models','model','trust','approval','harness','mcp','secret','telegram','serve','service','doctor','completion','version','help')
  $modes = @{
    'approval'   = @('ask','edits','all','plan')
    'trust'      = @('on','off')
    'completion' = @('bash','zsh','fish','powershell')
    'service'    = @('install','uninstall','status')
  }
  $elements = $commandAst.CommandElements
  if ($elements.Count -eq 2) {
    $cmds | Where-Object { $_ -like "$wordToComplete*" } | ForEach-Object {
      [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterName', $_)
    }
    return
  }
  $sub = "$($elements[1])"
  if ($modes.ContainsKey($sub)) {
    $modes[$sub] | Where-Object { $_ -like "$wordToComplete*" } | ForEach-Object {
      [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterName', $_)
    }
  }
}
`, true
	}
	return "", false
}
