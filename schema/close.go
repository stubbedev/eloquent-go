package schema

import "database/sql"

// closeRows releases a read query's rows; the error is irrelevant after a
// completed read, unlike writes whose results are checked.
func closeRows(rows *sql.Rows) {
	_ = rows.Close()
}
