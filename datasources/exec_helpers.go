package datasources

import "os"

// commonBinDirs are checked, in order, before falling back to a plain PATH
// lookup. Modules that shell out to external tools (snapraid, last, ...) use
// this instead of relying on PATH alone: the environment MOS's plugin query
// API spawns subprocesses in isn't guaranteed to have the same PATH as an
// interactive login shell, and services.go's own /etc/init.d/<service>
// checks already work reliably specifically because they use an absolute
// path rather than a bare command name.
var commonBinDirs = []string{
	"/usr/sbin",
	"/usr/bin",
	"/sbin",
	"/bin",
	"/usr/local/sbin",
	"/usr/local/bin",
}

// findBinary returns the first existing absolute path for name across
// commonBinDirs, or falls back to name itself (letting exec.Command/LookPath
// try PATH resolution as a last resort).
func findBinary(name string) string {
	for _, dir := range commonBinDirs {
		candidate := dir + "/" + name
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}

	return name
}
