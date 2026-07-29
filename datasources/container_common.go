package datasources

import (
	"fmt"
	"sort"
	"strings"

	"github.com/websterwh/mos-motd/utils"
)

type containerStatus struct {
	Name   string
	Status string
	// ExitCode is only populated for "exited" containers, parsed from
	// Docker's human-readable status string. nil means unknown/not
	// applicable.
	ExitCode *int
}

type containerList struct {
	Runtime    string
	Root       bool
	Containers []containerStatus
}

// cleanStopLabel returns the label to show for an "exited" container. Exit
// codes turned out too inconsistent across apps to reliably distinguish "you
// stopped this on purpose" from "this crashed": docker sends SIGTERM then
// SIGKILL on a normal `docker stop`, but plenty of apps still exit with
// something else entirely (e.g. plain 1) even on a completely deliberate,
// successful stop, depending on how they handle the signal. On a homelab box
// where people start/stop containers constantly, a container simply being
// "exited" isn't inherently a problem worth flagging at all -- so every
// "exited" container is shown as informational (never red/yellow) regardless
// of its exit code, with the code included in the label when it's not one of
// the common "someone asked this to stop" codes, purely for visibility.
func cleanStopLabel(code *int) string {
	if code == nil {
		return "stopped"
	}

	switch *code {
	case 0, 137, 143:
		return "stopped"
	default:
		return fmt.Sprintf("stopped (exit %d)", *code)
	}
}

func (cl *containerList) getContent(ignoreList []string, warnOnly bool, sourceConf TableConfig) (content string) {
	outputTable := GetTableWriter(sourceConf)
	var title string

	// Make set of ignored containers
	var ignoreSet utils.StringSet
	ignoreSet = ignoreSet.FromList(ignoreList)
	// Process output
	var goodCont = make(map[string]string)
	var failedCont = make(map[string]string)
	var sortedNames []string
	for _, container := range cl.Containers {
		if ignoreSet.Contains(container.Name) {
			continue
		}
		status := strings.ToLower(container.Status)
		switch {
		case status == "up" || status == "created" || status == "running":
			goodCont[container.Name] = status
		case status == "exited":
			goodCont[container.Name] = cleanStopLabel(container.ExitCode)
		default:
			failedCont[container.Name] = status
		}
		sortedNames = append(sortedNames, container.Name)
	}
	sort.Strings(sortedNames)

	// Decide what header should be
	if len(goodCont) == 0 && len(sortedNames) > 0 {
		title = fmt.Sprintf("%s: %s", cl.Runtime, utils.Err("Critical"))
	} else if len(failedCont) == 0 {
		title = fmt.Sprintf("%s: %s", cl.Runtime, utils.Good("OK"))
	} else if len(failedCont) < len(sortedNames) {
		title = fmt.Sprintf("%s: %s", cl.Runtime, utils.Warn("Warning"))
	}

	// Only print all containers if requested
	for _, c := range sortedNames {
		if val, ok := goodCont[c]; ok && !warnOnly {
			outputTable.AppendRow([]interface{}{c, utils.Good(val)})
		} else if val, ok := failedCont[c]; ok {
			outputTable.AppendRow([]interface{}{c, utils.Err(val)})
		}
	}

	content = RenderTable(outputTable, title)

	return
}
