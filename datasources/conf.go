package datasources

// Conf is the combined config struct, defines YAML file
type Conf struct {
	ConfGlobal   `yaml:"global"`
	Header       ConfHeader    `yaml:"header"`
	CPU          ConfTempCPU   `yaml:"cpu"`
	GPU          ConfGPU       `yaml:"gpu"`
	Docker       ConfDocker    `yaml:"docker"`
	VMs          ConfVMs       `yaml:"vms"`
	SysInfo      ConfSysInfo   `yaml:"sysinfo"`
	UserDrives   ConfDrives    `yaml:"user-drives"`
	SystemDrives ConfDrives    `yaml:"system-drives"`
	Networks     ConfNet       `yaml:"network"`
	Services     ConfServices  `yaml:"services"`
	Snapraid     ConfSnapraid  `yaml:"snapraid"`
	LastLogin    ConfLastLogin `yaml:"last-login"`
}

// Init a config with sane default values
func (c *Conf) Init() {
	// Set global defaults
	c.WarnOnly = true
	c.Border = true
	c.Enabled = true
	c.ColPad = 1
	c.ColDef = [][]string{
		{"sysinfo"},
		{"last-login"},
		{"docker", "cpu"},
		{"vms"},
		{"services", "networks"},
		{"user-drives", "system-drives"},
		{"snapraid", "gpu"},
	}
	c.FixedTableWidth = 60
	c.Header.Init()
	// Init data source configs
	c.CPU.Init()
	c.GPU.Init()
	c.Docker.Init()
	c.VMs.Init()
	c.SysInfo.Init()
	c.UserDrives.Init()
	c.SystemDrives.Init()
	c.Networks.Init()
	c.Services.Init()
	c.Snapraid.Init()
	c.LastLogin.Init()
}
