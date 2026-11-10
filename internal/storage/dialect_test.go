package storage

import (
	"errors"
	"fmt"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestIsDuplicateKeyErr 钉三方言唯一冲突判定（v6.2.0）：pg 23505 此前缺失——
// 生产方言（v5.4.8 起）撞唯一键被当普通错误上抛，幂等命中误报失败。
func TestIsDuplicateKeyErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"普通错误", errors.New("connection refused"), false},
		{"mysql 1062", &mysqldriver.MySQLError{Number: 1062, Message: "Duplicate entry"}, true},
		{"mysql 非 1062", &mysqldriver.MySQLError{Number: 1062 + 1}, false},
		{"pg 23505", &pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint"}, true},
		{"pg 非 23505", &pgconn.PgError{Code: "23503"}, false},
		{"sqlite 文案", errors.New(`UNIQUE constraint failed: tasks.event_id`), true},
		{"pg 文案兜底（类型被中间层抹掉）", fmt.Errorf("wrapped: %w", errors.New("ERROR: duplicate key value violates unique constraint \"tasks_event_id_key\"")), true},
		{"包装的 mysql 1062", fmt.Errorf("tx: %w", &mysqldriver.MySQLError{Number: 1062}), true},
	}
	for _, c := range cases {
		if got := isDuplicateKeyErr(c.err); got != c.want {
			t.Errorf("isDuplicateKeyErr(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}
