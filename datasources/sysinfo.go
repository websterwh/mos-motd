package datasources

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/websterwh/mos-motd/utils"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/jedib0t/go-pretty/v6/text"

	"github.com/shirou/gopsutil/v3/cpu"
)

type ConfSysInfo struct {
	ConfBase `yaml:",inline"`
}

func (c *ConfSysInfo) Init() {
	c.ConfBase.Init()
	c.Border = new(bool)
}

// GetSysInfo various stats about the host Linux OS (kernel, distro, load and more)
func GetSysInfo(channel chan<- SourceReturn, conf *Conf) {
	sourceConf := conf.SysInfo
	sourceConf.Load(conf)

	returnData := NewSourceReturn(conf.debug)
	defer func() {
		channel <- returnData.Return()
	}()
	type entry struct {
		name    string
		content string
	}

	outputTable := GetTableWriter(sourceConf)
	outputTable.SetColumnConfigs([]table.ColumnConfig{
		{Number: 1, Align: text.AlignLeft},
	})

	// Fetch all the things
	var info = []entry{
		{"Version", getDistroName()},
		{"Kernel", getKernel()},
		{"Uptime", getUptime()},
	}
	info = append(info, entry{"CPU", getCPU()})
	// GPU is inserted right after CPU (only when one was actually detected,
	// unlike the rows around it which are always present -- there's no
	// "Unavailable" row here, it's just left out entirely on a box with no
	// GPU).
	if gpuLine, ok := gpuUsageLine(); ok {
		info = append(info, entry{"GPU", gpuLine})
	}
	info = append(info, entry{"Load", getLoadAvg()}, entry{"RAM", getMemoryInfo()})
	for _, e := range info {
		outputTable.AppendRow([]interface{}{e.name, e.content})
	}
	returnData.Content = RenderTable(outputTable, "")
}

func getCPU() (retStr string) {
	times, _ := cpu.Times(false)

	totalCPU := times[0]
	totalCount := totalCPU.User + totalCPU.System + totalCPU.Idle + totalCPU.Iowait + totalCPU.Softirq

	userPercent := (totalCPU.User / totalCount) * 100
	systemPercent := (totalCPU.System / totalCount) * 100
	idlePercent := (totalCPU.Idle / totalCount) * 100
	ioWaitPercent := (totalCPU.Iowait / totalCount) * 100

	retStr = fmt.Sprintf("Usr: %.1f%% Sys: %.1f%% IO: %.1f%% Idle: %.1f%%", userPercent, systemPercent, ioWaitPercent, idlePercent)

	return
}

// runCmd executes command and returns stdout as string
func runCmd(name string, args string, buf *bytes.Buffer) (string, error) {
	var retStr string
	cmd := exec.Command(name, args)
	cmd.Stdout = buf
	err := cmd.Run()
	if err != nil {
		retStr = utils.Warn("unavailable")
	} else {
		retStr = buf.String()
	}
	buf.Reset()

	return retStr, CommandFailedError(fmt.Sprint(err))
}

// getDistroName reads the standard /etc/os-release file (present on Devuan/MOS
// as well as virtually every other modern Linux distro) and returns PRETTY_NAME.
// If a MOS release marker is present it is appended so the MOTD still shows
// the MOS build/version alongside the base OS.
func getDistroName() (retStr string) {
	retStr = getOSRelease()

	if mosVer := getMOSVersion(); mosVer != "" {
		if retStr == "" {
			retStr = mosVer
		} else {
			retStr = fmt.Sprintf("%s (MOS %s)", retStr, mosVer)
		}
	}

	if retStr == "" {
		retStr = utils.Warn("unavailable")
	}

	return
}

func getOSRelease() (retStr string) {
	file, err := os.Open("/etc/os-release")
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	re := regexp.MustCompile(`^PRETTY_NAME=(.*)`)
	for scanner.Scan() {
		m := re.FindSubmatch(scanner.Bytes())
		if len(m) > 1 {
			retStr = strings.Trim(string(m[1]), `"`)

			return
		}
	}

	return
}

// getMOSVersion looks for a MOS version marker. The exact on-disk location
// isn't fixed by MOS today, so a couple of plausible candidates are checked;
// if neither exists this simply falls back to the /etc/os-release value.
func getMOSVersion() (retStr string) {
	candidates := []string{"/etc/mos-release", "/boot/config/mos-version"}
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		retStr = strings.TrimSpace(string(data))
		if retStr != "" {
			return
		}
	}

	return
}

func getUptime() string {
	var buf bytes.Buffer
	uptime, err := runCmd("uptime", "-p", &buf)
	if err != nil {
		return uptime
	}
	re := regexp.MustCompile(`(up\s|\n)`)

	return re.ReplaceAllString(uptime, "")
}

func getLoadAvg() string {
	loadavg, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return utils.Warn("unavailable")
	}
	var loadArr = strings.Split(string(loadavg), " ")

	return fmt.Sprintf("%s [1m], %s [5m], %s [15m]", loadArr[0], loadArr[1], loadArr[2])
}

func getMemoryInfo() (retStr string) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		retStr = utils.Warn("unavailable")

		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	// Look for active and total
	var memAvailable float64
	var memTotal float64
	reAvailable := regexp.MustCompile(`MemAvailable:\s+(\d+)`)
	reTotal := regexp.MustCompile(`MemTotal:\s+(\d+)`)
	for scanner.Scan() {
		if memTotal != 0 && memAvailable != 0 {
			break
		}
		if memAvailable == 0 {
			// Look for active
			m := reAvailable.FindSubmatch(scanner.Bytes())
			if len(m) > 1 {
				// Store as int
				memAvailable, _ = strconv.ParseFloat(string(m[1]), 64)
			}
		}
		if memTotal == 0 {
			m := reTotal.FindSubmatch(scanner.Bytes())
			if len(m) > 1 {
				// Store as int
				memTotal, _ = strconv.ParseFloat(string(m[1]), 64)
			}
		}
	}
	memUsed := memTotal - memAvailable
	if err := scanner.Err(); err != nil {
		retStr = utils.Warn("unavailable")

		return
	}

	// Convert to GB, meminfo is in kB
	return fmt.Sprintf("%d%% used (%s of %s)", int((memUsed/memTotal)*100), utils.FormatBytes(memUsed*1024), utils.FormatBytes(memTotal*1024))
}

func getKernel() string {
	var buf bytes.Buffer
	var kernel, _ = runCmd("uname", "-sr", &buf)

	return strings.ReplaceAll(kernel, "\n", "")
}
