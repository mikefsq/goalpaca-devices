package driver

import "encoding/json"

// Switch layout for clients that draw a board page, returned by the
// SwitchLayout action. Clients without layout support use the flat list.
//
// strip lists switches shown in one line at the top. Each table has one row
// per port; a column gives the id of its switch in the first row, and row r
// uses that id plus r. The row's channel is rowNames[r]; the user's label is
// the text in parentheses in the first column's switch name, as in
// "DC 1 (Camera)". Switches in no strip or table go below, in id order.
//
// A column may list lockedWhen conditions. When a row's switch in the named
// column reads the given state, the cell is greyed out and shows show when set,
// or its own value otherwise. The first condition that matches applies.
type switchLayout struct {
	Kind    string        `json:"kind"`
	Version int           `json:"version"`
	Strip   []int         `json:"strip"`
	Tables  []layoutTable `json:"tables"`
}

type layoutTable struct {
	Title    string         `json:"title"`
	RowNames []string       `json:"rowNames"`
	Columns  []layoutColumn `json:"columns"`
}

type layoutColumn struct {
	Title      string       `json:"title"`
	First      int          `json:"first"`
	LockedWhen []layoutLock `json:"lockedWhen,omitempty"`
}

type layoutLock struct {
	Column string   `json:"column"` // title of another column in the same table
	Is     bool     `json:"is"`     // that row's switch state that locks this cell
	Show   *float64 `json:"show,omitempty"`
}

var full = 100.0

var switchLayoutJSON = func() string {
	l := switchLayout{
		Kind:    "scopehub",
		Version: 1,
		Strip:   []int{0, 1},
		Tables: []layoutTable{
			{
				Title:    "DC",
				RowNames: []string{"DC 1", "DC 2", "DC 3", "DC 4", "DC 5", "DC 6"},
				Columns: []layoutColumn{
					{Title: "Power", First: 2},
					{Title: "PWM", First: 56},
					{Title: "Dew Auto", First: 20},
					// PWM off runs the output steady at 100%; dew auto sets the duty itself,
					// and the cell shows the duty it chose.
					{Title: "Duty Cycle", First: 14, LockedWhen: []layoutLock{
						{Column: "PWM", Is: false, Show: &full},
						{Column: "Dew Auto", Is: true},
					}},
					{Title: "Current", First: 27},
				},
			},
			{
				Title:    "USB",
				RowNames: []string{"USB 1", "USB 2", "USB 3", "USB 4", "USB 5", "USB 6"},
				Columns:  []layoutColumn{{Title: "Power", First: 8}, {Title: "Attached", First: 39}, {Title: "Current", First: 33}},
			},
		},
	}
	b, _ := json.Marshal(l)
	return string(b)
}()
