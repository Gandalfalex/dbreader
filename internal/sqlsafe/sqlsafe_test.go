package sqlsafe_test

import (
	. "dbreader/internal/sqlsafe"

	"dbreader/internal/driver/mysql"
	"dbreader/internal/driver/oracle"
	"dbreader/internal/driver/postgres"
	"dbreader/internal/driver/sqlite"
	"reflect"
	"testing"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		sql  string
		d    *Dialect
		want Kind
	}{
		{"select * from users", postgres.SQL, Read},
		{"  SELECT 1 -- delete from users", postgres.SQL, Read},
		{"select 'drop table x', \"update\" from t", postgres.SQL, Read},
		{"select /* delete */ 1", postgres.SQL, Read},
		{"select /* a /* nested */ delete */ 1", postgres.SQL, Read},
		{"select $$ delete from t $$", postgres.SQL, Read},
		{"select $tag$ drop $tag$, $1", postgres.SQL, Read},
		{"select `delete` from t", mysql.SQL, Read},
		{`select 'it\'s; delete from t'`, mysql.SQL, Read},
		{"show tables", mysql.SQL, Read},
		{"explain select 1", postgres.SQL, Read},
		{"explain analyze select 1", postgres.SQL, Read},
		{"with a as (select 1) select * from a", postgres.SQL, Read},
		{"values (1), (2)", postgres.SQL, Read},
		{"pragma table_info", sqlite.SQL, Read},
		{"select replace(name, 'a', 'b') from t", mysql.SQL, Read},

		{"insert into t values (1)", postgres.SQL, Write},
		{"update t set a = 1 where id = 2", postgres.SQL, Write},
		{"delete from t where id = 2", postgres.SQL, Write},
		{"with d as (delete from t where id = 1 returning *) select * from d", postgres.SQL, Write},
		{"select * into backup from t", postgres.SQL, Write},
		{"select * from t for update", postgres.SQL, Write},
		{"explain analyze delete from t where id = 1", postgres.SQL, Write},
		{"create table t (a int)", postgres.SQL, Write},
		{"replace into t values (1)", mysql.SQL, Write},
		{"call cleanup()", mysql.SQL, Write},
		{"select 1 /*! ; delete from t */", mysql.SQL, Write},
		{"pragma journal_mode = wal", sqlite.SQL, Write},
		{"pragma wal_checkpoint(full)", sqlite.SQL, Write},
		{"grant all on t to bob", postgres.SQL, Write},
		{"set search_path = x", postgres.SQL, Write},
		{"vacuum", postgres.SQL, Write},
		{"frobnicate everything", postgres.SQL, Write},
		{"(((", postgres.SQL, Write},
		{"", postgres.SQL, Write},

		{"delete from t", postgres.SQL, Destructive},
		{"update t set a = 1", postgres.SQL, Destructive},
		{"update t set a = 'where'", postgres.SQL, Destructive},
		{"drop table t", postgres.SQL, Destructive},
		{"truncate t", postgres.SQL, Destructive},
		{"alter table t drop column a", postgres.SQL, Destructive},

		{"begin", postgres.SQL, TxControl},
		{"commit", postgres.SQL, TxControl},
		{"rollback", postgres.SQL, TxControl},
		{"start transaction", mysql.SQL, TxControl},
	}
	for _, tt := range tests {
		if got := Classify(tt.sql, tt.d); got.Kind != tt.want {
			t.Errorf("Classify(%q) = %v (%s), want %v", tt.sql, got.Kind, got.Reason, tt.want)
		}
	}
}

func TestClassifyNoTx(t *testing.T) {
	tests := []struct {
		sql  string
		d    *Dialect
		want bool
	}{
		{"vacuum full", postgres.SQL, true},
		{"create index concurrently i on t (a)", postgres.SQL, true},
		{"create database x", postgres.SQL, true},
		{"drop table t", postgres.SQL, false},
		{"drop table t", mysql.SQL, true},
		{"update t set a = 1 where b = 2", mysql.SQL, false},
	}
	for _, tt := range tests {
		if got := Classify(tt.sql, tt.d).NoTx; got != tt.want {
			t.Errorf("Classify(%q).NoTx = %v, want %v", tt.sql, got, tt.want)
		}
	}
}

func TestSplit(t *testing.T) {
	tests := []struct {
		sql  string
		want []string
	}{
		{"select 1", []string{"select 1"}},
		{"select 1;", []string{"select 1"}},
		{"select 1; ; -- done\n", []string{"select 1"}},
		{"select ';'; delete from t", []string{"select ';'", "delete from t"}},
		{"select $$ a; b $$; select 2", []string{"select $$ a; b $$", "select 2"}},
		{"", nil},
	}
	for _, tt := range tests {
		if got := Split(tt.sql, postgres.SQL); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Split(%q) = %q, want %q", tt.sql, got, tt.want)
		}
	}
}

func TestSpans(t *testing.T) {
	sql := "-- a\nselect 1;\n\n-- only a comment\n;\ndelete from t\n"
	spans := Spans(sql, postgres.SQL)
	if len(spans) != 2 {
		t.Fatalf("spans = %v", spans)
	}
	if got := sql[spans[0].Start:spans[0].End]; got != "-- a\nselect 1" {
		t.Errorf("first = %q", got)
	}
	if got := sql[spans[1].Start:spans[1].End]; got != "\ndelete from t\n" {
		t.Errorf("second = %q", got)
	}
}

func TestClassifyOracle(t *testing.T) {
	tests := []struct {
		sql  string
		want Kind
		noTx bool
	}{
		{"select * from dual", Read, false},
		{"SELECT name FROM customers WHERE id = :1", Read, false},
		{"with a as (select 1 x from dual) select * from a", Read, false},
		{"select 'drop table x' from dual", Read, false},
		{"select q'[it's; a delete from t]' from dual", Read, false},
		{"select nq'{drop}' from dual", Read, false},
		{"select \"DELETE\" from t", Read, false},
		{"select * from t for update", Write, false},
		{"select seq.nextval from dual", Write, false},
		{"select * into x from t", Write, false},
		{"explain plan for select 1 from dual", Write, false}, // writes the plan table
		{"describe customers", Write, false},                  // SQL*Plus, not SQL
		{"insert into t values (1)", Write, false},
		{"merge into t using s on (1=1) when matched then update set a = 1", Write, false},
		{"update t set a = 1 where id = 1", Write, false},
		{"update t set a = 1", Destructive, false},
		{"delete from t", Destructive, false},
		{"create table t (a number)", Write, true},
		{"create index i on t (a)", Write, true},
		{"drop table t", Destructive, true},
		{"truncate table t", Destructive, true},
		{"alter table t add b number", Destructive, true},
		{"grant select on t to u", Write, true},
		{"comment on table t is 'x'", Write, true},
		{"begin delete from t; end;", Destructive, false},
		{"BEGIN\n  NULL;\nEND;", Destructive, false},
		{"declare x number; begin x := 1; end;", Destructive, false},
		{"create or replace procedure p is begin null; end;", Destructive, true},
		{"create or replace editionable package body pk as end;", Destructive, true},
		{"create trigger tr before insert on t begin null; end;", Destructive, true},
		{"call do_it()", Write, false},
		{"commit", TxControl, false},
		{"rollback", TxControl, false},
		{"savepoint a", TxControl, false},
	}
	for _, tt := range tests {
		got := Classify(tt.sql, oracle.SQL)
		if got.Kind != tt.want || got.NoTx != tt.noTx {
			t.Errorf("Classify(%q) = %v noTx=%v (%s), want %v noTx=%v", tt.sql, got.Kind, got.NoTx, got.Reason, tt.want, tt.noTx)
		}
	}
}

func TestSplitOracle(t *testing.T) {
	block := "begin\n  insert into t values (1);\n  delete from t;\nend;"
	proc := "create or replace procedure p is\nbegin\n  null;\nend;"
	tests := []struct {
		name string
		sql  string
		want []string
	}{
		{"plain", "select 1 from dual;\nselect 2 from dual;", []string{"select 1 from dual", "select 2 from dual"}},
		{"block runs to the slash", block + "\n/\nselect 1 from dual;", []string{block, "select 1 from dual"}},
		{"procedure runs to the slash", proc + "\n/\n", []string{proc}},
		{"block without slash is one statement", block, []string{block}},
		{"slash after plain sql", "select 1 from dual\n/\nselect 2 from dual\n/\n", []string{"select 1 from dual", "select 2 from dual"}},
		{"slash with white space around", "select 1 from dual\n  /  \n", []string{"select 1 from dual"}},
		{"division is not a terminator", "select 4\n/ 2 from dual;", []string{"select 4\n/ 2 from dual"}},
		{"q-quote hides semicolons", "select q'[a; b]' from dual; select 2 from dual", []string{"select q'[a; b]' from dual", "select 2 from dual"}},
		{"q-quote with other delimiters", "select q'!a;'b!' from dual;select Q'(x;)' from dual", []string{"select q'!a;'b!' from dual", "select Q'(x;)' from dual"}},
		{"string with doubled quote", "select 'it''s; fine' from dual;", []string{"select 'it''s; fine' from dual"}},
		{"slash in a comment", "select 1 from dual -- ok\n-- /\n;", []string{"select 1 from dual -- ok\n-- /"}},
	}
	for _, tt := range tests {
		if got := Split(tt.sql, oracle.SQL); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s:\n got %q\nwant %q", tt.name, got, tt.want)
		}
	}
	// other dialects are unaffected: begin is a transaction, / is division
	if got := Split("begin; select 4\n/ 2;", postgres.SQL); len(got) != 2 {
		t.Errorf("postgres split = %q", got)
	}
}
