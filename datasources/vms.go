package datasources

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"

	log "github.com/sirupsen/logrus"

	"github.com/websterwh/mos-motd/utils"
)

// ConfVMs mirrors ConfDocker's shape: a warn-only toggle (via ConfBase) plus
// an ignore list for VMs you don't want shown/flagged.
type ConfVMs struct {
	ConfBase `yaml:",inline"`
	// List of VM (domain) names to ignore
	Ignore []string `yaml:"ignore"`
}

// Init sets up defaults
func (c *ConfVMs) Init() {
	c.ConfBase.Init()
	c.Ignore = []string{}
}

// vmGoodStates are libvirt domain states not treated as a problem. This
// deliberately includes "shut off" -- same lesson learned from Docker
// containers here: a VM being off because someone turned it off on purpose
// isn't a failure worth flagging, only something actually wrong (a crash,
// or a state virsh itself couldn't determine) is.
var vmGoodStates = map[string]bool{
	"running":     true,
	"idle":        true,
	"paused":      true,
	"in shutdown": true,
	"pmsuspended": true,
	"shut off":    true,
}

// GetVMs reports libvirt VM (domain) status via `virsh`, the same shape as
// the Docker module: a bordered box listing each VM and its state, or
// "Unavailable" if virsh/libvirt isn't present on this system at all.
func GetVMs(channel chan<- SourceReturn, conf *Conf) {
	sourceConf := conf.VMs
	sourceConf.Load(conf)

	returnData := NewSourceReturn(conf.debug)
	defer func() {
		channel <- returnData.Return()
	}()

	outputTable := GetTableWriter(sourceConf)

	virshBin := findBinary("virsh")

	names, err := listVMNames(virshBin)
	if err != nil {
		log.Debugf("[vms] could not list VMs via %q: %v", virshBin, err)
		returnData.Content = RenderTable(outputTable, "VMs: "+utils.Warn("Unavailable"))

		return
	}

	var ignoreSet utils.StringSet
	ignoreSet = ignoreSet.FromList(sourceConf.Ignore)

	states := make(map[string]string)
	var sortedNames []string
	for _, name := range names {
		if ignoreSet.Contains(name) {
			continue
		}
		state, stateErr := vmDomState(virshBin, name)
		if stateErr != nil {
			log.Debugf("[vms] could not get state for %q: %v", name, stateErr)
			state = "unknown"
		}
		states[name] = strings.ToLower(state)
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)

	if len(sortedNames) == 0 {
		returnData.Content = RenderTable(outputTable, "VMs: "+utils.Good("None defined"))

		return
	}

	warnOnly := *sourceConf.WarnOnly
	var warnCount, goodCount int
	for _, name := range sortedNames {
		state := states[name]
		if vmGoodStates[state] {
			goodCount++
			if !warnOnly {
				outputTable.AppendRow([]interface{}{name, utils.Good(state)})
			}
		} else {
			warnCount++
			outputTable.AppendRow([]interface{}{name, utils.Err(state)})
		}
	}

	var title string
	switch {
	case goodCount == 0:
		title = fmt.Sprintf("%s: %s", "VMs", utils.Err("Critical"))
	case warnCount == 0:
		title = fmt.Sprintf("%s: %s", "VMs", utils.Good("OK"))
	default:
		title = fmt.Sprintf("%s: %s", "VMs", utils.Warn("Warning"))
	}

	returnData.Content = RenderTable(outputTable, title)
}

// listVMNames lists every defined VM (running or not) by name. Using
// `--name` output (one name per line) rather than parsing the fixed-width
// `virsh list --all` table, since domain names could in principle contain
// spaces that would break naive column splitting.
func listVMNames(virshBin string) ([]string, error) {
	out, err := exec.Command(virshBin, "--connect", "qemu:///system", "list", "--all", "--name").Output() // #nosec G204
	if err != nil {
		return nil, err
	}

	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			names = append(names, line)
		}
	}

	return names, nil
}

// vmDomState gets a single VM's state as a plain string (e.g. "running",
// "shut off", "paused", "crashed").
func vmDomState(virshBin, name string) (string, error) {
	out, err := exec.Command(virshBin, "--connect", "qemu:///system", "domstate", name).Output() // #nosec G204
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(out)), nil
}
