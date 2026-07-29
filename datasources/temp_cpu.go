package datasources

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"

	"github.com/shirou/gopsutil/v3/host"
	log "github.com/sirupsen/logrus"

	"github.com/websterwh/mos-motd/utils"
)

// ConfTempCPU extends ConfBase with a list of containers to ignore
type ConfTempCPU struct {
	ConfBaseWarn `yaml:",inline"`
}

// Init sets up default alignment
func (c *ConfTempCPU) Init() {
	c.ConfBaseWarn.Init()
}

// coreTemp is one core's reading, tagged with which physical package
// (socket) it came from -- see cpuTempGopsutil for why this matters on
// multi-socket boards.
type coreTemp struct {
	Package int
	Core    string
	Temp    int
}

// GetCPUTemp returns CPU core temps using gopsutil or parsing sensors output
func GetCPUTemp(channel chan<- SourceReturn, conf *Conf) {
	sourceConf := conf.CPU
	sourceConf.Load(conf)

	returnData := NewSourceReturn(conf.debug)
	defer func() {
		channel <- returnData.Return()
	}()

	temps, isZen, err := cpuTempGopsutil()
	if err != nil {
		log.Warnf("[cpu] temperature read error: %v", err)
	}

	if len(temps) == 0 {
		outputTable := GetTableWriter(sourceConf)
		returnData.Content = RenderTable(outputTable, "CPU Temp: "+utils.Warn("Unavailable"))
	} else {
		returnData.Content = formatCPUTemps(temps, isZen, &sourceConf)
	}
}

func formatCPUTemps(temps []coreTemp, isZen bool, sourceConf *ConfTempCPU) (content string) {
	outputTable := GetTableWriter(sourceConf)
	var title string

	// Only label rows with a package prefix ("Pkg0 Core 8") when more than
	// one package was actually seen -- keeps the common single-socket box
	// looking exactly as it always has ("Core 8"), and only adds the prefix
	// where it's actually needed to disambiguate.
	seenPkg := make(map[int]bool)
	for _, t := range temps {
		seenPkg[t.Package] = true
	}
	multiPackage := len(seenPkg) > 1

	sort.Slice(temps, func(i, j int) bool {
		if temps[i].Package != temps[j].Package {
			return temps[i].Package < temps[j].Package
		}

		// Preserves the original (lexical, not numeric) sort of core
		// numbers within a package -- matches existing behavior exactly,
		// not something newly introduced here.
		return temps[i].Core < temps[j].Core
	})

	var warnCount int
	var errCount int
	for _, t := range temps {
		var wrapped string
		switch {
		case isZen:
			wrapped = t.Core
		case multiPackage:
			wrapped = fmt.Sprintf("Pkg%d Core %s", t.Package, t.Core)
		default:
			wrapped = fmt.Sprintf("Core %s", t.Core)
		}
		coreTemp := t.Temp
		if coreTemp < sourceConf.Warn && !*sourceConf.WarnOnly {
			outputTable.AppendRow([]interface{}{wrapped, utils.Good(coreTemp)})
		} else if coreTemp >= sourceConf.Warn && coreTemp < sourceConf.Crit {
			outputTable.AppendRow([]interface{}{wrapped, utils.Warn(coreTemp)})
			warnCount++
		} else if coreTemp >= sourceConf.Crit {
			warnCount++
			errCount++
			outputTable.AppendRow([]interface{}{wrapped, utils.Err(coreTemp)})
		}
	}
	if warnCount == 0 {
		title = fmt.Sprintf("%s: %s", "CPU Temp", utils.Good("OK"))
	} else if errCount > 0 {
		title = fmt.Sprintf("%s: %s", "CPU Temp", utils.Err("Critical"))
	} else if warnCount > 0 {
		title = fmt.Sprintf("%s: %s", "CPU Temp", utils.Warn("Warning"))
	}

	content = RenderTable(outputTable, title)

	return
}

// cpuTempGopsutil reads per-core temps from gopsutil's hwmon scan.
//
// On multi-socket boards, coretemp exposes one hwmon chip per physical
// package, and each package's core sensors are numbered starting from 0
// again (package 0 has "coretemp_core_0".."coretemp_core_N", and package 1
// *also* has "coretemp_core_0".."coretemp_core_N" -- gopsutil's SensorKey
// doesn't itself distinguish which package a given core sensor belongs to).
// The original version keyed a map purely by core number, so package 1's
// readings silently overwrote package 0's identically-numbered cores,
// showing only one socket's worth of cores on a dual-socket (or more) board
// -- confirmed via --debug on a real 2-socket/40-core box, where the log
// showed "coretemp_package_id_0" followed by cores 0-4,8-12, then
// "coretemp_package_id_1" followed by the *same* core numbers again.
//
// See extractCoreTemps for the full story on how this is resolved -- the
// short version is package number is tracked via "coretemp_package_id_N"
// markers, but readings are deduplicated by (package, core) rather than
// appended unconditionally, since real hardware re-reports some cores a
// second time before their package's own marker appears.
func cpuTempGopsutil() (temps []coreTemp, isZen bool, err error) {
	stats, err := host.SensorsTemperatures()

	temps = extractCoreTemps(stats)

	// Try k10temp if we didn't find anything (AMD Zen). Not known to have
	// the same multi-package numbering collision as coretemp, so no package
	// tagging is attempted here.
	if len(temps) == 0 {
		isZen = true
		log.Debug("[cpu] trying k10temp")
		re := regexp.MustCompile(`k10temp_(\w+)`)
		for _, stat := range stats {
			log.Debugf("[cpu] check %s", stat.SensorKey)
			if m := re.FindStringSubmatch(stat.SensorKey); len(m) > 1 {
				log.Debugf("[cpu] OK %s: %.0f", stat.SensorKey, stat.Temperature)
				temps = append(temps, coreTemp{Core: m[1], Temp: int(stat.Temperature)})
			}
		}
	}

	// Something's really wrong if we still have none
	if len(temps) == 0 {
		log.Warn("[cpu] could not find any CPU temperatures")
	} else {
		err = nil
	}

	return
}

// extractCoreTemps pulls out coretemp core readings, tagged by package, by
// watching for "coretemp_package_id_N" markers as we scan and tagging each
// subsequent core reading with the package it was seen under.
//
// Real --debug output from a 2-socket/40-core box (pasted three times
// across this plugin's development) showed a wrinkle: 5 core readings
// (cores 8-12) show up *before* any package_id marker at all, defaulting to
// package 0 -- and then "package_id_0" appears, followed by a full 10-core
// set (0-4, then 8-12 again) that re-reports those same 5 core numbers. So
// each package's core IDs aren't contiguous (0-4, 8-12, skipping 5-7) --
// consistent with a binned server CPU die that has some core slots fused
// off -- and gopsutil surfaces that first "8-12" group before the
// package_id_0 marker rather than after it.
//
// A first attempt at this treated every reading as a new entry and derived
// "package" purely from occurrence order per core number (first time core 8
// is seen it's package 0, second time it's package 1, etc.) to avoid
// silently overwriting data the way the original core-number-only map did.
// That over-corrected: since the pre-marker cores 8-12 are (most likely)
// the *same physical sensors* as package 0's own 8-12 reported a second
// time by gopsutil, occurrence-based numbering split them into a fake
// extra "package," inflating a 20-core box (10 cores/socket x 2 sockets) to
// 25 rows with 5 duplicate-looking entries.
//
// The fix: keep marker-based package tracking (it correctly separates
// package 0's group from package 1's), but store readings in a map keyed by
// (package, core) instead of appending unconditionally. A later reading for
// a (package, core) pair that's already been recorded -- exactly what
// happens when the pre-marker orphan cores get re-reported under
// package_id_0's own group moments later -- overwrites rather than
// duplicates, collapsing back down to one row per real core.
func extractCoreTemps(stats []host.TemperatureStat) []coreTemp {
	pkgRe := regexp.MustCompile(`coretemp_package_id_(\d+)`)
	coreRe := regexp.MustCompile(`coretemp_core(?:_)?(\d+)`)

	type key struct {
		pkg  int
		core string
	}
	seen := make(map[key]int) // temp value, keyed by (package, core)
	var order []key           // first-seen order, so output is deterministic
	currentPkg := 0

	for _, stat := range stats {
		log.Debugf("[cpu] check %s", stat.SensorKey)
		if m := pkgRe.FindStringSubmatch(stat.SensorKey); len(m) > 1 {
			if pkgNum, convErr := strconv.Atoi(m[1]); convErr == nil {
				currentPkg = pkgNum
			}

			continue
		}

		m := coreRe.FindStringSubmatch(stat.SensorKey)
		if len(m) <= 1 {
			continue
		}

		k := key{pkg: currentPkg, core: m[1]}
		if _, exists := seen[k]; !exists {
			order = append(order, k)
		}
		seen[k] = int(stat.Temperature)

		log.Debugf("[cpu] OK %s: %.0f (package %d)", stat.SensorKey, stat.Temperature, currentPkg)
	}

	temps := make([]coreTemp, 0, len(order))
	for _, k := range order {
		temps = append(temps, coreTemp{Package: k.pkg, Core: k.core, Temp: seen[k]})
	}

	return temps
}
