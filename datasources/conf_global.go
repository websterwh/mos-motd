package datasources

// ConfGlobal is the config struct for global settings
type ConfGlobal struct {
	// Hide fields which are deemed to be OK
	WarnOnly bool `yaml:"warnings_only"`
	// Define how data sources are displayed
	ColDef [][]string `yaml:"display,flow,omitempty"`
	// Padding between columns when using col_def
	ColPad int `yaml:"padding"`
	// Internal variables
	debug bool

	FixedTableWidth int  `yaml:"table_width"`
	Border          bool `yaml:"border"`
	// Enabled is an overall on/off switch for the automatic login hook
	// (/etc/profile.d/motd.sh, invoked with --login). It does not affect
	// running the binary directly or the settings page's Preview MOTD --
	// both of those still work even when this is false, so you can flip it
	// back on without losing the ability to check your work first.
	Enabled bool `yaml:"enabled"`
}
