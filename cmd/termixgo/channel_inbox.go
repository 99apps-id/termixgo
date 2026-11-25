package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/secrets"
	"github.com/99apps-id/termixgo/internal/telegram"
)

// inboxCommand reviews the updates the Telegram durable inbox could not finish.
//
//	termixgo channel inbox list [--account NAME] [--json]
//	termixgo channel inbox drop <id>... | all [--account NAME]
//
// An update becomes "uncertain" when the service stopped while its handler was
// running. It is never run again on its own, because the handler may already
// have sent a message or changed a file; the operator decides here.
func inboxCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: termixgo channel inbox list|drop [--account NAME]")
	}
	action := strings.ToLower(strings.TrimSpace(args[0]))
	account := config.FinalAgentName
	asJSON := false
	var positionals []string
	for index := 1; index < len(args); index++ {
		switch args[index] {
		case "--account", "-a":
			if index+1 >= len(args) {
				return fmt.Errorf("--account needs a Telegram account name")
			}
			index++
			account = strings.TrimSpace(args[index])
		case "--json":
			asJSON = true
		default:
			if strings.HasPrefix(args[index], "-") {
				return fmt.Errorf("unknown inbox option %q", args[index])
			}
			positionals = append(positionals, args[index])
		}
	}
	if action != "list" && action != "drop" {
		return fmt.Errorf("unknown inbox action %q; use list or drop", action)
	}
	path, err := telegramInboxPath(account)
	if err != nil {
		return err
	}
	if action == "list" {
		return listInbox(path, account, asJSON, stdout)
	}
	return dropInbox(path, account, positionals, stdout)
}

// telegramInboxPath finds one account's inbox from its bot id. Only the id in
// front of the colon is read from the token, and it is never printed.
func telegramInboxPath(account string) (string, error) {
	store, err := secrets.Load()
	if err != nil {
		return "", err
	}
	id := telegram.BotIDFromToken(store.Get(secrets.TelegramAccountTokenKey(account)))
	if id == "" {
		return "", fmt.Errorf("no Telegram bot token is stored for account %q", account)
	}
	cursor, err := config.HomePath(telegram.CursorFileName(id, account))
	if err != nil {
		return "", err
	}
	return telegram.InboxPath(cursor), nil
}

func listInbox(path, account string, asJSON bool, stdout io.Writer) error {
	entries, err := telegram.ListInbox(path)
	if err != nil {
		return err
	}
	if asJSON {
		if entries == nil {
			entries = []telegram.InboxEntry{}
		}
		encoded, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, string(encoded))
		return nil
	}
	if len(entries) == 0 {
		fmt.Fprintf(stdout, "The Telegram inbox of %q is empty.\n", account)
		return nil
	}
	uncertain := 0
	for _, entry := range entries {
		if entry.State == "uncertain" {
			uncertain++
		}
	}
	fmt.Fprintf(stdout, "Telegram inbox of %q: %d held, %d need review.\n", account, len(entries), uncertain)
	for _, entry := range entries {
		fmt.Fprintf(stdout, "  %-6d %-9s %-8s chat %d  from %d  %s\n", entry.ID, entry.State, entry.Kind, entry.ChatID, entry.SenderID, entry.Text)
	}
	if uncertain > 0 {
		fmt.Fprintln(stdout, "An uncertain update was interrupted while it ran and is never run again on its own.")
		fmt.Fprintln(stdout, "Check the chat for what was sent, then: termixgo channel inbox drop <id>... or drop all")
	}
	return nil
}

func dropInbox(path, account string, targets []string, stdout io.Writer) error {
	if len(targets) == 0 {
		return fmt.Errorf("usage: termixgo channel inbox drop <id>... | all [--account NAME]")
	}
	var ids []int64
	if len(targets) == 1 && strings.EqualFold(targets[0], "all") {
		ids = nil
	} else {
		for _, target := range targets {
			id, err := strconv.ParseInt(target, 10, 64)
			if err != nil || id < 0 {
				return fmt.Errorf("%q is not an update id; use the numbers from 'channel inbox list', or all", target)
			}
			ids = append(ids, id)
		}
	}
	removed, err := telegram.DropInbox(path, ids)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Dropped %d interrupted update(s) from the Telegram inbox of %q.\n", removed, account)
	if len(ids) > 0 && removed < len(ids) {
		fmt.Fprintln(stdout, "Only an interrupted (uncertain) update can be dropped; a queued or running one was left alone.")
	}
	return nil
}
