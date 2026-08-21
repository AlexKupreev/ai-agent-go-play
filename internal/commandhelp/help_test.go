package commandhelp

import (
	"slices"
	"strings"
	"testing"
)

func TestRegistryValidAndFrontendFiltered(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatal(err)
	}
	telegram := Names(Telegram)
	if !slices.Contains(telegram, "start") || !slices.Contains(telegram, "status") || slices.Contains(telegram, "tier") || slices.Contains(telegram, "exit") {
		t.Fatalf("Telegram commands = %v", telegram)
	}
	local := Names(Local)
	if !slices.Contains(local, "compact") || !slices.Contains(local, "status") || slices.Contains(local, "purge") || slices.Contains(local, "start") {
		t.Fatalf("local commands = %v", local)
	}
}

func TestHelpListDetailSubcommandAndAliases(t *testing.T) {
	list := List(Telegram)
	for _, want := range []string{"Available commands:", "/help", "/space", "Use /help <command>"} {
		if !strings.Contains(list, want) {
			t.Fatalf("list missing %q:\n%s", want, list)
		}
	}
	detail := Help(Telegram, "/space")
	if !strings.Contains(detail, "/space [list|") || !strings.Contains(detail, "Subcommands:") {
		t.Fatalf("space detail = %q", detail)
	}
	sub := Help(Telegram, "space list")
	if !strings.HasPrefix(sub, "/space list\n") {
		t.Fatalf("space-list help = %q", sub)
	}
	if got := Help(Telegram, "commands"); !strings.HasPrefix(got, "/help ") {
		t.Fatalf("alias help = %q", got)
	}
	if got := Help(Telegram, "tier"); !strings.Contains(got, "unknown command") {
		t.Fatalf("unavailable command help = %q", got)
	}
	if got := Help(Telegram, "guidance set extra"); !strings.HasPrefix(got, "usage: /help") {
		t.Fatalf("too many help topics = %q", got)
	}
}

func TestForLineRecognizesOnlyExplicitHelp(t *testing.T) {
	for _, line := range []string{"/help", "/commands", "/help /space", "/space HELP", "/guidance set --help"} {
		if out, ok := ForLine(Telegram, line); !ok || out == "" {
			t.Errorf("ForLine(%q) = %q, %v", line, out, ok)
		}
	}
	for _, line := range []string{"hello", "/space list", "/model"} {
		if _, ok := ForLine(Telegram, line); ok {
			t.Errorf("ForLine(%q) intercepted execution", line)
		}
	}
}

func TestMenuUsesCanonicalImplementedCommands(t *testing.T) {
	menu := Menu(Telegram)
	if len(menu) == 0 || menu[0].Command != "start" {
		t.Fatalf("menu = %+v", menu)
	}
	foundStatus := false
	for _, item := range menu {
		if item.Command == "commands" || item.Command == "reset" || item.Description == "" {
			t.Fatalf("bad menu item: %+v", item)
		}
		foundStatus = foundStatus || item.Command == "status"
	}
	if !foundStatus {
		t.Fatalf("status missing from Telegram menu: %+v", menu)
	}
}

func TestFormatSpaces(t *testing.T) {
	if got := FormatSpaces(nil, ""); !strings.Contains(got, "no spaces yet") {
		t.Fatalf("empty = %q", got)
	}
	got := FormatSpaces([]Space{{ID: "polish", Name: "Polish lessons"}, {ID: "tax", Name: "Tax"}}, "tax")
	if !strings.Contains(got, `  polish ("Polish lessons")`) || !strings.Contains(got, `* tax ("Tax")`) {
		t.Fatalf("spaces = %q", got)
	}
}
