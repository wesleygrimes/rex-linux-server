package server

import "github.com/google/jsonschema-go/jsonschema"

type shellOptions struct {
	Shell        string           `json:"shell,omitempty"`
	Command      []string         `json:"command,omitempty"`
	Args         []string         `json:"args,omitempty"`
	Login        *bool            `json:"login,omitempty"`
	Cwd          string           `json:"cwd,omitempty"`
	PathPrepend  []string         `json:"path_prepend,omitempty"`
	Size         *initialSize     `json:"size,omitempty"`
	Theme        terminalTheme    `json:"theme,omitempty"`
	InitialInput string           `json:"initial_input,omitempty"`
	Exit         *shellExitPolicy `json:"exit,omitempty"`
}

type initialSize struct {
	Columns    uint32  `json:"columns"`
	Rows       uint32  `json:"rows"`
	CellWidth  *uint32 `json:"cell_width_px,omitempty"`
	CellHeight *uint32 `json:"cell_height_px,omitempty"`
	Mode       string  `json:"mode,omitempty"`
}

type shellExitPolicy struct {
	OnCompletion       *bool   `json:"on_completion,omitempty"`
	QuickExitThreshold *uint32 `json:"quick_exit_threshold_ms,omitempty"`
}

var terminalOptionsSchema = func() *jsonschema.Schema {
	schema, err := jsonschema.For[shellOptions](nil)
	if err != nil {
		panic(err)
	}
	return schema
}()

func shellOptionsSchema() *jsonschema.Schema { return terminalOptionsSchema }
