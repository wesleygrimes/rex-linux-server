package server

import (
	"maps"
	"reflect"
	"sort"
	"strings"
	"time"

	ghostty "go.mitchellh.com/libghostty"
)

type programRecord struct {
	value    object
	order    uint64
	inferred bool
}

func (b *block) inferProgress(report ghostty.TerminalProgressReport) {
	if b.activityProgram() != nil {
		return
	}
	state := ""
	switch report.State {
	case ghostty.TerminalProgressStateSet, ghostty.TerminalProgressStateIndeterminate:
		state = "working"
	case ghostty.TerminalProgressStateError:
		state = "error"
	case ghostty.TerminalProgressStateRemove:
		if !b.programs[""].inferred {
			return
		}
		state = "done"
	default:
		return
	}
	b.setInferredStatus(state, "", "")
}

func (b *block) setInferredStatus(state, kind, app string) {
	if b.programs == nil {
		b.programs = map[string]programRecord{}
	}
	previous := b.programs[""].value
	owner := b.currentOwner()
	if text(previous, "state") == state && text(previous, "kind") == kind && text(previous, "app") == app && reflect.DeepEqual(previous["owner"], owner) {
		return
	}
	value := object{"state": state, "updated_at": time.Now()}
	if kind != "" {
		value["kind"] = kind
	}
	if app != "" {
		value["app"] = app
	}
	if owner != nil {
		value["owner"] = owner
	}
	b.programOrder++
	b.programs[""] = programRecord{value: value, order: b.programOrder, inferred: true}
	b.pending = append(b.pending, blockEvent{"program_status_changed", object{"id": "", "record": b.statusValue(""), "reason": "report"}})
}

func (b *block) statusValue(id string) object {
	record := maps.Clone(b.programs[id].value)
	app := text(record, "app")
	for parent := id; app == "" && parent != ""; {
		if index := strings.LastIndexByte(parent, '/'); index >= 0 {
			parent = parent[:index]
		} else {
			parent = ""
		}
		app = text(b.programs[parent].value, "app")
	}
	if app != "" {
		record["effective_app"] = app
	}
	return record
}

func (b *block) programStatus() object {
	ids := make([]string, 0, len(b.programs))
	for id := range b.programs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	records := make([]any, 0, len(ids))
	for _, id := range ids {
		records = append(records, b.statusValue(id))
	}
	return object{"records": records}
}

func (b *block) removeProgramStatus(reason string, match func(string, object) bool) {
	ids := []string{}
	for id, record := range b.programs {
		if match(id, record.value) {
			delete(b.programs, id)
			ids = append(ids, id)
		}
	}
	if len(ids) > 0 {
		sort.Strings(ids)
		b.pending = append(b.pending, blockEvent{"program_status_removed", object{"ids": ids, "reason": reason}})
	}
}

func (b *block) removeInProgress(reason string) {
	b.removeProgramStatus(reason, func(_ string, record object) bool {
		return record["state"] == "working" || record["state"] == "blocked"
	})
}

func (b *block) applyProgramStatus(_ *ghostty.Terminal, report ghostty.ProgramStatus) {
	if report.State == ghostty.ProgramStatusStateClear {
		b.removeProgramStatus("clear", func(id string, _ object) bool {
			return report.ID == "" || id == report.ID || strings.HasPrefix(id, report.ID+"/")
		})
		return
	}
	state := map[ghostty.ProgramStatusState]string{
		ghostty.ProgramStatusStateIdle: "idle", ghostty.ProgramStatusStateWorking: "working",
		ghostty.ProgramStatusStateDone: "done", ghostty.ProgramStatusStateBlocked: "blocked",
		ghostty.ProgramStatusStateError: "error",
	}[report.State]
	if state == "" {
		return
	}
	if b.programs == nil {
		b.programs = make(map[string]programRecord)
	}
	if _, exists := b.programs[report.ID]; !exists && len(b.programs) >= 64 {
		oldest := ""
		order := ^uint64(0)
		for id, record := range b.programs {
			if record.order < order {
				oldest, order = id, record.order
			}
		}
		b.removeProgramStatus("evicted", func(id string, _ object) bool { return id == oldest })
	}
	value := object{"state": state, "updated_at": time.Now()}
	for key, v := range map[string]string{"id": report.ID, "app": report.App, "title": report.Title, "msg": report.Message} {
		if v != "" {
			value[key] = v
		}
	}
	if report.Progress >= 0 {
		value["progress"] = report.Progress
	}
	if kind := map[ghostty.ProgramStatusKind]string{ghostty.ProgramStatusKindPermission: "permission", ghostty.ProgramStatusKindQuestion: "question", ghostty.ProgramStatusKindAuth: "auth"}[report.Kind]; kind != "" {
		value["kind"] = kind
	}
	pid := b.foregroundPID()
	if name := processName(pid); name != "" {
		value["owner"] = object{"pid": pid, "name": name}
	}
	b.programOrder++
	b.programs[report.ID] = programRecord{value: value, order: b.programOrder}
	b.pending = append(b.pending, blockEvent{"program_status_changed", object{"id": report.ID, "record": b.statusValue(report.ID), "reason": "report"}})
}

func (b *block) semanticPrompt(_ *ghostty.Terminal, prompt ghostty.TerminalSemanticPrompt) {
	if prompt.Kind == ghostty.SemanticPromptStart && prompt.PromptKind == ghostty.PromptPrimary {
		b.removeInProgress("prompt")
	}
}
