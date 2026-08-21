// Package commandhelp owns discoverable metadata and deterministic help text for
// interactive slash commands. Execution remains in the frontend adapters for now;
// keeping the metadata shared prevents Telegram and the two chat REPLs from
// publishing three different command lists while the broader command-service move
// proceeds (docs/planning/flexible-orchestration.md §8.3).
package commandhelp

import (
	"fmt"
	"sort"
	"strings"
)

// Frontend identifies an interactive command adapter.
type Frontend string

const (
	Local    Frontend = "local"
	Remote   Frontend = "remote"
	Telegram Frontend = "telegram"
)

// Subcommand is one documented operation beneath a slash command.
type Subcommand struct {
	Name     string
	Summary  string
	Usage    string
	Examples []string
}

// Spec is the shared documentation for one interactive command. Order in specs is
// intentional and is preserved in help and Telegram's native command menu.
type Spec struct {
	Name        string
	Aliases     []string
	Summary     string
	Usage       string
	Details     []string
	Examples    []string
	Subcommands []Subcommand
	Frontends   []Frontend
	Menu        bool
}

// MenuCommand is the transport-neutral form registered in Telegram's native menu.
type MenuCommand struct {
	Command     string
	Description string
}

var specs = []Spec{
	{Name: "start", Summary: "Show onboarding, commands, and session state", Usage: "/start", Frontends: []Frontend{Telegram}, Menu: true},
	{Name: "help", Aliases: []string{"commands"}, Summary: "List commands or show detailed help", Usage: "/help [command [subcommand]]", Details: []string{"Use /commands as an alias for the command list. You can also use /<command> help or /<command> --help."}, Examples: []string{"/help", "/help space", "/help guidance set"}, Frontends: []Frontend{Local, Remote, Telegram}, Menu: true},
	{Name: "status", Summary: "Show engine, session, host, and state status", Usage: "/status", Details: []string{"This is read-only. In Telegram it reports engine-only status until the chat has an active session; checking status never starts one."}, Frontends: []Frontend{Local, Remote, Telegram}, Menu: true},
	{Name: "new", Aliases: []string{"reset"}, Summary: "Start a fresh conversation", Usage: "/new", Details: []string{"The current conversation is cleared or ended before the fresh one starts."}, Frontends: []Frontend{Local, Remote, Telegram}, Menu: true},
	{Name: "end", Summary: "Archive this persistent session", Usage: "/end", Details: []string{"Ends the current persistent conversation recoverably. It does not permanently delete it."}, Frontends: []Frontend{Remote, Telegram}, Menu: true},
	{Name: "purge", Summary: "Permanently delete this session", Usage: "/purge", Details: []string{"This is irreversible. Telegram asks for confirmation before deleting."}, Frontends: []Frontend{Remote, Telegram}, Menu: true},
	{Name: "model", Summary: "Show or switch the session model", Usage: "/model [<id>|-]", Details: []string{"With no argument, shows the current model. Use - to return to the configured default. A change applies to the next turn."}, Examples: []string{"/model", "/model gpt-5.1", "/model -"}, Frontends: []Frontend{Local, Remote, Telegram}, Menu: true},
	{Name: "tier", Summary: "Show or switch the trust tier", Usage: "/tier [safe|balanced|permissive]", Details: []string{"A remote request is clamped to the engine's configured ceiling."}, Frontends: []Frontend{Local, Remote}, Menu: true},
	{Name: "space", Summary: "Show, list, switch, or clear the data context", Usage: "/space [list|<name-or-id>|-]", Details: []string{"A space scopes memory and standing guidance; it does not change the working directory. Listing spaces does not create a session. A switch applies to the next turn."}, Examples: []string{"/space", "/space list", "/space Polish lessons", "/space -"}, Subcommands: []Subcommand{{Name: "list", Summary: "List available spaces; * marks the active one", Usage: "/space list"}}, Frontends: []Frontend{Local, Remote, Telegram}, Menu: true},
	{Name: "guidance", Summary: "Manage standing instructions", Usage: "/guidance <global|space|session> <show|set|add|clear> [text]", Details: []string{"Global guidance applies across the workspace; space guidance applies in the active space; session guidance applies only to this persistent conversation. Changes take effect on the next turn."}, Examples: []string{"/guidance global show", "/guidance space set Answer in Polish", "/guidance session clear"}, Subcommands: []Subcommand{{Name: "show", Summary: "Show the selected scope", Usage: "/guidance <scope> show"}, {Name: "set", Summary: "Replace the selected scope", Usage: "/guidance <scope> set <text>"}, {Name: "add", Summary: "Append a new line to the selected scope", Usage: "/guidance <scope> add <text>"}, {Name: "clear", Summary: "Clear the selected scope", Usage: "/guidance <scope> clear"}}, Frontends: []Frontend{Local, Remote, Telegram}, Menu: true},
	{Name: "reload", Summary: "Reload prompts, agent types, and config defaults", Usage: "/reload", Details: []string{"A malformed update leaves the running configuration unchanged. Successful changes apply to subsequent turns."}, Frontends: []Frontend{Local, Remote, Telegram}, Menu: true},
	{Name: "compact", Summary: "Summarize older conversation context", Usage: "/compact", Details: []string{"Shrinks live context while keeping the on-disk transcript."}, Frontends: []Frontend{Local}},
	{Name: "verbose", Summary: "Show, enable, or disable the live trace", Usage: "/verbose [on|off]", Frontends: []Frontend{Local}},
	{Name: "attach", Summary: "Attach a local file to planned chat", Usage: "/attach <path>", Details: []string{"Available only when planned chat has an artifact manifest."}, Frontends: []Frontend{Local}},
	{Name: "exit", Aliases: []string{"quit"}, Summary: "Leave the chat REPL", Usage: "/exit", Details: []string{"Remote chat detaches without closing the persistent session."}, Frontends: []Frontend{Local, Remote}},
}

// Specs returns a copy of the visible command descriptors in stable display order.
func Specs(frontend Frontend) []Spec {
	out := make([]Spec, 0, len(specs))
	for _, spec := range specs {
		if available(spec, frontend) {
			out = append(out, cloneSpec(spec))
		}
	}
	return out
}

func cloneSpec(spec Spec) Spec {
	cloned := spec
	cloned.Aliases = append([]string(nil), spec.Aliases...)
	cloned.Details = append([]string(nil), spec.Details...)
	cloned.Examples = append([]string(nil), spec.Examples...)
	cloned.Frontends = append([]Frontend(nil), spec.Frontends...)
	cloned.Subcommands = append([]Subcommand(nil), spec.Subcommands...)
	for i := range cloned.Subcommands {
		cloned.Subcommands[i].Examples = append([]string(nil), spec.Subcommands[i].Examples...)
	}
	return cloned
}

// List renders the compact in-band command list.
func List(frontend Frontend) string {
	var b strings.Builder
	b.WriteString("Available commands:\n")
	for _, spec := range Specs(frontend) {
		fmt.Fprintf(&b, "/%-10s %s\n", spec.Name, spec.Summary)
	}
	b.WriteString("Use /help <command> for details.")
	return b.String()
}

// Help renders one command or subcommand. query accepts an optional leading slash.
func Help(frontend Frontend, query string) string {
	fields := strings.Fields(strings.TrimSpace(query))
	if len(fields) == 0 {
		return List(frontend)
	}
	if len(fields) > 2 {
		return "usage: /help [command [subcommand]]"
	}
	name := normalizeName(fields[0])
	spec, ok := find(frontend, name)
	if !ok {
		return fmt.Sprintf("unknown command: /%s\nUse /help to list available commands.", name)
	}
	if len(fields) > 1 {
		subName := normalizeName(fields[1])
		for _, sub := range spec.Subcommands {
			if sub.Name == subName {
				return renderSubcommand(spec, sub)
			}
		}
		return fmt.Sprintf("unknown subcommand: /%s %s\nUse /help %s for details.", spec.Name, subName, spec.Name)
	}
	return renderSpec(spec)
}

// ForLine recognizes the read-only help grammar before a frontend's command
// handlers. It never executes a command.
func ForLine(frontend Frontend, line string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", false
	}
	name := normalizeName(fields[0])
	if name == "help" || name == "commands" {
		return Help(frontend, strings.Join(fields[1:], " ")), true
	}
	if len(fields) >= 2 && (strings.EqualFold(fields[len(fields)-1], "help") || strings.EqualFold(fields[len(fields)-1], "--help")) {
		query := append([]string{name}, fields[1:len(fields)-1]...)
		return Help(frontend, strings.Join(query, " ")), true
	}
	return "", false
}

// Menu returns Telegram-native top-level entries from the same visible registry.
func Menu(frontend Frontend) []MenuCommand {
	var out []MenuCommand
	for _, spec := range Specs(frontend) {
		if spec.Menu {
			out = append(out, MenuCommand{Command: spec.Name, Description: spec.Summary})
		}
	}
	return out
}

// FormatSpaces renders body-redacted space metadata consistently across frontends.
func FormatSpaces(spaces []Space, activeID string) string {
	if len(spaces) == 0 {
		return "no spaces yet — create one with: agent space create <name>, or ask the agent to create one"
	}
	var b strings.Builder
	for _, sp := range spaces {
		marker := "  "
		if sp.ID == activeID {
			marker = "* "
		}
		fmt.Fprintf(&b, "%s%s (%q)\n", marker, sp.ID, sp.Name)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Space is the body-redacted subset needed by /space list.
type Space struct{ ID, Name string }

func find(frontend Frontend, name string) (Spec, bool) {
	for _, spec := range specs {
		if !available(spec, frontend) {
			continue
		}
		if spec.Name == name {
			return spec, true
		}
		for _, alias := range spec.Aliases {
			if alias == name {
				return spec, true
			}
		}
	}
	return Spec{}, false
}

func available(spec Spec, frontend Frontend) bool {
	for _, candidate := range spec.Frontends {
		if candidate == frontend {
			return true
		}
	}
	return false
}

func normalizeName(name string) string {
	name = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), "/")
	return name
}

func renderSpec(spec Spec) string {
	var b strings.Builder
	b.WriteString(spec.Usage)
	b.WriteByte('\n')
	b.WriteString(spec.Summary)
	if len(spec.Aliases) > 0 {
		fmt.Fprintf(&b, "\nAliases: /%s", strings.Join(spec.Aliases, ", /"))
	}
	for _, detail := range spec.Details {
		b.WriteString("\n\n")
		b.WriteString(detail)
	}
	if len(spec.Subcommands) > 0 {
		b.WriteString("\n\nSubcommands:\n")
		for _, sub := range spec.Subcommands {
			fmt.Fprintf(&b, "  %-10s %s\n", sub.Name, sub.Summary)
		}
		b.WriteString("Use /help ")
		b.WriteString(spec.Name)
		b.WriteString(" <subcommand> for details.")
	}
	if len(spec.Examples) > 0 {
		b.WriteString("\n\nExamples:\n  ")
		b.WriteString(strings.Join(spec.Examples, "\n  "))
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderSubcommand(spec Spec, sub Subcommand) string {
	var b strings.Builder
	b.WriteString(sub.Usage)
	b.WriteByte('\n')
	b.WriteString(sub.Summary)
	if len(sub.Examples) > 0 {
		b.WriteString("\n\nExamples:\n  ")
		b.WriteString(strings.Join(sub.Examples, "\n  "))
	}
	return b.String()
}

// Validate checks the static registry. It is exported for tests and future
// registry assembly from optional command modules.
func Validate() error {
	seen := map[string]string{}
	for _, spec := range specs {
		if spec.Name == "" || spec.Summary == "" || spec.Usage == "" {
			return fmt.Errorf("command %q is missing name, summary, or usage", spec.Name)
		}
		if len(spec.Frontends) == 0 {
			return fmt.Errorf("command %q has no frontend", spec.Name)
		}
		if spec.Menu && available(spec, Telegram) {
			if !validTelegramMenuName(spec.Name) {
				return fmt.Errorf("command %q is not valid in Telegram's native menu", spec.Name)
			}
			if len(spec.Summary) < 3 || len(spec.Summary) > 256 {
				return fmt.Errorf("command %q has an invalid Telegram menu description length", spec.Name)
			}
		}
		for _, name := range append([]string{spec.Name}, spec.Aliases...) {
			name = normalizeName(name)
			if owner, ok := seen[name]; ok {
				return fmt.Errorf("command name or alias %q is shared by %s and %s", name, owner, spec.Name)
			}
			seen[name] = spec.Name
		}
		subs := map[string]bool{}
		for _, sub := range spec.Subcommands {
			if sub.Name == "" || sub.Summary == "" || sub.Usage == "" {
				return fmt.Errorf("command %q has incomplete subcommand metadata", spec.Name)
			}
			if subs[sub.Name] {
				return fmt.Errorf("command %q repeats subcommand %q", spec.Name, sub.Name)
			}
			subs[sub.Name] = true
		}
	}
	return nil
}

func validTelegramMenuName(name string) bool {
	if len(name) < 1 || len(name) > 32 {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

// Names is a test/debug helper that returns visible canonical names sorted.
func Names(frontend Frontend) []string {
	var out []string
	for _, spec := range Specs(frontend) {
		out = append(out, spec.Name)
	}
	sort.Strings(out)
	return out
}
