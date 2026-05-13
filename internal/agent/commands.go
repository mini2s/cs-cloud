package agent

import (
	"strings"
)

// SlashCommand represents a slash command that can be exposed to cloud UI or TUI.
type SlashCommand struct {
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases,omitempty"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Scope       string   `json:"scope,omitempty"`
	Category    string   `json:"category,omitempty"`
	Keybind     string   `json:"keybind,omitempty"`
	Source      string   `json:"source,omitempty"`
	Agent       string   `json:"agent,omitempty"`
	Model       string   `json:"model,omitempty"`
	Subtask     bool     `json:"subtask,omitempty"`
	Hints       []string `json:"hints,omitempty"`
}

const (
	ScopeShared    = "shared"
	ScopeTuiOnly   = "tui-only"
	ScopeCloudOnly = "cloud-only"
	ScopePrompt    = "prompt"
)

// BuiltinCommands is the source-of-truth list for TUI/Web UI commands.
// These are maintained in cs-cloud and merged with opencode prompt commands
// at runtime via BuildManifest.
var BuiltinCommands = []SlashCommand{
	{Name: "favorites", Aliases: []string{"fav"}, Title: "Manage favorites", Description: "Manage favorite skills", Scope: ScopeShared, Category: "skill"},
}

// BuildManifest merges builtin UI commands with agent prompt commands,
// then filters by the requested scopes.
func BuildManifest(includeScopes []string, agentCmds []SlashCommand) ([]SlashCommand, error) {
	scopeSet := make(map[string]struct{})
	for _, s := range includeScopes {
		scopeSet[s] = struct{}{}
	}

	result := make([]SlashCommand, 0, len(BuiltinCommands)+len(agentCmds))

	for _, c := range BuiltinCommands {
		if _, ok := scopeSet[c.Scope]; ok {
			result = append(result, c)
		}
	}

	for _, c := range agentCmds {
		// agent commands are always prompt scope
		if _, ok := scopeSet[ScopePrompt]; !ok {
			continue
		}
		// ensure prompt commands carry the prompt scope
		c.Scope = ScopePrompt
		result = append(result, c)
	}

	// deduplicate by name, first occurrence wins
	seen := make(map[string]struct{})
	deduped := make([]SlashCommand, 0, len(result))
	for _, c := range result {
		if _, ok := seen[c.Name]; ok {
			continue
		}
		seen[c.Name] = struct{}{}
		// filter aliases that conflict with already-seen names
		cleanAliases := make([]string, 0, len(c.Aliases))
		for _, a := range c.Aliases {
			if _, ok := seen[a]; !ok {
				cleanAliases = append(cleanAliases, a)
				seen[a] = struct{}{}
			}
		}
		c.Aliases = cleanAliases
		deduped = append(deduped, c)
	}

	return deduped, nil
}

// ParseIncludeScopes parses the ?include= query parameter.
// Defaults to [shared, prompt, cloud-only] when empty.
func ParseIncludeScopes(q string) []string {
	if q == "" {
		return []string{ScopeShared, ScopePrompt, ScopeCloudOnly}
	}
	parts := strings.Split(q, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
