# dbreader

Vim-style terminal browser for PostgreSQL, MySQL/MariaDB and SQLite. Sibling of
s3reader and gdocker: same look, same keys, navigation from
[vim-kit](https://github.com/Gandalfalex/vim-kit).

```
make build
./dbreader -init      # writes settings.json from your DBeaver connections
$EDITOR settings.json # set "readOnly": false where writes are wanted
./dbreader            # or: ./dbreader -connection gallery
```

`-init` lists every DBeaver connection with its two flags and nothing else:

```json
{
  "readOnlyDefault": true,
  "profiles": [
    { "name": "gallery", "readOnly": true, "protect": false }
  ]
}
```

- `readOnly: true` refuses every write on that connection; all start that way.
- `protect: true` makes every write ask for the connection name.
- `readOnlyDefault` keeps DBeaver connections that are not listed read-only.
- Host, login and scripts are not copied: they are read from DBeaver on each
  start. Run `-init` again after adding connections in DBeaver; existing
  entries are left alone.

It writes to `-config <path>`, else the settings file already in use, else
`./settings.json`. Without DBeaver, write profiles by hand: see
`settings.example.json`.

## DBeaver link

dbreader reads the DBeaver workspace in place on every start, so there is
nothing to import or keep in sync:

- **Connections** from `data-sources.json` (PostgreSQL, MySQL/MariaDB, SQLite;
  others are skipped and logged). Read-only connections stay read-only, the
  "Production" connection type becomes `protect`.
- **Logins** from `credentials-config.json`, decrypted in memory only.
  With a DBeaver master password set they are not readable; the connections
  still load, without passwords.
- **Scripts**: `S` lists the SQL files linked to the open connection, then
  the unlinked ones. A script opens in a small vim-style editor: `i a o`
  insert with `tab` completion of tables and columns, `x dd yy p u` edit,
  `v`/`V` select. `:run` (or `:exec`) executes the selection, or without one
  the statement under the cursor; the status bar names what would run and
  whether it reads or writes. Writes go through the write guard, one
  statement at a time. `:w` saves; leaving or quitting saves too.
- **New scripts**: `:new <name>` creates `Scripts/<connection>/<name>.sql` in
  the DBeaver project, where DBeaver shows it as an ordinary script.
  dbreader does not edit DBeaver's own metadata; the folder name is what
  links the file to the connection here. Connections from settings.json
  keep theirs in `~/.local/share/dbreader/scripts/<connection>/`.

The default location (`~/Library/DBeaverData`, `~/.local/share/DBeaverData`)
is found automatically. Otherwise point at it with `-dbeaver <dir>` or
`"dbeaver": "<dir>"` in settings.json (data directory, workspace or project);
`"off"` disables the link. settings.json is optional once DBeaver is linked.
Connections through an SSH tunnel are listed but cannot be opened yet.

## Keys

| Where | Keys |
|---|---|
| everywhere | `j/k gg/G ^d/^u` move · `:` command line · `s` sql prompt (`tab` completes tables, columns, `alias.`) · `S` scripts · `e` statement in `$EDITOR` · `E` error log · `:help` |
| lists | `l`/`enter` open · `h` back · `/` filter · `i` table structure · `r` reload |
| data grid | `h/l 0/$` columns · `enter` row as record · `o` sort by column · `/` filter rows · `y`/`Y` yank cell/row · `-` back |

Commands: `:connect`, `:schema`, `:table`, `:where <cond>`, `:order <col> [desc]`,
`:limit <n>`, `:sql <statement>`, `:edit`, `:structure`.

## Search and yank

| Where | Key | Finds |
|---|---|---|
| table list | `/` | tables by name or by column name |
| table list | `?` / `:grep <text>` | rows containing the text in every table of the schema; lists tables with match counts, `enter` shows the rows |
| table | `?` | rows of the whole table where any column contains the text (on the server) |
| table, result | `/` | among the loaded rows |
| script list | `/` | scripts by name or content; opening jumps to the match |
| script | `/`, `n`, `N` | text in the file |

Yank from a result: `y` cell, `Y` row, `V` + move + `y` several rows with a
header (tab-separated), `:yank csv|json|markdown|tsv` the selected or all
loaded rows. In the record view `y` yanks the row as JSON.

## The write guard

Reading never asks. Anything else passes three gates:

1. **Classification** (`internal/sqlsafe`). Only statements recognised as
   reads run directly; unknown statements count as writes.
2. **Server enforcement.** Reads run in a `READ ONLY` transaction
   (`PRAGMA query_only` on SQLite), so a misjudged statement is refused by
   the database rather than executed.
3. **Typed confirmation, then commit.** A write shows the statement and the
   connection it will hit, and runs only after you type `yes`. Destructive
   statements (`DROP`, `TRUNCATE`, `ALTER`, `UPDATE`/`DELETE` without `WHERE`)
   and every write on a `"protect": true` profile need the connection name
   instead. The statement then runs in a transaction and stops again with
   the affected row count: `c` commits, `r` rolls back, nothing else works
   until you choose. Quitting rolls back.

Statements the server cannot roll back (`VACUUM`, `CREATE DATABASE`, any DDL
on MySQL) say so on the confirmation screen. `"readOnly": true` profiles
refuse writes outright.

## Settings

See `settings.example.json`. Per profile: `driver` (`postgres`, `mysql`,
`sqlite`), either `url` or `host`/`port`/`user`/`password`/`database`
(`${ENV_VAR}` is expanded), `path` for SQLite, and optionally `schema`,
`sslMode`, `limit`, `readOnly`, `protect`.

## Tests

```
make test
# against real servers:
DBREADER_TEST_POSTGRES='postgres://postgres:test@localhost:5432/postgres?sslmode=disable' go test ./...
DBREADER_TEST_MYSQL='root:test@tcp(localhost:3306)/mysql' go test ./internal/db
```
