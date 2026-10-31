package server

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	ghostty "go.mitchellh.com/libghostty"
)

// Activity signals are scoped to a recognized foreground program. Text left by
// an earlier command must not turn an ordinary shell into a working program.
//
//go:embed activity_rules.json
var activityJSON []byte

type activityMatcher struct {
	Contains         []string          `json:"contains"`
	Regex            string            `json:"regex"`
	LineRegex        string            `json:"line_regex"`
	Any              []activityMatcher `json:"any"`
	All              []activityMatcher `json:"all"`
	Not              *activityMatcher  `json:"not"`
	regex, lineRegex *regexp.Regexp
}
type activityRule struct {
	activityMatcher
	State  string `json:"state"`
	Kind   string `json:"kind"`
	Region string `json:"region"`
	Skip   bool   `json:"skip"`
	Weak   bool   `json:"weak"`
}
type activityProgram struct {
	Process []string       `json:"process"`
	App     string         `json:"app"`
	Prompt  string         `json:"prompt_marker"`
	Rules   []activityRule `json:"rules"`
}

var activityOnce sync.Once
var activityPrograms []activityProgram

func prepareMatcher(m *activityMatcher) error {
	var err error
	if m.Regex != "" {
		m.regex, err = regexp.Compile(m.Regex)
		if err != nil {
			return err
		}
	}
	if m.LineRegex != "" {
		m.lineRegex, err = regexp.Compile("(?m)" + m.LineRegex)
		if err != nil {
			return err
		}
	}
	for i := range m.Any {
		if err := prepareMatcher(&m.Any[i]); err != nil {
			return err
		}
	}
	for i := range m.All {
		if err := prepareMatcher(&m.All[i]); err != nil {
			return err
		}
	}
	if m.Not != nil {
		return prepareMatcher(m.Not)
	}
	return nil
}
func loadActivityPrograms() []activityProgram {
	activityOnce.Do(func() {
		if err := json.Unmarshal(activityJSON, &activityPrograms); err != nil {
			panic(err)
		}
		for i := range activityPrograms {
			for j := range activityPrograms[i].Rules {
				if err := prepareMatcher(&activityPrograms[i].Rules[j].activityMatcher); err != nil {
					panic(fmt.Errorf("activity rule %s: %w", activityPrograms[i].App, err))
				}
			}
		}
	})
	return activityPrograms
}
func (m *activityMatcher) matches(content string) bool {
	lower := strings.ToLower(content)
	for _, part := range m.Contains {
		if !strings.Contains(lower, strings.ToLower(part)) {
			return false
		}
	}
	if m.regex != nil && !m.regex.MatchString(content) {
		return false
	}
	if m.lineRegex != nil && !m.lineRegex.MatchString(content) {
		return false
	}
	if len(m.Any) > 0 {
		found := false
		for i := range m.Any {
			found = found || m.Any[i].matches(content)
		}
		if !found {
			return false
		}
	}
	for i := range m.All {
		if !m.All[i].matches(content) {
			return false
		}
	}
	return m.Not == nil || !m.Not.matches(content)
}
func activityRegion(region, screen, title, prompt string) string {
	if region == "osc_title" {
		return title
	}
	lines := strings.Split(screen, "\n")
	nonempty := []string{}
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			nonempty = append(nonempty, line)
		}
	}
	for _, prefix := range []string{"bottom_non_empty_lines(", "top_non_empty_lines("} {
		if strings.HasPrefix(region, prefix) {
			n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(region, prefix), ")"))
			n = max(0, min(n, len(nonempty)))
			if strings.HasPrefix(prefix, "bottom") {
				return strings.Join(nonempty[len(nonempty)-n:], "\n")
			}
			return strings.Join(nonempty[:n], "\n")
		}
	}
	if region == "after_last_prompt_marker" || region == "before_current_prompt_marker" {
		index := -1
		for i, line := range lines {
			if prompt != "" && strings.HasPrefix(strings.TrimSpace(line), prompt) {
				index = i
			}
		}
		if index >= 0 {
			if region == "after_last_prompt_marker" {
				return strings.Join(lines[index+1:], "\n")
			}
			return strings.Join(lines[:index], "\n")
		}
	}
	if region == "after_last_horizontal_rule" || region == "prompt_box_body" {
		index := -1
		previous := -1
		for i, line := range lines {
			trim := strings.TrimSpace(line)
			if len([]rune(trim)) >= 8 && strings.Trim(trim, "─━═- ") == "" {
				previous = index
				index = i
			}
		}
		if index >= 0 {
			if region == "prompt_box_body" && previous >= 0 {
				return strings.Join(lines[previous+1:index], "\n")
			}
			return strings.Join(lines[index+1:], "\n")
		}
	}
	return strings.Join(nonempty, "\n")
}
func (b *block) activityProgram() *activityProgram {
	foreground, ancestors := b.foregroundInfo()
	processes := append([]any{foreground}, ancestors...)
	for _, process := range processes {
		info := asObject(process)
		for i := range loadActivityPrograms() {
			program := &activityPrograms[i]
			for _, name := range program.Process {
				for _, field := range []string{"name", "path", "argv0", "invoked_path"} {
					if strings.EqualFold(filepath.Base(text(info, field)), name) {
						return program
					}
				}
			}
		}
	}
	return nil
}
func (b *block) inferApplication() {
	program := b.activityProgram()
	if program == nil {
		return
	}
	if program.App == "claude" {
		if state, kind, ok := b.claudeFileStatus(); ok {
			b.setInferredStatus(state, kind, program.App)
			return
		}
	}
	formatter, err := ghostty.NewFormatter(b.term, ghostty.WithFormatterTrim(true), ghostty.WithFormatterUnwrap(true))
	if err != nil {
		return
	}
	screen, err := formatter.FormatString()
	formatter.Close()
	if err != nil {
		return
	}
	var weak *activityRule
	for i := range program.Rules {
		rule := &program.Rules[i]
		content := activityRegion(rule.Region, screen, b.title, program.Prompt)
		if rule.Region == "osc_progress" {
			content = ""
			if b.progressReport != nil {
				content = fmt.Sprintf("%d", b.progressReport.State)
				if b.progressReport.Progress >= 0 {
					content += fmt.Sprintf(";%d", b.progressReport.Progress)
				}
			}
		}
		if !rule.matches(content) {
			continue
		}
		if rule.Skip {
			return
		}
		if rule.Weak {
			if weak == nil {
				weak = rule
			}
			continue
		}
		b.setInferredStatus(rule.State, rule.Kind, program.App)
		return
	}
	if weak != nil {
		b.setInferredStatus(weak.State, weak.Kind, program.App)
	}
}

func (b *block) claudeFileStatus() (string, string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", false
	}
	pid := b.foregroundPID()
	file, err := os.Open(filepath.Join(home, ".claude", "sessions", strconv.Itoa(pid)+".json"))
	if err != nil {
		return "", "", false
	}
	defer file.Close()
	var report struct {
		Status     string `json:"status"`
		WaitingFor string `json:"waitingFor"`
	}
	if json.NewDecoder(io.LimitReader(file, 64<<10)).Decode(&report) != nil {
		return "", "", false
	}
	switch report.Status {
	case "busy":
		return "working", "", true
	case "idle":
		return "idle", "", true
	case "waiting":
		kind := ""
		switch report.WaitingFor {
		case "permission prompt":
			kind = "permission"
		case "dialog open", "input needed":
			kind = "question"
		}
		return "blocked", kind, true
	}
	return "", "", false
}
