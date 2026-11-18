package oracle

import "dbreader/internal/driver"

var catalog = driver.Catalog{
	Schemas:    schemas(`OR u.oracle_maintained = 'Y'`),
	SchemasOld: schemas(""), // oracle_maintained is new in 12c
	Current:    `SELECT sys_context('USERENV', 'CURRENT_SCHEMA') FROM dual`,
	Tables: `SELECT o.object_name, lower(o.object_type), nvl(t.num_rows, -1)
			FROM all_objects o
			LEFT JOIN all_tables t ON t.owner = o.owner AND t.table_name = o.object_name
			WHERE o.owner = :1 AND o.object_name NOT LIKE 'BIN$%'
				AND (o.object_type IN ('TABLE', 'VIEW', 'MATERIALIZED VIEW')
					OR (o.object_type = 'SYNONYM' AND EXISTS (
						SELECT 1 FROM all_synonyms s
						JOIN all_objects x ON x.owner = s.table_owner AND x.object_name = s.table_name
						WHERE s.owner = o.owner AND s.synonym_name = o.object_name
							AND x.object_type IN ('TABLE', 'VIEW', 'MATERIALIZED VIEW'))))
			ORDER BY o.object_name`,
	// a synonym shows the columns of the table it stands for
	Columns: `WITH a AS (SELECT CAST(:1 AS VARCHAR2(128)) o, CAST(:2 AS VARCHAR2(128)) t FROM dual),
			r AS (SELECT o, t FROM a UNION ALL
				SELECT s.table_owner, s.table_name FROM all_synonyms s JOIN a ON s.owner = a.o AND s.synonym_name = a.t)
			SELECT c.column_name,
				c.data_type || CASE
					WHEN c.data_type LIKE '%CHAR%' AND c.char_length > 0 THEN '(' || c.char_length || ')'
					WHEN c.data_type = 'NUMBER' AND c.data_precision IS NOT NULL THEN
						'(' || c.data_precision || CASE WHEN nvl(c.data_scale, 0) > 0 THEN ',' || c.data_scale END || ')'
				END,
				CASE c.nullable WHEN 'Y' THEN 1 ELSE 0 END,
				c.data_default,
				CASE WHEN EXISTS (
					SELECT 1 FROM all_constraints k
					JOIN all_cons_columns cc ON cc.owner = k.owner AND cc.constraint_name = k.constraint_name
					WHERE k.constraint_type = 'P' AND k.owner = c.owner AND k.table_name = c.table_name
						AND cc.column_name = c.column_name) THEN 1 ELSE 0 END
			FROM all_tab_columns c JOIN r ON c.owner = r.o AND c.table_name = r.t ORDER BY c.column_id`,
	ColNames: colNames(""),
	SearchNames: colNames(`AND c.data_type_owner IS NULL
		AND c.data_type NOT IN ('BLOB', 'RAW', 'LONG RAW', 'LONG', 'BFILE', 'XMLTYPE', 'ANYDATA', 'ROWID', 'UROWID')`),
}

// colNames lists the columns of a schema's tables, views and
// synonyms (under the synonym's name), restricted by cond.
func colNames(cond string) string {
	return `WITH a AS (SELECT CAST(:1 AS VARCHAR2(128)) o FROM dual)
		SELECT name, column_name FROM (
			SELECT c.table_name name, c.column_name, c.column_id
			FROM all_tab_columns c JOIN a ON c.owner = a.o
			WHERE c.table_name NOT LIKE 'BIN$%' ` + cond + `
			UNION ALL
			SELECT s.synonym_name, c.column_name, c.column_id
			FROM all_synonyms s JOIN a ON s.owner = a.o
			JOIN all_tab_columns c ON c.owner = s.table_owner AND c.table_name = s.table_name
			WHERE 1 = 1 ` + cond + `)
		ORDER BY name, column_id`
}

// schemas lists the users with their tables, views and synonyms;
// system is a further condition for schemas Oracle itself owns.
func schemas(system string) string {
	return `SELECT u.username, nvl(o.n, 0),
			CASE WHEN u.username IN (` + systemUsers + `) ` + system + ` THEN 1 ELSE 0 END sys
		FROM all_users u LEFT JOIN (
			SELECT owner, count(*) n FROM all_objects
			WHERE object_type IN ('TABLE', 'VIEW', 'MATERIALIZED VIEW', 'SYNONYM') AND object_name NOT LIKE 'BIN$%'
			GROUP BY owner) o ON o.owner = u.username
		ORDER BY sys, u.username`
}

// systemUsers lists the schemas Oracle itself owns, which sort last.
const systemUsers = `'SYS', 'SYSTEM', 'OUTLN', 'DBSNMP', 'XDB', 'CTXSYS', 'MDSYS', 'ORDSYS', 'ORDDATA', 'ORDPLUGINS',
	'WMSYS', 'APPQOSSYS', 'AUDSYS', 'GSMADMIN_INTERNAL', 'LBACSYS', 'OJVMSYS', 'DVSYS', 'DVF', 'OLAPSYS',
	'REMOTE_SCHEDULER_AGENT', 'SI_INFORMTN_SCHEMA', 'ANONYMOUS', 'XS$NULL', 'DIP', 'SYSBACKUP', 'SYSDG',
	'SYSKM', 'SYSRAC', 'GGSYS', 'DBSFWUSER', 'PDBADMIN', 'EXFSYS', 'SPATIAL_CSW_ADMIN_USR', 'SPATIAL_WFS_ADMIN_USR'`
