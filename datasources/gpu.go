package datasources

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/shirou/gopsutil/v3/host"
	log "github.com/sirupsen/logrus"

	"github.com/websterwh/mos-motd/utils"
)

// ConfGPU reuses the same warn/crit temperature thresholds as ConfTempCPU.
// Utilization (see gpuStat.Util below) is informational only, like the CPU
// usage line in sysinfo -- no warn/crit coloring applies to it, only temp.
type ConfGPU struct {
	ConfBaseWarn `yaml:",inline"`
}

// Init sets up default alignment (warn 70 / crit 90, same as CPU).
func (c *ConfGPU) Init() {
	c.ConfBaseWarn.Init()
}

// gpuHwmonPatterns matches hwmon sensor keys exposed by open-source GPU
// drivers (gopsutil's host.SensorsTemperatures() already surfaces these --
// e.g. a "radeon" key showed up in this plugin's own CPU temp debug output
// on a box with an AMD GPU, just previously unused since the CPU module
// only looks for coretemp/k10temp). Proprietary NVIDIA doesn't expose hwmon
// sensors at all, so that's tried separately via nvidia-smi below.
var gpuHwmonPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^amdgpu_(\w+)`),
	regexp.MustCompile(`(?i)^radeon_(\w+)`),
	regexp.MustCompile(`(?i)^nouveau_(\w+)`),
}

// gpuStat is temperature plus a best-effort utilization percentage. Util is
// nil when it isn't known (e.g. no equivalent sysfs attribute for the
// driver in question).
type gpuStat struct {
	Temp int
	Util *int
}

// GetGPUTemp reports GPU temperature and, where available, utilization --
// preferring nvidia-smi (the standard tool on boxes with the proprietary
// NVIDIA driver, common for hardware transcoding) and falling back to hwmon
// sensors exposed by open-source GPU drivers (amdgpu/radeon/nouveau). Intel
// integrated GPUs aren't covered -- there's no reliably standard thermal
// sensor path for i915 across kernel versions, unlike the others.
func GetGPUTemp(channel chan<- SourceReturn, conf *Conf) {
	sourceConf := conf.GPU
	sourceConf.Load(conf)

	returnData := NewSourceReturn(conf.debug)
	defer func() {
		channel <- returnData.Return()
	}()

	stats, err := gpuStatsNvidiaSMI()
	if err != nil {
		log.Debugf("[gpu] nvidia-smi unavailable: %v", err)
	}

	if len(stats) == 0 {
		stats = gpuStatsHwmon()
	}

	if len(stats) == 0 {
		outputTable := GetTableWriter(sourceConf)
		returnData.Content = RenderTable(outputTable, "GPU Temp: "+utils.Warn("Unavailable"))

		return
	}

	returnData.Content = formatGPUStats(stats, &sourceConf)
}

// gpuUsageLine returns a compact, uncolored one-line summary of GPU
// temp/utilization for sysinfo's top block, called from there directly --
// mirroring how CPU shows a plain, uncolored usage line up top (getCPU())
// separate from the warn/crit-colored per-core "CPU Temp" box further down.
// Returns ok=false if no GPU could be detected at all, so sysinfo can leave
// the row out entirely rather than show "GPU: Unavailable" among otherwise
// always-present stats.
//
// This does re-run the same nvidia-smi/hwmon detection GetGPUTemp also runs
// if the "gpu" module is enabled in the display config -- they're
// independent goroutines with no shared state, so some duplicated work is
// the simplest option here rather than restructuring both modules around a
// shared cache. In practice this is one extra cheap subprocess call/sysfs
// read per login, not a meaningful cost.
func gpuUsageLine() (string, bool) {
	stats, err := gpuStatsNvidiaSMI()
	if err != nil {
		log.Debugf("[gpu] nvidia-smi unavailable: %v", err)
	}

	if len(stats) == 0 {
		stats = gpuStatsHwmon()
	}

	if len(stats) == 0 {
		return "", false
	}

	names := make([]string, 0, len(stats))
	for name := range stats {
		names = append(names, name)
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, name := range names {
		s := stats[name]
		if s.Util != nil {
			parts = append(parts, fmt.Sprintf("%s: %d°C %d%%", name, s.Temp, *s.Util))
		} else {
			parts = append(parts, fmt.Sprintf("%s: %d°C", name, s.Temp))
		}
	}

	return strings.Join(parts, ", "), true
}

// gpuStatsNvidiaSMI queries all GPUs nvidia-smi knows about, temperature and
// utilization together in a single call. Indexed by GPU index (e.g. "GPU0")
// rather than name, since names can repeat across multiple identical cards.
func gpuStatsNvidiaSMI() (map[string]gpuStat, error) {
	bin := findBinary("nvidia-smi")

	out, err := exec.Command(bin, "--query-gpu=index,temperature.gpu,utilization.gpu", "--format=csv,noheader,nounits").Output() // #nosec G204
	if err != nil {
		return nil, err
	}

	stats := make(map[string]gpuStat)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) != 3 {
			continue
		}
		idx := strings.TrimSpace(parts[0])
		temp, tempErr := strconv.Atoi(strings.TrimSpace(parts[1]))
		if tempErr != nil {
			log.Debugf("[gpu] could not parse nvidia-smi temperature %q: %v", parts[1], tempErr)

			continue
		}
		stat := gpuStat{Temp: temp}
		if util, utilErr := strconv.Atoi(strings.TrimSpace(parts[2])); utilErr == nil {
			stat.Util = &util
		}
		stats[fmt.Sprintf("GPU%s", idx)] = stat
	}

	return stats, nil
}

// gpuStatsHwmon scans the same hwmon sensor list the CPU temp module reads
// (via gopsutil) for temperature, filtering for GPU driver sensor keys
// instead of CPU ones, then best-effort pairs in a utilization reading from
// amdgpu's gpu_busy_percent sysfs attribute (no equivalent exists for
// radeon/nouveau). Only paired automatically when there's exactly one temp
// sensor and exactly one utilization reading -- correlating a specific
// hwmon sensor to a specific DRM card index for multi-GPU setups isn't
// attempted.
func gpuStatsHwmon() map[string]gpuStat {
	stats := make(map[string]gpuStat)

	temps, err := host.SensorsTemperatures()
	if err != nil {
		log.Debugf("[gpu] hwmon read error: %v", err)

		return stats
	}

	for _, stat := range temps {
		for _, re := range gpuHwmonPatterns {
			m := re.FindStringSubmatch(stat.SensorKey)
			if len(m) > 1 {
				log.Debugf("[gpu] OK %s: %.0f", stat.SensorKey, stat.Temperature)
				stats[m[1]] = gpuStat{Temp: int(stat.Temperature)}

				break
			}
		}
	}

	if util, ok := amdgpuBusyPercent(); ok && len(stats) == 1 {
		for name, s := range stats {
			u := util
			s.Util = &u
			stats[name] = s
		}
	}

	return stats
}

// amdgpuBusyPercent reads the first gpu_busy_percent file found under any
// DRM card (an amdgpu-specific sysfs attribute -- radeon/nouveau don't
// expose an equivalent). Returns ok=false if none exists or it can't be
// parsed.
func amdgpuBusyPercent() (int, bool) {
	matches, err := filepath.Glob("/sys/class/drm/card*/device/gpu_busy_percent")
	if err != nil || len(matches) == 0 {
		return 0, false
	}

	data, err := os.ReadFile(matches[0]) // #nosec G304
	if err != nil {
		log.Debugf("[gpu] could not read %s: %v", matches[0], err)

		return 0, false
	}

	util, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		log.Debugf("[gpu] could not parse %s content %q: %v", matches[0], string(data), err)

		return 0, false
	}

	return util, true
}

func formatGPUStats(stats map[string]gpuStat, sourceConf *ConfGPU) (content string) {
	outputTable := GetTableWriter(sourceConf)
	var title string

	sortedNames := make([]string, 0, len(stats))
	for name := range stats {
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)

	var warnCount, errCount int
	for _, name := range sortedNames {
		s := stats[name]
		value := strconv.Itoa(s.Temp)
		if s.Util != nil {
			value = fmt.Sprintf("%d (%d%% util)", s.Temp, *s.Util)
		}
		switch {
		case s.Temp >= sourceConf.Crit:
			warnCount++
			errCount++
			outputTable.AppendRow([]interface{}{name, utils.Err(value)})
		case s.Temp >= sourceConf.Warn:
			warnCount++
			outputTable.AppendRow([]interface{}{name, utils.Warn(value)})
		default:
			if !*sourceConf.WarnOnly {
				outputTable.AppendRow([]interface{}{name, utils.Good(value)})
			}
		}
	}

	switch {
	case errCount > 0:
		title = fmt.Sprintf("%s: %s", "GPU Temp", utils.Err("Critical"))
	case warnCount > 0:
		title = fmt.Sprintf("%s: %s", "GPU Temp", utils.Warn("Warning"))
	default:
		title = fmt.Sprintf("%s: %s", "GPU Temp", utils.Good("OK"))
	}

	content = RenderTable(outputTable, title)

	return
}
