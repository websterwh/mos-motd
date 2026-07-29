package datasources

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/user"
	"time"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/jedib0t/go-pretty/v6/text"
	log "github.com/sirupsen/logrus"

	"github.com/websterwh/mos-motd/utils"
)

// ConfLastLogin has no options of its own beyond ConfBase.
type ConfLastLogin struct {
	ConfBase `yaml:",inline"`
}

// Init disables the table border by default; this module renders as a
// single line like the header info modules.
func (c *ConfLastLogin) Init() {
	c.ConfBase.Init()
	c.Border = new(bool)
}

// wtmp records are fixed-size, byte-for-byte copies of glibc's `struct utmp`
// (see utmp.h). This layout is stable across distros/architectures on
// little-endian Linux (x86_64 and aarch64, both of which this plugin
// targets) -- it hasn't changed in decades, since changing it would break
// every tool that reads /var/log/wtmp. Offsets below were confirmed against
// a real glibc (via offsetof() in a small C program), not guessed from
// memory: sizeof(struct utmp) == 384, with ut_session/ut_tv using 32-bit
// fields regardless of word size (only the pre-2.4 `__WORDSIZE_COMPAT32`
// x32 ABI differs, which nothing relevant here uses).
//
// Originally this shelled out to the `last` command, but that requires
// util-linux to be installed -- on a minimal/embedded image like MOS's,
// that's an extra dependency for a NAS/homelab appliance to carry just for
// one MOTD line. Parsing wtmp directly means this works with zero external
// binaries, as long as the standard PAM session-tracking (pam_lastlog, wired
// up by default with OpenSSH on Debian/Devuan) is writing to it.
const (
	wtmpPath        = "/var/log/wtmp"
	utmpRecordSize  = 384
	utmpUserProcess = 7 // USER_PROCESS: an actual interactive login session

	utOffType  = 0
	utOffUser  = 44
	utUserLen  = 32
	utOffHost  = 76
	utHostLen  = 256
	utOffTVSec = 340
)

type wtmpEntry struct {
	host string
	when time.Time
}

// GetLastLogin shows the previous login for the current user (excluding the
// session that's currently running the MOTD) by reading /var/log/wtmp
// directly.
func GetLastLogin(channel chan<- SourceReturn, conf *Conf) {
	sourceConf := conf.LastLogin
	sourceConf.Load(conf)

	returnData := NewSourceReturn(conf.debug)
	defer func() {
		channel <- returnData.Return()
	}()

	const labelText = "Last Login"

	outputTable := GetTableWriter(sourceConf)
	// Render as a label/value row (like sysinfo's Version/Kernel/Uptime/...
	// lines) instead of a single "Last Login: <value>" string -- this module
	// sits directly under sysinfo with the same borderless styling, so it
	// should look like one more row in that block, not a colon-separated
	// status line the way bordered box titles (Docker:, SnapRAID:, etc.) do.
	//
	// Pinning column 1's width to the label's length is necessary here
	// specifically because this table only ever has a single row:
	// go-pretty sizes column widths from content across all rows, so with
	// six varied-length rows (like sysinfo's Version/Kernel/Uptime/...)
	// column 1 naturally ends up tight. With only one row and the table's
	// overall width pinned (via table_width), there's no such signal, and
	// it was instead splitting the fixed width close to evenly between the
	// two columns -- pushing the value far to the right instead of right
	// after the label.
	//
	// The "+2" isn't arbitrary: sysinfo's own table (also borderless, same
	// table_width) consistently starts its value column at character index
	// 15 across all six of its rows (measured directly against real output:
	// "Version" -> index 15, "CPU" -> index 15, etc.), while label length
	// alone (len(labelText) == 10) landed at index 13 -- two short. Bumping
	// to len(labelText)+2 lines the value column up with sysinfo's exactly.
	outputTable.SetColumnConfigs([]table.ColumnConfig{
		{Number: 1, Align: text.AlignLeft, WidthMax: len(labelText) + 2, WidthMin: len(labelText) + 2},
	})

	line, ok := getLastLoginSummary()
	if !ok {
		outputTable.AppendRow([]interface{}{labelText, utils.Warn("Unavailable")})
	} else {
		outputTable.AppendRow([]interface{}{labelText, line})
	}

	returnData.Content = RenderTable(outputTable, "")
}

func getLastLoginSummary() (string, bool) {
	u, err := user.Current()
	if err != nil || u.Username == "" {
		log.Debugf("[last-login] could not determine current user: %v", err)

		return "", false
	}

	entries, err := readWtmpForUser(wtmpPath, u.Username)
	if err != nil {
		// Common causes: no login has ever been recorded on this system, or
		// nothing is writing to wtmp at all (e.g. a non-PAM-aware SSH
		// server, or PAM session tracking not configured) -- run
		// `motd --debug` to see the exact error.
		log.Debugf("[last-login] could not read %s for user %q: %v", wtmpPath, u.Username, err)

		return "", false
	}

	// entries[0] is the session currently running this command; entries[1],
	// if present, is the one before it -- the "last login" we want to show.
	if len(entries) < 2 {
		return "first login", true
	}

	return summarizeWtmpEntry(entries[1]), true
}

// readWtmpForUser scans wtmp back-to-front (most recent first) for
// USER_PROCESS records belonging to username, stopping once it has the two
// the caller needs (current session + the one before it).
func readWtmpForUser(path, username string) ([]wtmpEntry, error) {
	data, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		return nil, err
	}

	if len(data) == 0 || len(data)%utmpRecordSize != 0 {
		return nil, fmt.Errorf("%s: size %d isn't a multiple of the utmp record size (%d)", path, len(data), utmpRecordSize)
	}

	var entries []wtmpEntry
	for offset := len(data) - utmpRecordSize; offset >= 0; offset -= utmpRecordSize {
		record := data[offset : offset+utmpRecordSize]

		if binary.LittleEndian.Uint16(record[utOffType:]) != utmpUserProcess {
			continue
		}

		if cString(record[utOffUser:utOffUser+utUserLen]) != username {
			continue
		}

		tvSec := int32(binary.LittleEndian.Uint32(record[utOffTVSec:])) // #nosec G115

		entries = append(entries, wtmpEntry{
			host: cString(record[utOffHost : utOffHost+utHostLen]),
			when: time.Unix(int64(tvSec), 0),
		})

		if len(entries) >= 2 {
			break
		}
	}

	return entries, nil
}

// cString trims a fixed-size, NUL-padded C string field down to its content.
func cString(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}

	return string(b)
}

func summarizeWtmpEntry(e wtmpEntry) string {
	when := e.when.Format("Mon Jan 2 15:04")
	if e.host != "" {
		return fmt.Sprintf("%s on %s", e.host, when)
	}

	return when
}
