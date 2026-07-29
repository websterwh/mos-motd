package datasources

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/websterwh/mos-motd/utils"
)

// ConfSnapraid extends ConfBase with SnapRAID-specific thresholds. Parity
// sync age is measured from the mtime of the first "content" file listed in
// the SnapRAID config, since `snapraid status` itself can be slow to run on
// large arrays and its output format isn't guaranteed stable across
// versions.
type ConfSnapraid struct {
	ConfBase   `yaml:",inline"`
	ConfigPath string `yaml:"config_path,omitempty"`
	WarnHours  int    `yaml:"warn_hours,omitempty"`
	CritHours  int    `yaml:"crit_hours,omitempty"`
	// ContentRoot overrides where the MOS-style content-file fallback search
	// looks (see mosSnapraidContentRoot) -- only needed if your setup keeps
	// snapraid's content files somewhere other than /var/snapraid.
	ContentRoot string `yaml:"content_root,omitempty"`
	// CheckErrors controls whether `snapraid status` is run at all as a
	// best-effort error check on top of the age-based sync status. Disable
	// if you'd rather skip it (e.g. a very large array where it's slow), or
	// if your snapraid setup doesn't support running `status` without a
	// classic config file present.
	CheckErrors bool `yaml:"check_errors"`
}

// Init sets sane defaults: warn if no sync in 2 days, critical after 7.
func (c *ConfSnapraid) Init() {
	c.ConfBase.Init()
	c.ConfigPath = "/etc/snapraid.conf"
	c.WarnHours = 48
	c.CritHours = 168
	c.ContentRoot = mosSnapraidContentRoot
	c.CheckErrors = true
}

// snapraidConfigCandidates are tried, in order, if the configured path
// doesn't exist. /etc/snapraid.conf is the conventional upstream default,
// but NAS distros commonly keep app config alongside their own boot config
// tree instead -- e.g. this plugin's own default config path is
// /boot/optional/plugins/motd (see main.go), and services.go already reads
// MOS's own docker/shares state from /boot/config/*.json, so
// /boot/config/snapraid.conf is a reasonable guess for where MOS's own
// SnapRAID setup (if any) keeps its config.
var snapraidConfigCandidates = []string{
	"/etc/snapraid.conf",
	"/boot/config/snapraid.conf",
	"/etc/snapraid/snapraid.conf",
}

// resolveSnapraidConfigPath returns the first existing path among the
// configured one and the fallback candidates, so a stock config.yaml
// pointing at the conventional /etc/snapraid.conf still finds a
// differently-located config without the user needing to edit config_path
// first. If none exist, returns "" -- unlike a real MOS install, which
// doesn't use a classic snapraid.conf at all (see mosSnapraidContentRoot
// below), so falling through to that is the expected path there, not an
// error.
func resolveSnapraidConfigPath(configured string) string {
	candidates := make([]string, 0, len(snapraidConfigCandidates)+1)
	candidates = append(candidates, configured)
	for _, c := range snapraidConfigCandidates {
		if c != configured {
			candidates = append(candidates, c)
		}
	}

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}

	return ""
}

// mosSnapraidContentRoot is where MOS's own SnapRAID integration
// (`mos-snapraid`, backed by `/usr/local/bin/snapraid`) actually keeps its
// content files -- confirmed by inspecting a real install, since MOS
// doesn't generate a classic /etc/snapraid.conf at all:
//
//	/var/snapraid/<pool>/parity<N>/.snapraid.content
//
// (each data disk under /var/mergerfs/<pool>/disk<N>/ also gets its own
// ".snapraid" content copy, per upstream SnapRAID's usual multi-copy
// redundancy convention, but the parity-side copy is the simplest single
// signal to key off of). Pool/array names aren't fixed, so this walks the
// whole tree rather than assuming a specific pool name like "main".
const mosSnapraidContentRoot = "/var/snapraid"

// mosSnapraidContentName is the exact filename mos-snapraid writes its
// content file as.
const mosSnapraidContentName = ".snapraid.content"

// newestMosSnapraidContentFile walks mosSnapraidContentRoot for files named
// mosSnapraidContentName and returns the path and mtime of the most
// recently modified one -- the freshest sync across all pools/parity disks.
func newestMosSnapraidContentFile(root string) (string, time.Time, error) {
	var newestPath string
	var newestMod time.Time

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Best-effort walk -- skip anything unreadable rather than
			// aborting the whole scan over one bad entry.
			return nil
		}
		if d.IsDir() || d.Name() != mosSnapraidContentName {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		if info.ModTime().After(newestMod) {
			newestMod = info.ModTime()
			newestPath = path
		}

		return nil
	})
	if err != nil {
		return "", time.Time{}, err
	}
	if newestPath == "" {
		return "", time.Time{}, fmt.Errorf("no %s files found under %s", mosSnapraidContentName, root)
	}

	return newestPath, newestMod, nil
}

// GetSnapraid reports SnapRAID parity sync age and whether the last
// `snapraid status` run reported any errors.
func GetSnapraid(channel chan<- SourceReturn, conf *Conf) {
	sourceConf := conf.Snapraid
	sourceConf.Load(conf)

	returnData := NewSourceReturn(conf.debug)
	defer func() {
		channel <- returnData.Return()
	}()

	outputTable := GetTableWriter(sourceConf)

	snapraidBin := findBinary("snapraid")
	if _, err := os.Stat(snapraidBin); err != nil {
		if _, lookErr := exec.LookPath(snapraidBin); lookErr != nil {
			log.Debugf("[snapraid] binary %q not found (stat: %v, PATH lookup: %v)", snapraidBin, err, lookErr)
			returnData.Content = RenderTable(outputTable, "SnapRAID: "+utils.Warn("Unavailable"))

			return
		}
	}

	contentRoot := sourceConf.ContentRoot
	if contentRoot == "" {
		contentRoot = mosSnapraidContentRoot
	}

	contentModTime, contentSource, err := resolveSnapraidContentTime(sourceConf.ConfigPath, contentRoot)
	if err != nil {
		log.Debugf("[snapraid] %v", err)
		returnData.Content = RenderTable(outputTable, "SnapRAID: "+utils.Warn("Unavailable"))

		return
	}
	log.Debugf("[snapraid] using content time from %s", contentSource)

	age := time.Since(contentModTime)
	ageHours := int(age.Hours())

	var status string
	switch {
	case ageHours >= sourceConf.CritHours:
		status = utils.Err(fmt.Sprintf("no sync in %s", formatSnapraidAge(age)))
	case ageHours >= sourceConf.WarnHours:
		status = utils.Warn(fmt.Sprintf("no sync in %s", formatSnapraidAge(age)))
	default:
		status = utils.Good(fmt.Sprintf("last sync %s ago", formatSnapraidAge(age)))
	}

	if sourceConf.CheckErrors && snapraidHasErrors(snapraidBin) {
		status = utils.Err("errors reported, run: snapraid status")
	}

	returnData.Content = RenderTable(outputTable, "SnapRAID: "+status)
}

// resolveSnapraidContentTime finds the last-sync time to report, trying a
// classic snapraid.conf-based setup first (for anyone running vanilla
// upstream snapraid) and falling back to MOS's own content-file layout
// (mos-snapraid) under contentRoot if no classic config exists at all --
// which is the normal case on MOS, since it doesn't generate
// /etc/snapraid.conf.
func resolveSnapraidContentTime(configuredPath, contentRoot string) (time.Time, string, error) {
	if configPath := resolveSnapraidConfigPath(configuredPath); configPath != "" {
		contentFile, err := snapraidContentFile(configPath)
		if err == nil {
			if info, statErr := os.Stat(contentFile); statErr == nil {
				return info.ModTime(), contentFile, nil
			}
		}
	}

	path, modTime, err := newestMosSnapraidContentFile(contentRoot)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("no classic config (configured: %s) and no MOS-style content file under %s: %w", configuredPath, contentRoot, err)
	}

	return modTime, path, nil
}

// snapraidContentFile returns the path of the first "content" entry in the
// given SnapRAID config file.
func snapraidContentFile(configPath string) (string, error) {
	f, err := os.Open(configPath) // #nosec G304
	if err != nil {
		return "", err
	}
	defer f.Close()

	re := regexp.MustCompile(`^\s*content\s+(\S+)`)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if m := re.FindStringSubmatch(scanner.Text()); len(m) > 1 {
			return m[1], nil
		}
	}

	return "", fmt.Errorf("no content file found in %s", configPath)
}

// snapraidHasErrors runs `snapraid status` as a best-effort check for
// reported errors. Any failure to run it is treated as "no errors" rather
// than surfacing a second failure mode on top of the age-based status.
func snapraidHasErrors(snapraidBin string) bool {
	out, err := exec.Command(snapraidBin, "status").CombinedOutput() // #nosec G204
	if err != nil {
		log.Debugf("[snapraid] %q status failed: %v", snapraidBin, err)

		return false
	}

	lower := strings.ToLower(string(out))

	return strings.Contains(lower, "error") && !strings.Contains(lower, "0 errors")
}

func formatSnapraidAge(d time.Duration) string {
	hours := int(d.Hours())
	if hours < 1 {
		return "<1h"
	}
	if hours < 48 {
		return fmt.Sprintf("%dh", hours)
	}

	return fmt.Sprintf("%dd", hours/24)
}
