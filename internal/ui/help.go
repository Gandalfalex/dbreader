package ui

import "sync"

// helpPage is built once: static key help plus the generated command list.
var helpPage = sync.OnceValue(func() string {
	return helpText + "\n  COMMANDS (:, tab completes, ↑↓ history, unique prefixes work: :conn, :str)\n" +
		commandHelp() + "    :42                       jump to line 42\n"
})

const helpText = `
  MOTIONS (all accept a count, e.g. 5j, 10G)
    j / k  ↓ / ↑        move down / up
    gg / G              first / last  (3gg, 42G jump to line)
    ctrl+d / ctrl+u     half page down / up
    ctrl+f / ctrl+b     full page down / up
    ctrl+e / ctrl+y     scroll view without moving
    H / M / L           top / middle / bottom of screen
    zz / zt / zb        center / top / bottom cursor line

  NAVIGATION
    l / enter / →       connect, open schema, open table
    h / - / ← / bksp    back
    i / gi              table structure
    * (schema list)     pin the schema: listed first, never hidden, remembered
    zh (schema list)    show / hide system schemas and those without tables
    r                   reload
    q                   quit (press twice below the connection list; back, in data and pager views)
    ctrl+c              quit anywhere (an open transaction is rolled back)
    esc                 clear filter → cancel running statement → back

  DATA GRID
    h / l  ← / →        previous / next column (w, b too; counts work: 5l)
    0 / $               first / last column
    enter               show the row as a record
    o                   sort by the column under the cursor: asc → desc → off
                        (tables are re-read with ORDER BY, query results sorted in place)
    /                   filter loaded rows, any column (enter keeps, esc reverts)
    ?                   search the whole table on the server: rows where any
                        column contains the text (empty clears)
    :where <cond>       filter a table on the server with your own condition
    y / Y               yank cell / whole row (tab-separated; JSON from the record view)
    V                   select rows (move to extend); y yanks them with a header
    :yank csv|json|markdown|tsv
                        yank the selected rows, or all loaded ones, in that format
    - / bksp / q        back

  SEARCH
    /   (table list)    filter by table name or column name
    ?   (table list)    rows containing a text, in every table of the schema;
                        lists the tables with matches, enter shows the rows
    /   (script list)   filter by file name or file content; opening a script
                        found by content jumps to the match
    /   (script)        search in the file, n / N next / previous match
    :grep <text>        same as ?

  SQL
    s                   sql prompt: one statement, ↑↓ recalls earlier ones
    tab / shift+tab     (sql prompt) complete tables, columns and keywords;
                        "alias." or "table." lists that table's columns
    S                   sql scripts linked to the connection in DBeaver

  SCRIPT (opened from S, or started with :new <name>)
    e                   edit the file in $EDITOR (nvim, vim: back here on the
                        line you left); also e on a script in the list.
                        Tables and columns complete there too: neovim gets
                        a language server, vim an omnifunc (ctrl-x ctrl-o)
    enter               run the selection; without one, the statement the
                        cursor is in (same as :run)
    Small edits also work here, like in vim:
    h l w b 0 $         move within the line, j k gg G ... between lines
    i a I A o O         insert mode; esc leaves it
    tab / shift+tab     (insert) complete tables, columns, keywords;
                        without candidates tab indents
    x  dd  D  J         delete character / line / to end of line, join lines
    cc  C               change line / to end of line
    yy  p  P            yank line, paste after / before (counts work: 3dd)
    u / ctrl+r          undo / redo
    v / V               select characters / lines; then y d c, esc clears
    :run  (:exec)       run the selection; without one, the statement the
                        cursor is in (marked │). The status bar says what
                        would run. Several statements run in order, each
                        write is confirmed and committed on its own, and the
                        run stops at the first error, refusal or rollback
    :w                  save. Leaving the script or quitting saves too, so
                        text is never lost; :e! drops unsaved changes
    :w!                 save over a file that changed on disk meanwhile
    :new <name>         new script for this connection

  THE WRITE GUARD
    Only statements that read (SELECT, SHOW, EXPLAIN, WITH ... SELECT, ...) run
    straight away, and they run in a read-only transaction: if dbreader
    misjudges a statement, the server refuses it.

    Everything else stops at a confirmation screen showing the statement and
    the connection it will hit:
      INSERT, UPDATE/DELETE with WHERE, ...   type  yes
      DROP, TRUNCATE, ALTER,
      UPDATE/DELETE without WHERE             type the connection name
      any write on a "protect": true profile  type the connection name

    A confirmed write runs inside a transaction and stops again: you see how
    many rows it touched, then press c to commit or r to roll back. Nothing
    else can be done until you decide. Statements the server cannot roll back
    (VACUUM, CREATE DATABASE, DDL on MySQL) say so before you confirm.

    Profiles with "readOnly": true refuse every write; so do DBeaver
    connections marked read-only. DBeaver's "Production" type counts as protect. BEGIN/COMMIT/ROLLBACK
    typed by hand are refused: the guard owns the transaction.
`
