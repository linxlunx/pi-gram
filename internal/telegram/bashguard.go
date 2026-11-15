package telegram

import (
	"fmt"
	"regexp"
	"strings"
)

// Guard against obviously destructive shell commands submitted through
// /bash. This is a coarse, defense-in-depth filter: real enforcement
// happens through Linux permissions and restricted sudo on the server.
// It deliberately blocks entire command families (privilege
// escalation, firewall/SSH/user/sudoer changes, disk destruction)
// rather than trying to judge individual cases.
type dangerousRule struct {
	re     *regexp.Regexp
	reason string
}

var dangerousRules = []dangerousRule{
	{
		// Any use of sudo: privilege escalation must be limited to
		// explicitly authorized scripts, never free-form RPC bash.
		re:     regexp.MustCompile(`(?i)(^|[;&|]\s*|\bsudo\s+)sudo\b|\bsudo\s+-`),
		reason: "sudo is not permitted through /bash",
	},
	{
		re:     regexp.MustCompile(`(?i)\b(shutdown|reboot|halt|poweroff)\b|\binit\s+[06]\b`),
		reason: "shutting down or rebooting the server",
	},
	{
		re:     regexp.MustCompile(`(?i)\bmkfs(\.[a-z0-9]+)?\b|\bwipefs\b|\bfdisk\b|\bsfdisk\b|\bsgdisk\b|\bparted\b|\bpartprobe\b`),
		reason: "filesystem or partition modification",
	},
	{
		re:     regexp.MustCompile(`(?i)\bdd\b[^;&|]*\bof=/dev/`),
		reason: "raw disk writes (dd of=/dev/...)",
	},
	{
		re:     regexp.MustCompile(`(?i)\b(iptables|ip6tables|nft|nftables|ufw|firewall-cmd)\b`),
		reason: "firewall modification is not allowed",
	},
	{
		re:     regexp.MustCompile(`(?i)\bsshd\b|/etc/ssh\b|authorized_keys`),
		reason: "SSH configuration modification is not allowed",
	},
	{
		re:     regexp.MustCompile(`(?i)\b(useradd|userdel|usermod|adduser|deluser|groupadd|groupdel|groupmod|passwd|chpasswd|visudo)\b|/etc/sudoers`),
		reason: "user account or sudoer modification is not allowed",
	},
	{
		re:     regexp.MustCompile(`(?i):\(\)\s*\{.*\}\s*;?\s*:`),
		reason: "fork bomb",
	},
	{
		re:     regexp.MustCompile(`>\s*/dev/(sd[a-z]|nvme[0-9]|hd[a-z]|vd[a-z])`),
		reason: "raw device write",
	},
	{
		re:     regexp.MustCompile(`(?i)\bchmod\s+-R\s+[0-7]{3,4}\s+/(\s|$)`),
		reason: "recursive chmod on /",
	},
	{
		re:     regexp.MustCompile(`(?i)\bchown\s+(-R\b[^;&|]*\s)+/(\s|$)`),
		reason: "recursive chown on /",
	},
}

var rmRecursive = regexp.MustCompile(`(?i)\brm\s+-[a-su-z]*[rf]`)

// CheckBashCommand inspects a shell command and returns a refusal
// reason when it matches a dangerous pattern.
func CheckBashCommand(cmd string) (string, bool) {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" {
		return "empty command", true
	}
	for _, rule := range dangerousRules {
		if rule.re.MatchString(trimmed) {
			return rule.reason, true
		}
	}
	if reason := checkRm(trimmed); reason != "" {
		return reason, true
	}
	return "", false
}

// checkRm refuses rm -r/-rf (and long forms) aimed at absolute paths,
// home directories, or wildcards. Recursive rm of relative paths is
// allowed.
func checkRm(cmd string) string {
	if !rmRecursive.MatchString(cmd) {
		return ""
	}
	// Look at each rm invocation's arguments.
	parts := strings.FieldsFunc(cmd, func(r rune) bool {
		return r == ';' || r == '|' || r == '&' || r == '\n'
	})
	for _, part := range parts {
		fields := strings.Fields(strings.TrimSpace(part))
		if len(fields) == 0 || fields[0] != "rm" && fields[0] != "/bin/rm" && fields[0] != "/usr/bin/rm" {
			continue
		}
		recursive := false
		var targets []string
		for _, f := range fields[1:] {
			if strings.HasPrefix(f, "-") {
				if strings.ContainsAny(f[1:], "rR") {
					recursive = true
				}
				continue
			}
			targets = append(targets, f)
		}
		if !recursive {
			continue
		}
		for _, t := range targets {
			if strings.HasPrefix(t, "/") || strings.HasPrefix(t, "~") ||
				strings.HasPrefix(t, "$") || strings.HasPrefix(t, "*") {
				return fmt.Sprintf("recursive rm of %q", t)
			}
		}
	}
	return ""
}
