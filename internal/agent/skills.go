package agent

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Skill represents a loaded skill parsed from a SKILL.md file.
type Skill struct {
	Name         string
	Description  string
	AllowedTools []string // nil means all tools
	Model        string   // empty means inherit
	Body         string   // raw markdown body (template)
	Source       string   // "project" or "user"
	Dir          string   // absolute path to the skill directory (contains SKILL.md)
}

// SkillRegistry holds all loaded skills keyed by name.
type SkillRegistry struct {
	skills map[string]*Skill
}

// NewSkillRegistry creates an empty registry.
func NewSkillRegistry() *SkillRegistry {
	return &SkillRegistry{skills: make(map[string]*Skill)}
}

// LoadSkills scans the given directories for skill definitions.
// Each subdirectory containing a SKILL.md file is parsed as a skill.
// Directories earlier in the list take precedence on name collisions.
// Callers should pass directories in precedence order (highest first).
func LoadSkills(dirs ...string) *SkillRegistry {
	reg := NewSkillRegistry()
	for i, dir := range dirs {
		source := "user"
		if i == 0 {
			source = "project"
		}
		reg.loadFrom(dir, source)
	}
	return reg
}

// LoadSkillsLayered loads skills with explicit project/user separation.
// projectDirs (highest precedence first) are tagged "project", userDirs
// following are tagged "user". Within each group, earlier entries win on
// name collision. Use this when more than two skill dirs exist (e.g. custom
// skills_dir entries) so real project skills aren't mislabeled "user" once a
// custom dir takes index 0.
func LoadSkillsLayered(projectDirs, userDirs []string) *SkillRegistry {
	reg := NewSkillRegistry()
	for _, dir := range projectDirs {
		reg.loadFrom(dir, "project")
	}
	for _, dir := range userDirs {
		reg.loadFrom(dir, "user")
	}
	return reg
}

func (r *SkillRegistry) loadFrom(dir, source string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillDir := filepath.Join(dir, entry.Name())
		skillFile := filepath.Join(skillDir, "SKILL.md")
		if _, err := os.Stat(skillFile); err != nil {
			continue
		}

		skill, err := parseSkillFile(skillFile)
		if err != nil {
			log.Printf("[skills] failed to parse %s: %v", skillFile, err)
			continue
		}

		if skill.Name == "" {
			skill.Name = entry.Name()
		}
		skill.Source = source
		skill.Dir = skillDir

		// Project skills take precedence — don't overwrite
		if _, exists := r.skills[skill.Name]; !exists {
			r.skills[skill.Name] = skill
		}
	}
}

// Get returns a skill by name, or nil if not found.
func (r *SkillRegistry) Get(name string) *Skill {
	return r.skills[name]
}

// All returns all loaded skills.
func (r *SkillRegistry) All() map[string]*Skill {
	return r.skills
}

// Count returns the number of loaded skills.
func (r *SkillRegistry) Count() int {
	return len(r.skills)
}

// BundledFiles returns the relative paths of supporting files in the skill
// directory (everything except SKILL.md), sorted. These are the third level of
// progressive disclosure: the agent reads them on demand via read_file/bash
// using the absolute paths formed by joining Dir with each entry.
func (s *Skill) BundledFiles() []string {
	if s.Dir == "" {
		return nil
	}
	var files []string
	_ = filepath.WalkDir(s.Dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(s.Dir, path)
		if relErr != nil || rel == "SKILL.md" {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	sort.Strings(files)
	return files
}

// parseSkillFile reads a SKILL.md file with YAML frontmatter.
func parseSkillFile(path string) (*Skill, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	skill := &Skill{}
	var body strings.Builder

	state := 0 // 0=before frontmatter, 1=in frontmatter, 2=body

	for scanner.Scan() {
		line := scanner.Text()

		switch state {
		case 0:
			if strings.TrimSpace(line) == "---" {
				state = 1
			}
		case 1:
			if strings.TrimSpace(line) == "---" {
				state = 2
				continue
			}
			parts := strings.SplitN(line, ":", 2)
			if len(parts) != 2 {
				continue
			}
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])

			switch key {
			case "name":
				skill.Name = val
			case "description":
				skill.Description = val
			case "model":
				skill.Model = val
			case "allowed-tools":
				for _, t := range strings.Split(val, ",") {
					t = strings.TrimSpace(t)
					if t != "" {
						skill.AllowedTools = append(skill.AllowedTools, t)
					}
				}
			}
		case 2:
			body.WriteString(line)
			body.WriteString("\n")
		}
	}

	skill.Body = strings.TrimSpace(body.String())
	return skill, scanner.Err()
}

// dynamicCmdPattern matches !`command` for dynamic context substitution.
var dynamicCmdPattern = regexp.MustCompile("!`([^`]+)`")

// RenderPrompt processes the skill body template with the given arguments.
// It replaces $ARGUMENTS, $1/$2/etc., and executes !`command` substitutions.
func (s *Skill) RenderPrompt(rawArgs string) string {
	result := s.Body

	// Replace ${SKILL_DIR} with the skill's directory so the body can reference
	// bundled files by absolute path.
	result = strings.ReplaceAll(result, "${SKILL_DIR}", s.Dir)

	// Replace $ARGUMENTS with the full argument string
	result = strings.ReplaceAll(result, "$ARGUMENTS", rawArgs)

	// Replace positional args $1, $2, etc.
	args := splitArgs(rawArgs)
	for i, arg := range args {
		placeholder := fmt.Sprintf("$%d", i+1)
		result = strings.ReplaceAll(result, placeholder, arg)
	}

	// Execute dynamic context commands: !`command`
	result = dynamicCmdPattern.ReplaceAllStringFunc(result, func(match string) string {
		sub := dynamicCmdPattern.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		cmd := sub[1]
		out, err := exec.Command("sh", "-c", cmd).Output()
		if err != nil {
			log.Printf("[skills] dynamic command failed: %s: %v", cmd, err)
			return fmt.Sprintf("(error running %q: %v)", cmd, err)
		}
		return strings.TrimRight(string(out), "\n")
	})

	return result
}

// splitArgs splits a string into shell-like arguments, respecting quotes.
func splitArgs(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}

	var args []string
	var current strings.Builder
	inSingle := false
	inDouble := false

	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '\'' && !inDouble:
			inSingle = !inSingle
		case ch == '"' && !inSingle:
			inDouble = !inDouble
		case ch == ' ' && !inSingle && !inDouble:
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(ch)
		}
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}

// LoadForTool renders the skill body with the given arguments and appends a
// listing of bundled supporting files (with absolute paths) so the agent can
// choose to read them next. This is the payload returned by the `skill` tool —
// the second level of progressive disclosure, with pointers to the third.
func (s *Skill) LoadForTool(rawArgs string) string {
	var sb strings.Builder
	sb.WriteString(s.RenderPrompt(rawArgs))

	if files := s.BundledFiles(); len(files) > 0 {
		sb.WriteString("\n\n---\nBundled files (read with read_file or run with bash as needed):\n")
		for _, f := range files {
			sb.WriteString(fmt.Sprintf("- %s\n", filepath.Join(s.Dir, f)))
		}
	}
	return sb.String()
}

// FormatSkillsList returns a formatted string listing all skills for display.
func (r *SkillRegistry) FormatSkillsList() string {
	if len(r.skills) == 0 {
		return "No skills loaded."
	}

	var sb strings.Builder
	sb.WriteString("Available skills:\n\n")

	for name, skill := range r.skills {
		desc := skill.Description
		if desc == "" {
			desc = "(no description)"
		}
		sb.WriteString(fmt.Sprintf("  /%s — %s [%s]\n", name, desc, skill.Source))
	}

	return sb.String()
}

// FormatForSystemPrompt returns a system-prompt block describing available skills
// so the LLM can suggest them to the user when relevant.
func (r *SkillRegistry) FormatForSystemPrompt() string {
	if len(r.skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("# Available Skills\n\n")
	sb.WriteString("Skills are reusable, task-specific instruction sets. Only their names and descriptions are listed below. When a user's request matches a skill, call the `skill` tool with that skill's name to load its full instructions (and any bundled reference files) into context. You can also tell the user they may invoke a skill directly by typing /<name>.\n\n")

	for name, skill := range r.skills {
		desc := skill.Description
		if desc == "" {
			desc = "(no description)"
		}
		sb.WriteString(fmt.Sprintf("- %s: %s\n", name, desc))
	}

	return sb.String()
}
