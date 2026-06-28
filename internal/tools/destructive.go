package tools

import "regexp"

// destructivePatterns is a best-effort, conservative set of heuristics for shell
// commands that are irreversible or high-impact. A match triggers a confirmation
// prompt — false positives only cost an extra prompt, so we err toward catching.
// This is NOT a security boundary; it is a guardrail against the agent (possibly
// steered by injected content) running something destructive without a human nod.
var destructivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b(rm|rmdir|shred|unlink)\b`),                                         // delete
	regexp.MustCompile(`\bmv\b`),                                                              // move/overwrite
	regexp.MustCompile(`\b(dd|mkfs\w*|truncate)\b`),                                           // raw disk / truncate
	regexp.MustCompile(`(^|[^>0-9&])>([^>&]|$)`),                                              // single > file overwrite (not >>, 2>, &>)
	regexp.MustCompile(`\bchmod\b.*(-R|--recursive)`),                                         // recursive perms
	regexp.MustCompile(`\bchown\b.*(-R|--recursive)`),                                         // recursive ownership
	regexp.MustCompile(`\bsudo\b`),                                                            // privilege escalation
	regexp.MustCompile(`\b(kill|killall|pkill)\b`),                                            // process termination
	regexp.MustCompile(`\b(shutdown|reboot|halt|poweroff)\b`),                                 // system control
	regexp.MustCompile(`git\s+push`),                                                          // publish
	regexp.MustCompile(`git\s+reset\s+--hard`),                                                // discard changes
	regexp.MustCompile(`git\s+clean`),                                                         // delete untracked
	regexp.MustCompile(`git\s+branch\s+-D`),                                                   // force-delete branch
	regexp.MustCompile(`\b(apt|apt-get|yum|dnf|pacman|brew)\b.*\b(remove|purge|uninstall)\b`), // pkg removal
	regexp.MustCompile(`(curl|wget)\b.*\|\s*(sh|bash|zsh)\b`),                                 // remote pipe to shell
}

// isDestructive reports whether a shell command looks irreversible or high-impact.
func isDestructive(command string) bool {
	for _, p := range destructivePatterns {
		if p.MatchString(command) {
			return true
		}
	}
	return false
}
