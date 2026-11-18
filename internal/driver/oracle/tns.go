package oracle

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// tnsEntry starts a tnsnames.ora entry: one or more aliases, "=", then the
// descriptor in parentheses.
var tnsEntry = regexp.MustCompile(`(?m)^[ \t]*([A-Za-z0-9_.\-]+(?:[ \t]*,[ \t]*[A-Za-z0-9_.\-]+)*)[ \t\r\n]*=[ \t\r\n]*\(`)

// tnsDirs lists where tnsnames.ora is looked for: the directory DBeaver was
// given, $TNS_ADMIN, $ORACLE_HOME/network/admin, then the home directory.
func tnsFiles(extra string) []string {
	var files []string
	add := func(dir string) {
		if dir != "" {
			files = append(files, filepath.Join(dir, "tnsnames.ora"))
		}
	}
	add(extra)
	add(os.Getenv("TNS_ADMIN"))
	if home := os.Getenv("ORACLE_HOME"); home != "" {
		add(filepath.Join(home, "network", "admin"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		files = append(files, filepath.Join(home, ".tnsnames.ora"))
		add(home)
	}
	return append(files, "/etc/tnsnames.ora")
}

// lookupTNS returns the descriptor of alias from the first tnsnames.ora
// that defines it.
func lookupTNS(alias, extraDir string) (desc string, ok bool) {
	for _, f := range tnsFiles(extraDir) {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if desc, ok := findTNS(string(data), alias); ok {
			return desc, true
		}
	}
	return "", false
}

// findTNS finds alias in the text of a tnsnames.ora. Names compare without
// case; an entry "DB.example.com" also answers to "DB".
func findTNS(text, alias string) (string, bool) {
	var clean strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		clean.WriteString(line + "\n")
	}
	text = clean.String()
	short := func(s string) string { s, _, _ = strings.Cut(s, "."); return s }

	fallback := ""
	for _, m := range tnsEntry.FindAllStringSubmatchIndex(text, -1) {
		// the descriptor is the balanced parenthesis group opening at the match's end
		open, depth, end := m[1]-1, 0, -1
		for i := open; i < len(text); i++ {
			switch text[i] {
			case '(':
				depth++
			case ')':
				depth--
			}
			if depth == 0 {
				end = i + 1
				break
			}
		}
		if end < 0 {
			continue
		}
		for _, name := range strings.Split(text[m[2]:m[3]], ",") {
			name = strings.TrimSpace(name)
			switch {
			case strings.EqualFold(name, alias):
				return text[open:end], true
			case fallback == "" && strings.EqualFold(short(name), short(alias)):
				fallback = text[open:end]
			}
		}
	}
	return fallback, fallback != ""
}
