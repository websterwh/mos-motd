package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/alexflint/go-arg"
	"github.com/olekukonko/tablewriter"
	log "github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/writer"
	"gopkg.in/yaml.v2"

	"github.com/websterwh/mos-motd/datasources"

	"github.com/arsham/figurine/figurine"
	"golang.org/x/term"
)

// Matches ANSI SGR escape sequences (the color codes used throughout for
// status text and the figurine header banner).
var ansiEscape = regexp.MustCompile("\x1b\\[[0-9;]*m")

var defaultCfgPath = "/boot/optional/plugins/motd/config.yaml"

func makeTable(buf *strings.Builder, padding int) (table *tablewriter.Table) {
	table = tablewriter.NewWriter(buf)
	table.SetAutoWrapText(false)
	table.SetAutoFormatHeaders(false)
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetCenterSeparator("")
	table.SetColumnSeparator("")
	table.SetRowSeparator("")
	table.SetHeaderLine(false)
	table.SetBorder(false)
	table.SetTablePadding(strings.Repeat(" ", padding))
	table.SetNoWhiteSpace(true)

	return
}

func mapToTable(buf *strings.Builder, inStr map[string]string, colDef [][]string, padding int) {
	table := makeTable(buf, padding)
	var tmp []string
	// Render a new table every row for compact output
	for _, row := range colDef {
		// Just write block to buffer if it is alone
		if len(row) == 1 {
			a, ok := inStr[row[0]]
			// Skip invalid modules
			if !ok {
				continue
			}
			_, _ = fmt.Fprintln(buf, a)

			continue
		}
		tmp = nil
		for _, k := range row {
			a, ok := inStr[k]
			if !ok {
				continue
			}
			tmp = append(tmp, a)
		}
		table.Append(tmp)
		table.Render()
		// Remake table to avoid imbalanced output
		table = makeTable(buf, padding)
	}
}

// makePrintOrder flattens colDef (if present). If showOrder is defined as well, it is ignored.
func makePrintOrder(c *datasources.Conf) (printOrder []string) {
	// Flatten 2-dim input
	for _, row := range c.ColDef {
		printOrder = append(printOrder, row...)
	}

	return
}

var args struct {
	ConfigFile      string `arg:"-c,--config,env:CONFIG_FILE"             help:"Path to config yaml"`
	Debug           bool   `arg:"--debug,env:DEBUG"                       help:"Debug mode"`
	DumpConfig      bool   `arg:"--dump-config"                           help:"Dump config and exit"`
	DumpConfigPath  string `arg:"--dump-config-path"                      help:"Write dumped config to this path instead of stdout"`
	HideUnavailable bool   `arg:"--hide-unavailable,env:HIDE_UNAVAILABLE" help:"Hide unavailable modules"`
	LogLevel        string `arg:"--log-level,env:LOG_LEVEL"               help:"Set log level"`
	Login           bool   `arg:"--login"                                 help:"Mark this as the automatic login-hook invocation, so global.enabled=false can silence it"`
}

func setupLogging() {
	var logLevel log.Level
	defaultLevel := log.WarnLevel

	var err error
	getLogLevels := func(level log.Level) []log.Level {
		ret := make([]log.Level, 0)
		for _, lvl := range log.AllLevels {
			if level >= lvl {
				ret = append(ret, lvl)
			}
		}

		return ret
	}

	log.SetFormatter(&log.TextFormatter{DisableTimestamp: true})
	log.SetOutput(io.Discard)
	if args.Debug {
		logLevel = log.DebugLevel
	} else if args.LogLevel != "" {
		logLevel, err = log.ParseLevel(args.LogLevel)
		if err != nil {
			logLevel = defaultLevel
			log.Warnf("Unknown log level %s, defaulting to %s", args.LogLevel, logLevel.String())
		}
	} else {
		logLevel = defaultLevel
	}
	log.SetLevel(logLevel)
	levels := getLogLevels(logLevel)
	if args.Debug {
		log.AddHook(&writer.Hook{
			Writer:    os.Stderr,
			LogLevels: levels,
		})
	}
}

func runModules(conf *datasources.Conf) string {
	outOrder, outData := datasources.RunSources(makePrintOrder(conf), conf)
	outStr := make(map[string]string)
	// Wait and save results
	for _, source := range outOrder {
		sourceData, ok := outData[source]
		if !ok {
			continue
		}
		// Check if we should skip due to unavailable error
		if _, unOK := sourceData.Error.(datasources.UnavailableError); unOK && args.HideUnavailable {
			continue
		}
		if sourceData.Error != nil {
			log.Warnf("%s error: %v", source, sourceData.Error)
		}

		if sourceData.Content != "" {
			outStr[source] = sourceData.Content
		}
	}
	outBuf := &strings.Builder{}

	mapToTable(outBuf, outStr, conf.ColDef, conf.ColPad)

	// Show timing results
	if args.Debug {
		for _, k := range outOrder {
			log.Debugf("%s ran in: %s", k, outData[k].Time.String())
		}
	}

	return outBuf.String()
}

func main() {
	args.ConfigFile = defaultCfgPath
	arg.MustParse(&args)

	setupLogging()

	var mainStart time.Time
	if args.Debug {
		mainStart = time.Now()
	}
	// Read config file
	conf, err := datasources.NewConfFromFile(args.ConfigFile, args.Debug)
	if err != nil {
		log.Warn(err)
	}

	if args.DumpConfig {
		log.Info("Dumping config")
		// Intentionally dump a fresh, code-level default Conf here rather than
		// the one just loaded from args.ConfigFile above. NewConfFromFile
		// starts from Init() defaults and then unmarshals the existing file
		// on top, so any custom value already in the file survives into
		// `conf` unchanged. If we dumped `conf`, --dump-config would just
		// re-serialize the current (possibly customized) config back out --
		// a no-op when writing back to the same path, which is exactly what
		// made the settings page's "Reset to Default" button appear to do
		// nothing: it shells out to `motd --dump-config --dump-config-path
		// config.yaml`, which was silently re-writing the same custom values
		// it was supposed to be discarding.
		var defaults datasources.Conf
		defaults.Init()
		dumpConfig(&defaults, args.DumpConfigPath)

		return
	}

	// global.enabled is an off switch for the automatic login hook only --
	// /etc/profile.d/motd.sh (see `functions`) invokes this with --login, so
	// gating on args.Login here means a disabled config still doesn't affect
	// running the binary directly or the settings page's Preview MOTD button
	// (neither of which pass --login), only the thing that fires on every
	// new shell.
	if args.Login && !conf.Enabled {
		return
	}

	// A bare invocation (no flags beyond --login) with no controlling
	// terminal -- e.g. /etc/profile.d sourced during a non-interactive shell
	// like `scp`/`ssh host cmd` -- should stay silent, same as before. Any
	// other explicit flag (such as --hide-unavailable from the MOS
	// "Preview MOTD" button, which runs the binary through the query API
	// where stdin isn't a tty) should still produce output. --login itself
	// doesn't count as an "explicit flag" for this check -- it's always
	// present on the login hook's invocation now (see `functions`), so
	// treating it as significant here would defeat the whole point of this
	// check.
	isTerminal := term.IsTerminal(0)
	if !isTerminal {
		hasOutputFlags := false
		for _, a := range os.Args[1:] {
			if a != "--login" {
				hasOutputFlags = true

				break
			}
		}
		if !hasOutputFlags {
			return
		}
	}

	if isTerminal {
		if width, _, err := term.GetSize(0); err == nil && conf.FixedTableWidth > width {
			conf.FixedTableWidth = width
		}
	}

	// Colorized output (the header banner's rainbow gradient and the
	// green/yellow/red status text) only makes sense on a real terminal.
	// When invoked without one -- e.g. through the MOS "Preview MOTD"
	// button, which runs the binary via an API call -- render everything
	// into a buffer first and strip the raw ANSI escape codes before
	// printing, since they'd otherwise show up as garbage text.
	outBuf := &bytes.Buffer{}

	if conf.Header.Show {
		text := conf.Header.CustomText
		if conf.Header.UseHostname {
			text, _ = os.Hostname()
		}

		if err := figurine.Write(outBuf, text, conf.Header.Font); err != nil {
			log.Debug(err.Error())
		}

		fmt.Fprintln(outBuf, "")
	}

	outBuf.WriteString(runModules(&conf))

	if isTerminal {
		fmt.Print(outBuf.String())
	} else {
		fmt.Print(ansiEscape.ReplaceAllString(outBuf.String(), ""))
	}

	// Show timing results
	if args.Debug {
		log.Debugf("main ran in: %s", time.Since(mainStart).String())
	}
}

func dumpConfig(c *datasources.Conf, writeFile string) {
	configDump, err := yaml.Marshal(c)
	if err != nil {
		log.Errorf("Config parse error: %v", err)

		return
	}
	if writeFile != "" {
		err = os.WriteFile(writeFile, configDump, 0600)
		if err != nil {
			log.Errorf("Config dumped failed: %v", err)

			return
		}
		log.Infof("Config dumped to: %s", writeFile)
	} else {
		fmt.Printf("%s\n", string(configDump))
	}
}
