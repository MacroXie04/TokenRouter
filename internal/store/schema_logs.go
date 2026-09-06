package store

import (
	"errors"
	"fmt"
	"gorm.io/gorm"
	"strings"
)

var referenceLogSchemaDefaults = []referenceSchemaDefault{
	{table: "logs", column: "username", wanted: "", literal: "''"},
	{table: "logs", column: "token_name", wanted: "", literal: "''"},
	{table: "logs", column: "model_name", wanted: "", literal: "''"},
	{table: "logs", column: "quota", wanted: "0", literal: "0"},
	{table: "logs", column: "prompt_tokens", wanted: "0", literal: "0"},
	{table: "logs", column: "completion_tokens", wanted: "0", literal: "0"},
	{table: "logs", column: "use_time", wanted: "0", literal: "0"},
	{table: "logs", column: "token_id", wanted: "0", literal: "0"},
	{table: "logs", column: "ip", wanted: "", literal: "''"},
	{table: "logs", column: "request_id", wanted: "", literal: "''"},
	{table: "logs", column: "upstream_request_id", wanted: "", literal: "''"},
}

// prepareReferenceLogSchemaMigration protects the only Log alignment that
// can narrow an existing server column. GORM represents the reference's
// indexed, dialect-native string as varchar(191) on MySQL, while older
// TokenRouter releases declared model_name as varchar(255). PostgreSQL widens
// the same field to text and SQLite does not enforce VARCHAR lengths.
func prepareReferenceLogSchemaMigration(db *gorm.DB) error {
	if db == nil {
		return errors.New("database is nil")
	}
	if db.Dialector == nil || db.Dialector.Name() != "mysql" ||
		!db.Migrator().HasTable(&Log{}) || !db.Migrator().HasColumn(&Log{}, "ModelName") {
		return nil
	}
	columns, err := db.Migrator().ColumnTypes(Log{}.TableName())
	if err != nil {
		return fmt.Errorf("inspect legacy logs schema before alignment: %w", err)
	}
	for _, column := range columns {
		if !strings.EqualFold(column.Name(), "model_name") {
			continue
		}
		if length, known := column.Length(); known && length <= referenceLogMySQLModelNameLimit {
			return nil
		}
		databaseType := strings.ToLower(strings.TrimSpace(column.DatabaseTypeName()))
		switch databaseType {
		case "varchar", "char", "tinytext", "text", "mediumtext", "longtext":
			return rejectOversizedLegacyLogModelName(db)
		default:
			return fmt.Errorf("%w: logs.model_name has unsupported legacy type %q",
				ErrUnsafeReferenceSchemaMigration, databaseType)
		}
	}
	return fmt.Errorf("inspect legacy logs schema before alignment: model_name column is missing")
}

func rejectOversizedLegacyLogModelName(db *gorm.DB) error {
	lengthFunction := "LENGTH"
	if db.Dialector != nil && db.Dialector.Name() == "mysql" {
		lengthFunction = "CHAR_LENGTH"
	}
	rows, err := db.Table(Log{}.TableName()).Select("1").
		Where(lengthFunction+"(model_name) > ?", referenceLogMySQLModelNameLimit).Limit(1).Rows()
	if err != nil {
		return fmt.Errorf("inspect legacy logs.model_name: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("%w: logs.model_name contains a value longer than %d characters",
			ErrUnsafeReferenceSchemaMigration, referenceLogMySQLModelNameLimit)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect legacy logs.model_name: %w", err)
	}
	return nil
}

type referenceLogIndex struct {
	name    string
	columns []string
}

var referenceLogIndexes = []referenceLogIndex{
	{name: "idx_logs_user_id", columns: []string{"user_id"}},
	{name: "idx_user_id_id", columns: []string{"user_id", "id"}},
	{name: "idx_created_at_id", columns: []string{"created_at", "id"}},
	{name: "idx_created_at_type", columns: []string{"created_at", "type"}},
	{name: "idx_logs_username", columns: []string{"username"}},
	{name: "index_username_model_name", columns: []string{"model_name", "username"}},
	{name: "idx_logs_token_name", columns: []string{"token_name"}},
	{name: "idx_logs_model_name", columns: []string{"model_name"}},
	{name: "idx_logs_channel_id", columns: []string{"channel_id"}},
	{name: "idx_logs_token_id", columns: []string{"token_id"}},
	{name: "idx_logs_group", columns: []string{"group"}},
	{name: "idx_logs_ip", columns: []string{"ip"}},
	{name: "idx_logs_request_id", columns: []string{"request_id"}},
	{name: "idx_logs_upstream_request_id", columns: []string{"upstream_request_id"}},
}

// ensureReferenceLogIndexes repairs legacy same-name indexes whose column
// list or order GORM's AutoMigrate does not update. A temporary equivalent
// index stays in place while each ordinary lookup index is replaced, avoiding
// an uncovered query path if creating the final index fails midway.
func ensureReferenceLogIndexes(db *gorm.DB) error {
	if db == nil {
		return errors.New("database is nil")
	}
	if db.Dialector != nil && db.Dialector.Name() == "clickhouse" {
		return nil
	}
	if !db.Migrator().HasTable(&Log{}) {
		return nil
	}
	states, err := inspectReferenceLogIndexes(db)
	if err != nil {
		return err
	}
	for _, wanted := range referenceLogIndexes {
		state, exists := states[strings.ToLower(wanted.name)]
		_, temporaryExists := states[strings.ToLower("tmp_reference_"+wanted.name)]
		if exists && referenceLogIndexColumnsEqual(state.columns, wanted.columns) {
			if state.uniqueKnown && state.unique {
				return fmt.Errorf("%w: logs index %s is unexpectedly unique", ErrUnsafeReferenceSchemaMigration, wanted.name)
			}
			if temporaryExists {
				if err := cleanupReferenceLogTemporaryIndex(db, wanted); err != nil {
					return err
				}
			}
			continue
		}
		if exists && state.uniqueKnown && state.unique {
			return fmt.Errorf("%w: logs index %s is unexpectedly unique", ErrUnsafeReferenceSchemaMigration, wanted.name)
		}
		if !exists {
			if err := db.Migrator().CreateIndex(&Log{}, wanted.name); err != nil {
				return fmt.Errorf("create reference Log index %s: %w", wanted.name, err)
			}
			if err := verifyReferenceLogIndex(db, wanted); err != nil {
				return err
			}
			if temporaryExists {
				if err := cleanupReferenceLogTemporaryIndex(db, wanted); err != nil {
					return err
				}
			}
			continue
		}
		if err := replaceReferenceLogIndex(db, wanted); err != nil {
			return err
		}
	}
	return nil
}

func replaceReferenceLogIndex(db *gorm.DB, wanted referenceLogIndex) error {
	temporaryName := "tmp_reference_" + wanted.name
	temporaryColumns, temporaryExists, temporaryUnique, temporaryUniqueKnown, err := inspectReferenceLogIndex(db, temporaryName)
	if err != nil {
		return err
	}
	if temporaryExists && (!referenceLogIndexColumnsEqual(temporaryColumns, wanted.columns) || (temporaryUniqueKnown && temporaryUnique)) {
		return fmt.Errorf("%w: temporary logs index %s has an unexpected definition",
			ErrUnsafeReferenceSchemaMigration, temporaryName)
	}
	if !temporaryExists {
		quotedColumns := make([]string, 0, len(wanted.columns))
		for _, column := range wanted.columns {
			quotedColumns = append(quotedColumns, quoteReferenceSchemaIdentifier(db, column))
		}
		query := fmt.Sprintf("CREATE INDEX %s ON %s (%s)",
			quoteReferenceSchemaIdentifier(db, temporaryName),
			quoteReferenceSchemaIdentifier(db, Log{}.TableName()),
			strings.Join(quotedColumns, ", "))
		if err := db.Exec(query).Error; err != nil {
			return fmt.Errorf("create temporary Log index %s: %w", temporaryName, err)
		}
	}
	if err := dropIndexPortable(db, &Log{}, wanted.name); err != nil {
		return fmt.Errorf("drop legacy Log index %s: %w", wanted.name, err)
	}
	if err := db.Migrator().CreateIndex(&Log{}, wanted.name); err != nil {
		return fmt.Errorf("create reference Log index %s (temporary index retained): %w", wanted.name, err)
	}
	if err := verifyReferenceLogIndex(db, wanted); err != nil {
		return err
	}
	return cleanupReferenceLogTemporaryIndex(db, wanted)
}

func verifyReferenceLogIndex(db *gorm.DB, wanted referenceLogIndex) error {
	columns, exists, unique, uniqueKnown, err := inspectReferenceLogIndex(db, wanted.name)
	if err != nil {
		return err
	}
	if !exists || !referenceLogIndexColumnsEqual(columns, wanted.columns) || (uniqueKnown && unique) {
		return fmt.Errorf("create reference Log index %s: migrated definition did not match", wanted.name)
	}
	return nil
}

func cleanupReferenceLogTemporaryIndex(db *gorm.DB, wanted referenceLogIndex) error {
	temporaryName := "tmp_reference_" + wanted.name
	columns, exists, unique, uniqueKnown, err := inspectReferenceLogIndex(db, temporaryName)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if !referenceLogIndexColumnsEqual(columns, wanted.columns) || (uniqueKnown && unique) {
		return fmt.Errorf("%w: temporary logs index %s has an unexpected definition",
			ErrUnsafeReferenceSchemaMigration, temporaryName)
	}
	if err := dropIndexPortable(db, &Log{}, temporaryName); err != nil {
		return fmt.Errorf("drop temporary Log index %s: %w", temporaryName, err)
	}
	return nil
}

// dropIndexPortable avoids a PostgreSQL GORM migrator bug that renders a
// function call as an identifier (DROP INDEX "CURRENT_SCHEMA"."name"). An
// unqualified, quoted index name correctly resolves through the active search
// path and works for both the default and operator-selected schemas.
func dropIndexPortable(db *gorm.DB, value any, indexName string) error {
	if db != nil && db.Dialector != nil && db.Dialector.Name() == "postgres" {
		return db.Exec(postgresDropIndexStatement(db, indexName)).Error
	}
	return db.Migrator().DropIndex(value, indexName)
}

func postgresDropIndexStatement(db *gorm.DB, indexName string) string {
	return "DROP INDEX IF EXISTS " + quoteReferenceSchemaIdentifier(db, indexName)
}

type referenceLogIndexState struct {
	columns     []string
	unique      bool
	uniqueKnown bool
}

func inspectReferenceLogIndexes(db *gorm.DB) (map[string]referenceLogIndexState, error) {
	if db != nil && db.Dialector != nil && db.Dialector.Name() == "postgres" {
		return inspectPostgresReferenceIndexes(db, Log{}.TableName())
	}
	indexes, err := db.Migrator().GetIndexes(&Log{})
	if err != nil {
		return nil, fmt.Errorf("inspect Log indexes: %w", err)
	}
	states := make(map[string]referenceLogIndexState, len(indexes))
	for _, index := range indexes {
		unique, uniqueKnown := index.Unique()
		states[strings.ToLower(index.Name())] = referenceLogIndexState{
			columns: index.Columns(), unique: unique, uniqueKnown: uniqueKnown,
		}
	}
	return states, nil
}

// PostgreSQL's GORM GetIndexes query joins pg_attribute through ANY(indkey)
// without ordinality, so composite columns are returned in physical table
// order instead of index order. Query the catalog with ordinality because
// ordering is part of the reference index contract and otherwise causes an
// endless drop/recreate cycle on every startup.
func inspectPostgresReferenceIndexes(db *gorm.DB, table string) (map[string]referenceLogIndexState, error) {
	var rows []struct {
		Name    string `gorm:"column:index_name"`
		Unique  bool   `gorm:"column:is_unique"`
		Columns string `gorm:"column:columns"`
	}
	err := db.Raw(`
		SELECT ci.relname AS index_name,
		       i.indisunique AS is_unique,
		       string_agg(a.attname, ',' ORDER BY keys.ordinality) AS columns
		FROM pg_index AS i
		JOIN pg_class AS ct ON ct.oid = i.indrelid
		JOIN pg_namespace AS ns ON ns.oid = ct.relnamespace
		JOIN pg_class AS ci ON ci.oid = i.indexrelid
		JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS keys(attnum, ordinality) ON true
		JOIN pg_attribute AS a ON a.attrelid = ct.oid AND a.attnum = keys.attnum
		LEFT JOIN pg_constraint AS con ON con.conindid = i.indexrelid
		WHERE ns.nspname = current_schema()
		  AND ct.relkind = 'r'
		  AND ct.relname = ?
		  AND con.oid IS NULL
		GROUP BY ci.relname, i.indisunique`, table).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("inspect PostgreSQL %s indexes: %w", table, err)
	}
	states := make(map[string]referenceLogIndexState, len(rows))
	for _, row := range rows {
		columns := []string{}
		if row.Columns != "" {
			columns = strings.Split(row.Columns, ",")
		}
		states[strings.ToLower(row.Name)] = referenceLogIndexState{
			columns: columns, unique: row.Unique, uniqueKnown: true,
		}
	}
	return states, nil
}

func inspectReferenceLogIndex(db *gorm.DB, name string) (columns []string, exists, unique, uniqueKnown bool, err error) {
	indexes, err := inspectReferenceLogIndexes(db)
	if err != nil {
		return nil, false, false, false, err
	}
	index, exists := indexes[strings.ToLower(name)]
	if !exists {
		return nil, false, false, false, nil
	}
	return index.columns, true, index.unique, index.uniqueKnown, nil
}

func referenceLogIndexColumnsEqual(actual, wanted []string) bool {
	if len(actual) != len(wanted) {
		return false
	}
	for i := range actual {
		if !strings.EqualFold(actual[i], wanted[i]) {
			return false
		}
	}
	return true
}
