package oracle

import (
	"regexp"
	"strconv"
	"strings"

	"dbreader/internal/config"
	"dbreader/internal/driver"
)

func (Driver) DBeaverProviders() []string { return []string{"oracle"} }

// FromDBeaver reads the target from the JDBC URL, and refuses logons
// without a password dbreader could use (wallet, Kerberos, OS).
func (Driver) FromDBeaver(c driver.DBeaverConn, p *config.Profile) bool {
	target(c.URL, c.Props, p)
	if m := c.AuthModel; m != "" && m != "native" && m != "oracle_native" && p.Blocked == "" {
		p.Blocked = "it logs on with " + m + ", and dbreader only does user and password"
	}
	return true
}

var (
	thinURL    = regexp.MustCompile(`(?i)^jdbc:oracle:thin:@(?:(tcps?):)?(?://)?([^:/]+|\[[^\]]+\])(?::(\d+))?(?:[/:](.*))?$`)
	tnsHost    = regexp.MustCompile(`(?i)\(\s*HOST\s*=\s*([^)\s]+)\s*\)`)
	tnsPort    = regexp.MustCompile(`(?i)\(\s*PORT\s*=\s*(\d+)\s*\)`)
	tnsService = regexp.MustCompile(`(?i)\(\s*SERVICE_NAME\s*=\s*([^)\s]+)\s*\)`)
	tnsSID     = regexp.MustCompile(`(?i)\(\s*SID\s*=\s*([^)\s]+)\s*\)`)
	tnsProto   = regexp.MustCompile(`(?i)\(\s*PROTOCOL\s*=\s*(\w+)\s*\)`)
)

// target reads host, port and service or SID from the JDBC URL, which
// DBeaver always writes, in whichever form it has:
//
//	jdbc:oracle:thin:@//host:1521/SERVICE
//	jdbc:oracle:thin:@host:1521/SERVICE
//	jdbc:oracle:thin:@host:1521:SID
//	jdbc:oracle:thin:@(DESCRIPTION=(ADDRESS=(HOST=h)(PORT=1521))(CONNECT_DATA=(SERVICE_NAME=s)))
//	jdbc:oracle:thin:@ALIAS          resolved through tnsnames.ora
//
// props are DBeaver's provider and auth properties, keys lower-cased: they
// say whether the name is a SID, where tnsnames.ora is, and the logon role.
func target(raw string, props map[string]string, p *config.Profile) {
	for k, v := range props {
		switch up := strings.ToUpper(v); {
		case (strings.Contains(k, "logon") || strings.Contains(k, "role")) && (up == "SYSDBA" || up == "SYSOPER"):
			p.Role = strings.ToLower(v)
		}
	}
	raw = strings.TrimSpace(raw)
	at := raw[strings.Index(raw, "@")+1:]
	isAlias := !strings.ContainsAny(at, ":/(") && at != ""

	if isAlias {
		tnsDir := ""
		for k, v := range props {
			if strings.Contains(k, "tns") && (strings.Contains(k, "path") || strings.Contains(k, "admin")) {
				tnsDir = v
			}
		}
		desc, ok := lookupTNS(at, tnsDir)
		if !ok {
			p.Host, p.Database = "", at
			p.Blocked = "its TNS alias " + at + " is in no tnsnames.ora dbreader can find (set TNS_ADMIN to the directory holding it)"
			return
		}
		at = desc
	}
	if strings.HasPrefix(at, "(") {
		// several addresses (RAC, failover): the first one is used
		if m := tnsHost.FindStringSubmatch(at); m != nil {
			p.Host = m[1]
		}
		if m := tnsPort.FindStringSubmatch(at); m != nil {
			p.Port, _ = strconv.Atoi(m[1])
		}
		if m := tnsService.FindStringSubmatch(at); m != nil {
			p.Database, p.SID = m[1], false
		} else if m := tnsSID.FindStringSubmatch(at); m != nil {
			p.Database, p.SID = m[1], true
		}
		if m := tnsProto.FindStringSubmatch(at); m != nil && strings.EqualFold(m[1], "tcps") {
			p.SSLMode = "require"
		}
		if p.Host == "" || p.Database == "" {
			p.Blocked = "its TNS descriptor has no HOST or SERVICE_NAME/SID dbreader can read"
		}
		return
	}
	m := thinURL.FindStringSubmatch(raw)
	if m == nil {
		// no usable URL: the host, port and database fields, if DBeaver wrote them
		p.SID = strings.EqualFold(props["@dbeaver-sid-service@"], "SID")
		if p.Host == "" || p.Database == "" {
			p.Blocked = "its connection URL (" + raw + ") is not in a form dbreader understands"
		}
		return
	}
	p.Host = strings.Trim(m[2], "[]")
	if m[3] != "" {
		p.Port, _ = strconv.Atoi(m[3])
	}
	if strings.EqualFold(m[1], "tcps") {
		p.SSLMode = "require"
	}
	switch rest := m[4]; {
	case rest == "":
		p.Blocked = "its URL names no service or SID"
	case strings.Contains(raw, "@//") || strings.Contains(raw, "/"+rest):
		p.Database, p.SID = rest, false // host:port/service
	default:
		p.Database, p.SID = rest, true // host:port:SID
	}
}
