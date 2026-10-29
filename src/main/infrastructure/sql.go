package infrastructure

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)


type Where struct {
	conds []string
	Args  []any
}

func (w *Where) Add(cond string, value any) {
	w.Args = append(w.Args, value)
	w.conds = append(w.conds, strings.Replace(cond, "?", "$"+strconv.Itoa(len(w.Args)), 1))
}

func (w *Where) Raw(cond string) {
	w.conds = append(w.conds, cond)
}

func (w *Where) Arg(value any) string {
	w.Args = append(w.Args, value)
	return "$" + strconv.Itoa(len(w.Args))
}

func (w *Where) SQL() string {
	if len(w.conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.conds, " AND ")
}


func ResetIdentity(ctx context.Context, db DBTX, table string) error {
	_, err := db.ExecContext(ctx, fmt.Sprintf(
		`SELECT setval(pg_get_serial_sequence('%[1]s', 'id'), GREATEST((SELECT MAX(id) FROM %[1]s), 1))`, table))
	if err != nil {
		return fmt.Errorf("reset %s identity: %w", table, err)
	}
	return nil
}
