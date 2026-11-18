// Package all registers the built-in drivers. A new server is added by
// writing its driver package and importing it here.
package all

import (
	_ "dbreader/internal/driver/mysql"
	_ "dbreader/internal/driver/oracle"
	_ "dbreader/internal/driver/postgres"
	_ "dbreader/internal/driver/sqlite"
)
