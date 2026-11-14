// schema_sql_test.go —— schema.sql 与 AutoMigrate 模型清单防漂移门禁（Phase1⑤）：
// deploy/migrations/schema.sql 是 MySQL 初始化参考（ops-runbook 灌库入口），
// 表集合必须与 autoMigrateModels() 双向相等——新增表只登记 GORM 而漏写
// schema.sql（或反之）在此红。

package storage

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"

	"gorm.io/gorm/schema"
)

// tabler GORM 可选 TableName() 接口（有手写表名的模型优先手写名）。
type tabler interface{ TableName() string }

func TestSchemaSQLMatchesAutoMigrate(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "migrations", "schema.sql"))
	if err != nil {
		t.Fatalf("读 schema.sql 失败: %v", err)
	}
	// 解析 CREATE TABLE IF NOT EXISTS `name` 清单。
	re := regexp.MustCompile("CREATE TABLE IF NOT EXISTS `([a-z0-9_]+)`")
	inSQL := map[string]bool{}
	for _, m := range re.FindAllSubmatch(raw, -1) {
		inSQL[string(m[1])] = true
	}
	// AutoMigrate 模型清单（有 TableName() 用手写名，否则 GORM 命名策略）。
	ns := schema.NamingStrategy{}
	inModels := map[string]bool{}
	for _, m := range autoMigrateModels() {
		name := ""
		if tn, ok := m.(tabler); ok {
			name = tn.TableName()
		} else {
			name = ns.TableName(reflect.TypeOf(m).Elem().Name())
		}
		inModels[name] = true
	}
	for name := range inModels {
		if !inSQL[name] {
			t.Errorf("schema.sql 缺表 %q（autoMigrateModels 已登记）", name)
		}
	}
	for name := range inSQL {
		if !inModels[name] {
			t.Errorf("schema.sql 多表 %q（autoMigrateModels 无对应模型）", name)
		}
	}
	if len(inSQL) != len(inModels) {
		t.Fatalf("表数不一致: sql=%d models=%d", len(inSQL), len(inModels))
	}
}
