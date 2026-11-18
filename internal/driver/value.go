package driver

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Value formats a scanned value of a column of type typ: d.Format first,
// then the forms common to all servers.
func Value(d Driver, v any, typ string) Cell {
	if c, ok := d.Format(v, typ); ok {
		return c
	}
	switch v := v.(type) {
	case nil:
		return Cell{Null: true}
	case []byte:
		if utf8.Valid(v) {
			return Cell{S: string(v)}
		}
		const maxHex = 64
		if len(v) > maxHex {
			return Cell{S: fmt.Sprintf("\\x%s… (%d bytes)", hex.EncodeToString(v[:maxHex]), len(v))}
		}
		return Cell{S: "\\x" + hex.EncodeToString(v)}
	case string:
		return Cell{S: v}
	case time.Time:
		switch {
		case typ == "DATE":
			return Cell{S: v.Format("2006-01-02")}
		case strings.Contains(typ, "TZ") || strings.Contains(typ, "TIME ZONE"):
			return Cell{S: v.Format("2006-01-02 15:04:05.999999 Z07:00")}
		}
		return Cell{S: v.Format("2006-01-02 15:04:05.999999")}
	case float64:
		return Cell{S: strconv.FormatFloat(v, 'g', -1, 64)}
	case float32:
		return Cell{S: strconv.FormatFloat(float64(v), 'g', -1, 32)}
	}
	return Cell{S: fmt.Sprint(v)}
}
