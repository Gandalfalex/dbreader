// Package sqlsafe decides whether a SQL statement only reads. It is the
// first of two guards: anything it calls Read is still run inside a
// read-only transaction, so a wrong "read" verdict fails at the server
// instead of changing data. Everything it does not recognise counts as a
// write.
package sqlsafe

import (
	"strings"
	"unicode"
)

// Dialect holds the rules that differ between servers. Each driver
// declares its own; the zero value is plain standard SQL.
type Dialect struct {
	NestedComments bool // /* /* */ */ nests (PostgreSQL)
	DollarQuotes   bool // $tag$ ... $tag$ strings (PostgreSQL)
	// BackslashEscapes: a backslash escapes the next character in '...' and
	// "..." (MySQL).
	BackslashEscapes bool
	QQuotes          bool // q'[ ... ]' and nq'{ ... }' strings (Oracle)
	// SlashTerminator: a line holding only "/" ends a statement (SQL*Plus).
	SlashTerminator bool
	// Blocks: BEGIN, DECLARE and CREATE FUNCTION|PROCEDURE|PACKAGE|TRIGGER|
	// TYPE|LIBRARY open a procedural block that may run anything and whose
	// semicolons do not end it (PL/SQL); it runs to the next "/".
	Blocks bool
	// ReadVerbs replaces the verbs that start a read; nil means SELECT,
	// SHOW, DESCRIBE, DESC, EXPLAIN, VALUES, TABLE, WITH and PRAGMA.
	ReadVerbs map[string]bool
	// ImplicitCommit lists verbs the server commits before and after: they
	// cannot be rolled back and end any open transaction.
	ImplicitCommit map[string]bool
	// Fold is how unquoted identifiers are read.
	Fold Fold
}

// Fold is how a server reads unquoted identifiers, which decides which
// names need quotes.
type Fold int

const (
	FoldNone  Fold = iota // names are used as written
	FoldLower             // unquoted names become lower case (PostgreSQL)
	FoldUpper             // unquoted names become upper case (Oracle)
)

var standard = &Dialect{}

// or returns d, or standard SQL for nil.
func (d *Dialect) or() *Dialect {
	if d == nil {
		return standard
	}
	return d
}

// Kind is how dangerous a statement is, in rising order.
type Kind int

const (
	Read        Kind = iota // runs without asking, in a read-only transaction
	Write                   // changes rows; needs confirmation
	Destructive             // drops, truncates, alters or changes every row
	TxControl               // BEGIN/COMMIT/...; the app owns transactions
)

func (k Kind) String() string {
	return [...]string{"read", "write", "destructive", "transaction control"}[k]
}

// Stmt is one classified statement.
type Stmt struct {
	Text   string
	Kind   Kind
	Verb   string // first keyword, upper case
	Reason string // why it is not a plain read; shown in the confirmation
	NoTx   bool   // cannot run inside a transaction, so it cannot be rolled back
}

type token struct {
	word  string // upper-cased keyword or identifier; "" for punctuation
	punct byte
}

var readVerbs = map[string]bool{
	"SELECT": true, "SHOW": true, "DESCRIBE": true, "DESC": true, "EXPLAIN": true,
	"VALUES": true, "TABLE": true, "WITH": true, "PRAGMA": true,
}

// writeWords turn a statement that starts with a read verb into a write
// when they appear anywhere in it: data-modifying CTEs, SELECT INTO,
// EXPLAIN ANALYZE DELETE, SELECT ... FOR UPDATE.
var writeWords = map[string]bool{
	"INSERT": true, "UPDATE": true, "DELETE": true, "MERGE": true, "INTO": true,
	"DROP": true, "ALTER": true, "CREATE": true, "TRUNCATE": true,
	"GRANT": true, "REVOKE": true, "CALL": true, "COPY": true, "LOCK": true,
	"NEXTVAL": true, // advances a sequence, which no rollback undoes
}

var destructiveVerbs = map[string]bool{"DROP": true, "TRUNCATE": true, "ALTER": true}

var txVerbs = map[string]bool{
	"BEGIN": true, "START": true, "COMMIT": true, "ROLLBACK": true, "END": true,
	"ABORT": true, "SAVEPOINT": true, "RELEASE": true, "PREPARE": true,
}

// noTxVerbs cannot run inside a transaction block.
var noTxVerbs = map[string]bool{"VACUUM": true, "REINDEX": true, "CLUSTER": true}

// Classify inspects a single statement.
func Classify(sql string, d *Dialect) Stmt {
	d = d.or()
	st := Stmt{Text: strings.TrimSpace(sql)}
	toks, hint := lex(st.Text, d)
	if len(toks) == 0 || toks[0].word == "" {
		st.Kind, st.Reason = Write, "not a recognised statement"
		return st
	}
	st.Verb = toks[0].word
	has := func(w string) bool {
		for _, t := range toks {
			if t.word == w {
				return true
			}
		}
		return false
	}

	reads := readVerbs
	if d.ReadVerbs != nil {
		reads = d.ReadVerbs
	}
	switch {
	case d.Blocks && isBlockStart(toks):
		// anonymous blocks and stored program units can run anything
		st.Kind, st.Reason = Destructive, "a procedural block can run any code, including DDL and commits that cannot be rolled back"
	case hint:
		// MySQL runs the contents of /*! ... */ comments.
		st.Kind, st.Reason = Write, "contains an executable comment (/*! ... */)"
	case txVerbs[st.Verb]:
		st.Kind, st.Reason = TxControl, "transactions are managed by dbreader"
	case reads[st.Verb]:
		st.Kind = Read
		for _, t := range toks[1:] {
			if writeWords[t.word] {
				st.Kind, st.Reason = Write, st.Verb+" containing "+t.word
				break
			}
		}
		if st.Verb == "PRAGMA" && st.Kind == Read {
			for _, t := range toks {
				if t.punct == '=' || t.punct == '(' {
					st.Kind, st.Reason = Write, "PRAGMA with an argument may change settings or data"
				}
			}
		}
	case destructiveVerbs[st.Verb]:
		st.Kind, st.Reason = Destructive, st.Verb+" changes or removes schema objects"
	case (st.Verb == "DELETE" || st.Verb == "UPDATE") && !has("WHERE"):
		st.Kind, st.Reason = Destructive, st.Verb+" without WHERE affects every row"
	default:
		st.Kind, st.Reason = Write, st.Verb+" statement"
	}

	if st.Kind == Write || st.Kind == Destructive {
		st.NoTx = noTxVerbs[st.Verb] || has("CONCURRENTLY") ||
			(len(toks) > 1 && (st.Verb == "CREATE" || st.Verb == "DROP") &&
				(toks[1].word == "DATABASE" || toks[1].word == "TABLESPACE"))
		if d.ImplicitCommit[st.Verb] {
			st.NoTx = true
		}
	}
	return st
}

// Span is the byte range of one statement in a script, from the end of
// the statement before it to its own terminator (exclusive).
type Span struct{ Start, End int }

// slash marks a line holding only "/", which ends a statement under
// SlashTerminator (SQL*Plus convention) and is the only end of a block.
const slash = 0x1f

// Spans locates the statements of a script: it is cut at top-level
// semicolons, and pieces with nothing but comments are dropped. A block
// (Dialect.Blocks) contains semicolons and runs up to the line with the
// lone slash instead.
func Spans(sql string, d *Dialect) []Span {
	d = d.or()
	var out []Span
	start := 0
	var words []token // the statement so far, for block detection
	add := func(end int) {
		if toks, hint := lex(sql[start:end], d); len(toks) > 0 || hint {
			out = append(out, Span{start, end})
		}
		start, words = end+1, words[:0]
	}
	scan(sql, d, func(i int, word string, punct byte, _ bool) {
		switch {
		case punct == ';':
			if d.Blocks && isBlockStart(words) {
				return
			}
			add(i)
		case punct == slash:
			add(i)
		case word != "" && len(words) < 8:
			words = append(words, token{word: word})
		}
	})
	if start < len(sql) {
		add(len(sql))
	}
	return out
}

// isBlockStart reports whether a statement begins a procedural block or a
// stored program unit, whose semicolons do not end it.
func isBlockStart(w []token) bool {
	return len(w) > 0 && (w[0].word == "BEGIN" || w[0].word == "DECLARE" || isProgramUnit(w))
}

// isProgramUnit matches CREATE [OR REPLACE] [EDITIONABLE] FUNCTION|PROCEDURE|
// PACKAGE|TRIGGER|TYPE ...
func isProgramUnit(w []token) bool {
	if len(w) == 0 || w[0].word != "CREATE" {
		return false
	}
	for _, t := range w[1:] {
		switch t.word {
		case "OR", "REPLACE", "EDITIONABLE", "NONEDITIONABLE":
		case "FUNCTION", "PROCEDURE", "PACKAGE", "TRIGGER", "TYPE", "LIBRARY":
			return true
		default:
			return false
		}
	}
	return false
}

// Split returns the statements of a script, trimmed.
func Split(sql string, d *Dialect) []string {
	var out []string
	for _, sp := range Spans(sql, d) {
		out = append(out, strings.TrimSpace(sql[sp.Start:sp.End]))
	}
	return out
}

func lex(sql string, d *Dialect) (toks []token, hint bool) {
	scan(sql, d, func(_ int, word string, punct byte, h bool) {
		if h {
			hint = true
			return
		}
		toks = append(toks, token{word: word, punct: punct})
	})
	return toks, hint
}

// scan walks sql and reports words and punctuation outside of strings,
// quoted identifiers and comments. hint is set for MySQL executable
// comments, whose content the server runs. d is never nil.
func scan(sql string, d *Dialect, emit func(pos int, word string, punct byte, hint bool)) {
	n := len(sql)
	for i := 0; i < n; {
		c := sql[i]
		switch {
		case c == '-' && i+1 < n && sql[i+1] == '-':
			for i < n && sql[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && sql[i+1] == '*':
			if i+2 < n && sql[i+2] == '!' {
				emit(i, "", 0, true)
			}
			depth := 1
			i += 2
			for i < n && depth > 0 {
				switch {
				case sql[i] == '*' && i+1 < n && sql[i+1] == '/':
					depth--
					i += 2
				case d.NestedComments && sql[i] == '/' && i+1 < n && sql[i+1] == '*':
					depth++
					i += 2
				default:
					i++
				}
			}
		case c == '\'' || c == '"' || c == '`':
			i = skipQuoted(sql, i, d.BackslashEscapes && c != '`')
		case c == '/' && d.SlashTerminator && loneSlash(sql, i):
			emit(i, "", slash, false)
			i++
		case c == '$' && d.DollarQuotes:
			if end, ok := dollarQuote(sql, i); ok {
				i = end
			} else {
				i++
			}
		case isWordStart(rune(c)):
			j := i
			for j < n && isWordPart(rune(sql[j])) {
				j++
			}
			// alternative quoting: q'[ ... ]', nq'{ ... }'
			if w := strings.ToLower(sql[i:j]); d.QQuotes && (w == "q" || w == "nq") && j+1 < n && sql[j] == '\'' {
				i = skipQQuote(sql, j)
				continue
			}
			emit(i, strings.ToUpper(sql[i:j]), 0, false)
			i = j
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		default:
			emit(i, "", c, false)
			i++
		}
	}
}

// skipQuoted returns the index after the quoted run starting at i. A doubled
// quote is an escaped quote; with backslash set, so is \".
func skipQuoted(sql string, i int, backslash bool) int {
	q := sql[i]
	for i++; i < len(sql); i++ {
		switch {
		case backslash && sql[i] == '\\':
			i++
		case sql[i] == q:
			if i+1 < len(sql) && sql[i+1] == q {
				i++
				continue
			}
			return i + 1
		}
	}
	return len(sql)
}

// dollarQuote skips a PostgreSQL $tag$ ... $tag$ string starting at i.
func dollarQuote(sql string, i int) (int, bool) {
	j := i + 1
	for j < len(sql) && (isWordPart(rune(sql[j])) && sql[j] != '$') {
		j++
	}
	if j >= len(sql) || sql[j] != '$' || (j > i+1 && sql[i+1] >= '0' && sql[i+1] <= '9') {
		return 0, false // $1 is a parameter, not a quote
	}
	tag := sql[i : j+1]
	end := strings.Index(sql[j+1:], tag)
	if end < 0 {
		return len(sql), true
	}
	return j + 1 + end + len(tag), true
}

func isWordStart(r rune) bool { return r == '_' || r >= 0x80 || unicode.IsLetter(r) }

func isWordPart(r rune) bool { return isWordStart(r) || (r >= '0' && r <= '9') || r == '$' }

// loneSlash reports whether the "/" at i is alone on its line.
func loneSlash(sql string, i int) bool {
	for j := i - 1; j >= 0 && sql[j] != '\n'; j-- {
		if sql[j] != ' ' && sql[j] != '\t' && sql[j] != '\r' {
			return false
		}
	}
	for j := i + 1; j < len(sql) && sql[j] != '\n'; j++ {
		if sql[j] != ' ' && sql[j] != '\t' && sql[j] != '\r' {
			return false
		}
	}
	return true
}

// skipQQuote returns the index after the q'x ... x' literal whose opening
// quote is at i.
func skipQQuote(sql string, i int) int {
	if i+1 >= len(sql) {
		return len(sql)
	}
	closing := map[byte]byte{'[': ']', '(': ')', '{': '}', '<': '>'}[sql[i+1]]
	if closing == 0 {
		closing = sql[i+1]
	}
	if end := strings.Index(sql[i+2:], string(closing)+"'"); end >= 0 {
		return i + 2 + end + 2
	}
	return len(sql)
}
