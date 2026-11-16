package ui

import (
	"strings"
	"testing"
)

var testCatalog = sqlCatalog{
	tables:  []string{"orders", "users", "UserRoles"},
	schemas: []string{"public", "audit"},
	columns: map[string][]string{
		"users":     {"id", "name", "email", "created at"},
		"orders":    {"id", "user_id", "total"},
		"UserRoles": {"user_id", "role"},
	},
	quote:   func(s string) string { return `"` + s + `"` },
	foldsUp: true,
}

// complete runs completion with the cursor at "|" and returns the labels
// and the line the first candidate produces.
func complete(t *testing.T, input string) (labels []string, first string) {
	t.Helper()
	cursor := len([]rune(input[:strings.Index(input, "|")]))
	items, tail := completeSQL(strings.Replace(input, "|", "", 1), cursor, testCatalog)
	for _, it := range items {
		labels = append(labels, it.Label)
	}
	if len(items) > 0 {
		first = strings.TrimSuffix(items[0].Value, tail) + "|" + tail
	}
	return labels, first
}

func TestCompleteSQL(t *testing.T) {
	tests := []struct {
		in     string
		labels string // space separated, in order
		first  string
	}{
		{"select * from us|", "users UserRoles", "select * from users|"},
		{"select * from |", "orders users UserRoles public audit", "select * from orders|"},
		{"select * from User|", "users UserRoles", ""},
		{"select * from userr|", "UserRoles", `select * from "UserRoles"|`},
		{"select * from users u join o|", "orders", "select * from users u join orders|"},
		{"select u.| from users u", "id name email created at", "select u.id| from users u"},
		{"select u.na| from users as u", "name", "select u.name| from users as u"},
		{"select users.cr| from users", "created at", `select users."created at"| from users`},
		{"select o.| from users u, orders o", "id user_id total", ""},
		{"select x.| from users u", "", ""},
		{"select na| from users", "name", "select name| from users"},
		{"select * from users where em|", "email", ""},
		{"select * from public.users where to|", "", ""},
		{"select * from orders where to|", "total", ""},
		{"select tot|", "total", "select tot|"[:0] + "select total|"},
		{"sel|", "select", "select|"},
		{"SEL|", "SELECT", "SELECT|"},
		{"select * from users |", "", ""},
		{"update us| set", "users UserRoles", "update users| set"},
		{"select * from users where id|", "", ""}, // already complete
	}
	for _, tt := range tests {
		labels, first := complete(t, tt.in)
		if got := strings.Join(labels, " "); got != tt.labels {
			t.Errorf("%q: candidates = %q, want %q", tt.in, got, tt.labels)
		}
		if tt.first != "" && first != tt.first {
			t.Errorf("%q: first = %q, want %q", tt.in, first, tt.first)
		}
	}
}
