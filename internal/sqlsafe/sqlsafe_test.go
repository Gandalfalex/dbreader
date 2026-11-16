package sqlsafe

import (
	"reflect"
	"testing"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		sql  string
		d    Dialect
		want Kind
	}{
		{"select * from users", Postgres, Read},
		{"  SELECT 1 -- delete from users", Postgres, Read},
		{"select 'drop table x', \"update\" from t", Postgres, Read},
		{"select /* delete */ 1", Postgres, Read},
		{"select /* a /* nested */ delete */ 1", Postgres, Read},
		{"select $$ delete from t $$", Postgres, Read},
		{"select $tag$ drop $tag$, $1", Postgres, Read},
		{"select `delete` from t", MySQL, Read},
		{`select 'it\'s; delete from t'`, MySQL, Read},
		{"show tables", MySQL, Read},
		{"explain select 1", Postgres, Read},
		{"explain analyze select 1", Postgres, Read},
		{"with a as (select 1) select * from a", Postgres, Read},
		{"values (1), (2)", Postgres, Read},
		{"pragma table_info", SQLite, Read},
		{"select replace(name, 'a', 'b') from t", MySQL, Read},

		{"insert into t values (1)", Postgres, Write},
		{"update t set a = 1 where id = 2", Postgres, Write},
		{"delete from t where id = 2", Postgres, Write},
		{"with d as (delete from t where id = 1 returning *) select * from d", Postgres, Write},
		{"select * into backup from t", Postgres, Write},
		{"select * from t for update", Postgres, Write},
		{"explain analyze delete from t where id = 1", Postgres, Write},
		{"create table t (a int)", Postgres, Write},
		{"replace into t values (1)", MySQL, Write},
		{"call cleanup()", MySQL, Write},
		{"select 1 /*! ; delete from t */", MySQL, Write},
		{"pragma journal_mode = wal", SQLite, Write},
		{"pragma wal_checkpoint(full)", SQLite, Write},
		{"grant all on t to bob", Postgres, Write},
		{"set search_path = x", Postgres, Write},
		{"vacuum", Postgres, Write},
		{"frobnicate everything", Postgres, Write},
		{"(((", Postgres, Write},
		{"", Postgres, Write},

		{"delete from t", Postgres, Destructive},
		{"update t set a = 1", Postgres, Destructive},
		{"update t set a = 'where'", Postgres, Destructive},
		{"drop table t", Postgres, Destructive},
		{"truncate t", Postgres, Destructive},
		{"alter table t drop column a", Postgres, Destructive},

		{"begin", Postgres, TxControl},
		{"commit", Postgres, TxControl},
		{"rollback", Postgres, TxControl},
		{"start transaction", MySQL, TxControl},
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
		d    Dialect
		want bool
	}{
		{"vacuum full", Postgres, true},
		{"create index concurrently i on t (a)", Postgres, true},
		{"create database x", Postgres, true},
		{"drop table t", Postgres, false},
		{"drop table t", MySQL, true},
		{"update t set a = 1 where b = 2", MySQL, false},
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
		if got := Split(tt.sql, Postgres); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Split(%q) = %q, want %q", tt.sql, got, tt.want)
		}
	}
}

func TestSpans(t *testing.T) {
	sql := "-- a\nselect 1;\n\n-- only a comment\n;\ndelete from t\n"
	spans := Spans(sql, Postgres)
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
