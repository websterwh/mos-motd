package datasources

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"regexp"

	"github.com/websterwh/mos-motd/utils"
)

type ConfServices struct {
	ConfBase `yaml:",inline"`
	Services []string `yaml:"monitor,flow"`
}

// Init is mandatory
func (c *ConfServices) Init() {
	// Base init must be called
	c.ConfBase.Init()
	// Service names match Devuan/MOS init.d script names rather than
	// Unraid/Slackware's rc.<service> convention. nfs-kernel-server is
	// intentionally NOT in the default list -- unlike Samba/SSH/Docker,
	// NFS shares are opt-in, and isServiceEnabled()'s enabled-flag check
	// (below) is a best-effort guess at MOS's config schema, not
	// confirmed reliable enough to trust for a service that's frequently
	// left disabled. Add "nfs-kernel-server" to `services.monitor` in
	// config.yaml if you do use NFS shares.
	c.Services = []string{
		"nginx",
		"smbd",
		"ssh",
		"docker",
	}
}

func GetServices(channel chan<- SourceReturn, conf *Conf) {
	sourceConf := conf.Services
	sourceConf.Load(conf)

	returnData := NewSourceReturn(conf.debug)
	defer func() {
		channel <- returnData.Return()
	}()
	returnData.Content = getServiceStatus(&sourceConf)
}

// isServiceEnabled checks MOS's own JSON config files (under /boot/config)
// to decide whether a service was intentionally disabled by the user, so
// disabled services aren't reported as "not running". If the relevant
// config file/key can't be found, the service is assumed enabled and its
// actual init.d status decides the row.
func isServiceEnabled(service string) bool {
	type enabledCfg struct {
		Enabled *bool `json:"enabled"`
	}

	var path, altPath string

	switch service {
	case "docker":
		path = "/boot/config/docker.json"
	case "smbd", "nmbd":
		path = "/boot/config/shares.json"
		altPath = "/boot/config/network.json"
	case "nfs-kernel-server", "nfsd":
		path = "/boot/config/shares.json"
	default:
		return true
	}

	for _, p := range []string{path, altPath} {
		if p == "" {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var cfg enabledCfg
		if err := json.Unmarshal(data, &cfg); err != nil {
			continue
		}
		if cfg.Enabled != nil {
			return *cfg.Enabled
		}
	}

	return true
}

func getServiceStatus(sourceConf *ConfServices) (content string) {
	outputTable := GetTableWriter(sourceConf)

	overall := utils.Good("OK")

	//SERVICES:
	for _, service := range sourceConf.Services {
		reg := regexp.MustCompile(`[^a-zA-Z0-9\-_]+`)
		service = reg.ReplaceAllString(service, "")

		if !isServiceEnabled(service) {
			continue
		}

		cmd := exec.Command("/etc/init.d/"+service, "status") // #nosec G204
		err := cmd.Run()

		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			overall = utils.Err("Critical")
			outputTable.AppendRow([]interface{}{service, utils.Err("Not running")})
		} else if !*sourceConf.WarnOnly {
			outputTable.AppendRow([]interface{}{service, utils.Good("Running")})
		}
	}

	content = RenderTable(outputTable, "Services: "+overall)

	return
}
