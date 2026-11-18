package ui

import (
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/Gandalfalex/vim-kit/cmdline"

	"dbreader/internal/sqlsafe"
)

const maxCompletions = 40

// sqlCatalog is what the sql prompt can complete from.
type sqlCatalog struct {
	tables  []string
	schemas []string
	columns map[string][]string // table → column names
	quote   func(string) string
	fold    foldRule
}

// foldRule is how a server reads unquoted identifiers, which decides which
// names need quotes.
type foldRule int

const (
	foldNone  foldRule = iota // names are used as written
	foldLower                 // unquoted names become lower case (PostgreSQL)
	foldUpper                 // unquoted names become upper case (Oracle)
)

func foldFor(d sqlsafe.Dialect) foldRule {
	switch d {
	case sqlsafe.Postgres:
		return foldLower
	case sqlsafe.Oracle:
		return foldUpper
	}
	return foldNone
}

// needsQuote reports whether name must be quoted to mean itself.
func (r foldRule) needsQuote(name string) bool {
	switch {
	case !plainIdent.MatchString(name) && !(r == foldUpper && oracleIdent.MatchString(name)):
		return true
	case r == foldLower:
		return hasUpperRun.MatchString(name)
	case r == foldUpper:
		return name != strings.ToUpper(name)
	}
	return false
}

var sqlKeywords = strings.Fields(`SELECT FROM WHERE AND OR NOT NULL IS IN LIKE ILIKE BETWEEN ORDER BY GROUP
	HAVING LIMIT OFFSET JOIN LEFT RIGHT INNER OUTER CROSS ON AS DISTINCT COUNT SUM AVG MIN MAX COALESCE
	INSERT INTO VALUES UPDATE SET DELETE CREATE TABLE ALTER DROP TRUNCATE EXPLAIN ANALYZE WITH UNION ALL
	CASE WHEN THEN ELSE END ASC DESC EXISTS RETURNING SHOW DESCRIBE TRUE FALSE`)

// tableWords are the keywords a table name follows.
var tableWords = map[string]bool{
	"FROM": true, "JOIN": true, "UPDATE": true, "INTO": true, "TABLE": true,
	"DESCRIBE": true, "DESC": true, "TRUNCATE": true,
}

var keywordSet = func() map[string]bool {
	set := map[string]bool{}
	for _, k := range sqlKeywords {
		set[k] = true
	}
	return set
}()

var (
	sqlToken    = regexp.MustCompile(`[\pL_][\pL\pN_$]*|[.,]`)
	plainIdent  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	hasUpperRun = regexp.MustCompile(`[A-Z]`)
	oracleIdent = regexp.MustCompile(`^[A-Z][A-Z0-9_$#]*$`)
)

func isIdentRune(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// completeSQL completes the sql prompt: candidates for the word left of
// the cursor (a rune index into text) and the text right of it. Each Value
// is the whole line with the word replaced.
func completeSQL(text string, cursor int, cat sqlCatalog) (items []cmdline.Item, tail string) {
	r := []rune(text)
	cursor = max(0, min(cursor, len(r)))
	words, start := completeWord(text, cursor, cat)
	head, tail := string(r[:start]), string(r[cursor:])
	for _, w := range words {
		w.Value = head + w.Value + tail
		items = append(items, w)
	}
	return items, tail
}

// completeWord returns candidates for the word that ends at the cursor (a
// rune index into text) and the index where that word starts. Each Value is
// the replacement for the word.
//
// After "name." it offers the columns of that table or alias; after FROM,
// JOIN and the like, tables; elsewhere the columns of the tables the
// statement mentions, then tables, then keywords.
func completeWord(text string, cursor int, cat sqlCatalog) (items []cmdline.Item, start int) {
	r := []rune(text)
	cursor = max(0, min(cursor, len(r)))
	start = cursor
	for start > 0 && isIdentRune(r[start-1]) {
		start--
	}
	head, word := string(r[:start]), string(r[start:cursor])

	byLower := map[string]string{}
	for _, t := range cat.tables {
		byLower[strings.ToLower(t)] = t
	}
	aliases, mentioned := tableRefs(text, byLower)

	seen := map[string]bool{}
	add := func(name, desc string, raw bool) {
		if len(items) >= maxCompletions || seen[name] || !strings.HasPrefix(strings.ToLower(name), strings.ToLower(word)) {
			return
		}
		seen[name] = true
		ins := name
		if !raw && cat.fold.needsQuote(name) {
			ins = cat.quote(name)
		}
		items = append(items, cmdline.Item{Value: ins, Label: name, Desc: desc})
	}
	columnsOf := func(table string) {
		for _, c := range cat.columns[table] {
			add(c, table, false)
		}
	}
	tables := func() {
		for _, t := range cat.tables {
			add(t, "table", false)
		}
	}

	// qualified: complete columns of the table or alias before the dot
	if q := qualifier(r[:start]); q != "" {
		t, ok := aliases[strings.ToLower(q)]
		if !ok {
			t, ok = byLower[strings.ToLower(q)]
		}
		if ok {
			columnsOf(t)
		}
		return items, start
	}

	prev := ""
	if toks := sqlToken.FindAllString(head, -1); len(toks) > 0 {
		prev = strings.ToUpper(toks[len(toks)-1])
	}
	if tableWords[prev] {
		tables()
		for _, s := range cat.schemas {
			add(s, "schema", false)
		}
		return items, start
	}
	if word == "" {
		return nil, start
	}

	for _, t := range mentioned {
		columnsOf(t)
	}
	tables()
	lower := word == strings.ToLower(word)
	for _, k := range sqlKeywords {
		if lower {
			k = strings.ToLower(k)
		}
		add(k, "", true)
	}
	if len(mentioned) == 0 && len([]rune(word)) >= 2 {
		// no FROM yet: any column in the schema
		names := make([]string, 0, len(cat.columns))
		for t := range cat.columns {
			names = append(names, t)
		}
		sort.Strings(names)
		for _, t := range names {
			columnsOf(t)
		}
	}
	// nothing to offer when the only candidate is what is already typed
	if len(items) == 1 && items[0].Value == word {
		return nil, start
	}
	return items, start
}

// qualifier returns the name before a trailing dot: "u" for "... u.".
func qualifier(before []rune) string {
	n := len(before)
	if n == 0 || before[n-1] != '.' {
		return ""
	}
	i := n - 1
	for i > 0 && isIdentRune(before[i-1]) {
		i--
	}
	return string(before[i : n-1])
}

// tableRefs finds the tables a statement names after FROM, JOIN, UPDATE and
// INTO, and the aliases given to them (lower-cased).
func tableRefs(text string, byLower map[string]string) (aliases map[string]string, mentioned []string) {
	aliases = map[string]string{}
	toks := sqlToken.FindAllString(text, -1)
	isName := func(i int) bool {
		return i < len(toks) && toks[i] != "." && toks[i] != "," && !keywordSet[strings.ToUpper(toks[i])]
	}
	for i := 0; i < len(toks); i++ {
		if up := strings.ToUpper(toks[i]); !tableWords[up] || up == "DESC" {
			continue
		}
		for {
			i++
			if !isName(i) {
				break
			}
			for i+2 < len(toks) && toks[i+1] == "." && isName(i+2) {
				i += 2 // schema.table: keep the table
			}
			table, ok := byLower[strings.ToLower(toks[i])]
			if i+1 < len(toks) && strings.EqualFold(toks[i+1], "AS") {
				i++
			}
			alias := ""
			if isName(i + 1) {
				i++
				alias = strings.ToLower(toks[i])
			}
			if ok {
				mentioned = append(mentioned, table)
				if alias != "" {
					aliases[alias] = table
				}
			}
			if i+1 >= len(toks) || toks[i+1] != "," {
				break
			}
			i++ // FROM a, b
		}
	}
	return aliases, mentioned
}
