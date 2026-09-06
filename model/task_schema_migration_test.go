package model

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// referenceTaskSchema independently transcribes the pinned reference Task
// declaration. Its custom string aliases and JSON value types are reproduced
// locally so this test does not depend on the reference repository at runtime.
type referenceTaskSchema struct {
	ID          int64 `gorm:"primary_key;AUTO_INCREMENT"`
	CreatedAt   int64 `gorm:"index"`
	UpdatedAt   int64
	TaskID      string                `gorm:"type:varchar(191);index"`
	Platform    referenceTaskPlatform `gorm:"type:varchar(30);index"`
	UserId      int                   `gorm:"index"`
	Group       string                `gorm:"type:varchar(50)"`
	ChannelId   int                   `gorm:"index"`
	Quota       int
	Action      string              `gorm:"type:varchar(40);index"`
	Status      referenceTaskStatus `gorm:"type:varchar(20);index"`
	FailReason  string
	SubmitTime  int64                    `gorm:"index"`
	StartTime   int64                    `gorm:"index"`
	FinishTime  int64                    `gorm:"index"`
	Progress    string                   `gorm:"type:varchar(20);index"`
	Properties  referenceTaskProperties  `gorm:"type:json"`
	Username    string                   `gorm:"-"`
	PrivateData referenceTaskPrivateData `gorm:"column:private_data;type:json"`
	Data        json.RawMessage          `gorm:"type:json"`
}

func (referenceTaskSchema) TableName() string { return "tasks" }

type referenceTaskPlatform string
type referenceTaskStatus string

type referenceTaskProperties struct {
	Input             string `json:"input"`
	UpstreamModelName string `json:"upstream_model_name,omitempty"`
	OriginModelName   string `json:"origin_model_name,omitempty"`
}

func (value *referenceTaskProperties) Scan(src any) error {
	if src == nil {
		*value = referenceTaskProperties{}
		return nil
	}
	var bytes []byte
	switch typed := src.(type) {
	case []byte:
		bytes = typed
	case string:
		bytes = []byte(typed)
	default:
		return fmt.Errorf("scan reference Task properties from %T", src)
	}
	return json.Unmarshal(bytes, value)
}

func (value referenceTaskProperties) Value() (driver.Value, error) {
	if value == (referenceTaskProperties{}) {
		return nil, nil
	}
	return json.Marshal(value)
}

type referenceTaskPrivateData struct {
	Key string `json:"key,omitempty"`
}

func (value *referenceTaskPrivateData) Scan(src any) error {
	if src == nil {
		*value = referenceTaskPrivateData{}
		return nil
	}
	var bytes []byte
	switch typed := src.(type) {
	case []byte:
		bytes = typed
	case string:
		bytes = []byte(typed)
	default:
		return fmt.Errorf("scan reference Task private data from %T", src)
	}
	return json.Unmarshal(bytes, value)
}

func (value referenceTaskPrivateData) Value() (driver.Value, error) {
	if value == (referenceTaskPrivateData{}) {
		return nil, nil
	}
	return json.Marshal(value)
}

// legacyTaskSchema captures TokenRouter's Task declaration immediately before
// this slice. FailReason was explicitly TEXT; the three payload columns stay
// TEXT because deployed Jimeng rows intentionally include a non-JSON fencing
// prefix and older task history also accepts empty or malformed values.
type legacyTaskSchema struct {
	ID          int64 `gorm:"primaryKey;autoIncrement"`
	CreatedAt   int64 `gorm:"index"`
	UpdatedAt   int64
	TaskID      string `gorm:"type:varchar(191);index"`
	Platform    string `gorm:"type:varchar(30);index"`
	UserId      int    `gorm:"index"`
	Group       string `gorm:"type:varchar(50)"`
	ChannelId   int    `gorm:"index"`
	Quota       int
	Action      string `gorm:"type:varchar(40);index"`
	Status      string `gorm:"type:varchar(20);index"`
	FailReason  string `gorm:"type:text"`
	SubmitTime  int64  `gorm:"index"`
	StartTime   int64  `gorm:"index"`
	FinishTime  int64  `gorm:"index"`
	Progress    string `gorm:"type:varchar(20);index"`
	Properties  string `gorm:"type:text"`
	PrivateData string `gorm:"column:private_data;type:text"`
	Data        string `gorm:"type:text"`
}

func (legacyTaskSchema) TableName() string { return "tasks" }

var referenceTaskJSONFields = map[string]bool{
	"Properties":  true,
	"PrivateData": true,
	"Data":        true,
}

var expectedReferenceTaskIndexes = []struct {
	name   string
	column string
}{
	{name: "idx_tasks_created_at", column: "created_at"},
	{name: "idx_tasks_task_id", column: "task_id"},
	{name: "idx_tasks_platform", column: "platform"},
	{name: "idx_tasks_user_id", column: "user_id"},
	{name: "idx_tasks_channel_id", column: "channel_id"},
	{name: "idx_tasks_action", column: "action"},
	{name: "idx_tasks_status", column: "status"},
	{name: "idx_tasks_submit_time", column: "submit_time"},
	{name: "idx_tasks_start_time", column: "start_time"},
	{name: "idx_tasks_finish_time", column: "finish_time"},
	{name: "idx_tasks_progress", column: "progress"},
}

func TestReferenceTaskSchemaPortableDialects(t *testing.T) {
	targetSchema, err := schema.Parse(&Task{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	referenceSchema, err := schema.Parse(&referenceTaskSchema{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	legacySchema, err := schema.Parse(&legacyTaskSchema{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)

	referenceColumnCount := 0
	for _, referenceField := range referenceSchema.Fields {
		if referenceField.DBName == "" {
			continue
		}
		referenceColumnCount++
		targetField := targetSchema.LookUpField(referenceField.Name)
		require.NotNil(t, targetField, "Task.%s is missing", referenceField.Name)
		assert.Equal(t, referenceField.DBName, targetField.DBName, referenceField.Name+" column")
		assert.Equal(t, referenceField.PrimaryKey, targetField.PrimaryKey, referenceField.Name+" primary key")
		assert.Equal(t, referenceField.AutoIncrement, targetField.AutoIncrement, referenceField.Name+" auto increment")
		assert.Equal(t, referenceField.NotNull, targetField.NotNull, referenceField.Name+" nullability")
		assert.Equal(t, referenceField.HasDefaultValue, targetField.HasDefaultValue, referenceField.Name+" default presence")
		assert.Equal(t, referenceField.DefaultValue, targetField.DefaultValue, referenceField.Name+" default")
		assert.Equal(t, referenceField.Size, targetField.Size, referenceField.Name+" size")
		if referenceTaskJSONFields[referenceField.Name] {
			assert.Equal(t, "json", strings.ToLower(referenceField.TagSettings["TYPE"]))
			assert.Equal(t, "text", strings.ToLower(targetField.TagSettings["TYPE"]),
				"Task.%s must retain the legacy-compatible text representation", referenceField.Name)
		}
	}
	targetColumnCount := 0
	for _, field := range targetSchema.Fields {
		if field.DBName != "" {
			targetColumnCount++
		}
	}
	assert.Equal(t, referenceColumnCount, targetColumnCount, "Task must not gain hidden database columns")

	mysqlDB, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "user@tcp(127.0.0.1:1)/db?charset=utf8mb4&parseTime=true",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DisableAutomaticPing: true, DryRun: true})
	require.NoError(t, err)
	postgresDB, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=127.0.0.1 port=1 user=gorm dbname=gorm sslmode=disable",
	}), &gorm.Config{DisableAutomaticPing: true, DryRun: true})
	require.NoError(t, err)

	for _, dialect := range []gorm.Dialector{mysqlDB.Dialector, postgresDB.Dialector} {
		t.Run(dialect.Name(), func(t *testing.T) {
			for _, referenceField := range referenceSchema.Fields {
				if referenceField.DBName == "" {
					continue
				}
				targetField := targetSchema.LookUpField(referenceField.Name)
				require.NotNil(t, targetField)
				if referenceTaskJSONFields[referenceField.Name] {
					assert.Equal(t, "json", strings.ToLower(dialect.DataTypeOf(referenceField)),
						"reference Task.%s %s type", referenceField.Name, dialect.Name())
					assert.Equal(t, "text", strings.ToLower(dialect.DataTypeOf(targetField)),
						"legacy-compatible Task.%s %s type", referenceField.Name, dialect.Name())
					continue
				}
				assert.Equal(t, dialect.DataTypeOf(referenceField), dialect.DataTypeOf(targetField),
					"Task.%s %s type", referenceField.Name, dialect.Name())
			}

			legacyFailReason := legacySchema.LookUpField("FailReason")
			targetFailReason := targetSchema.LookUpField("FailReason")
			referenceFailReason := referenceSchema.LookUpField("FailReason")
			require.NotNil(t, legacyFailReason)
			require.NotNil(t, targetFailReason)
			require.NotNil(t, referenceFailReason)
			if dialect.Name() == "mysql" {
				assert.Equal(t, "text", strings.ToLower(dialect.DataTypeOf(legacyFailReason)))
				assert.Equal(t, "longtext", strings.ToLower(dialect.DataTypeOf(targetFailReason)))
				assert.Equal(t, "longtext", strings.ToLower(dialect.DataTypeOf(referenceFailReason)))
			} else {
				assert.Equal(t, "text", strings.ToLower(dialect.DataTypeOf(legacyFailReason)))
				assert.Equal(t, "text", strings.ToLower(dialect.DataTypeOf(targetFailReason)))
				assert.Equal(t, "text", strings.ToLower(dialect.DataTypeOf(referenceFailReason)))
			}
		})
	}

	referenceIndexes := referenceSchema.ParseIndexes()
	targetIndexes := targetSchema.ParseIndexes()
	assert.Len(t, targetIndexes, len(referenceIndexes))
	for _, referenceIndex := range referenceIndexes {
		targetIndex := targetSchema.LookIndex(referenceIndex.Name)
		require.NotNil(t, targetIndex, "missing Task index %s", referenceIndex.Name)
		assert.Equal(t, referenceIndex.Class, targetIndex.Class, referenceIndex.Name+" class")
		require.Len(t, targetIndex.Fields, len(referenceIndex.Fields), referenceIndex.Name+" fields")
		for i := range referenceIndex.Fields {
			assert.Equal(t, referenceIndex.Fields[i].DBName, targetIndex.Fields[i].DBName,
				referenceIndex.Name+" column")
			assert.Equal(t, referenceIndex.Fields[i].Priority, targetIndex.Fields[i].Priority,
				referenceIndex.Name+" priority")
		}
	}

	failReason := targetSchema.LookUpField("FailReason")
	require.NotNil(t, failReason)
	assert.Empty(t, failReason.TagSettings["TYPE"], "Task.FailReason must use the reference dialect-native string type")
}

func TestReferenceTaskSchemaSQLite(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, migrateDB())
	assertReferenceTaskSchemaMatchesSQLite(t, db)
	assertReferenceTaskSchema(t, db)
	insertReferenceTaskWithNoDatabaseDefaults(t, db, "task-schema-direct")

	widened := Task{
		TaskID: "task-schema-wide-failure", Platform: "video", UserId: 71,
		FailReason: strings.Repeat("failure", 10_000),
		Properties: "legacy-property", PrivateData: `jimeng-v2:{"key":"ciphertext"}`, Data: "",
	}
	require.NoError(t, db.Create(&widened).Error)
	assertTaskPayloadsPreserved(t, db, widened.ID, widened.FailReason, widened.Properties, widened.PrivateData, widened.Data)

	columnsBefore := sqliteColumnSignatures(t, db, Task{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, Task{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, Task{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, Task{}.TableName()))
	assertReferenceTaskSchemaMatchesSQLite(t, db)
}

func TestReferenceTaskSchemaSQLitePreservesLegacyRows(t *testing.T) {
	db := newReferenceSchemaTestDB(t)
	require.NoError(t, db.AutoMigrate(&legacyTaskSchema{}))
	legacy := makeLegacyReferenceTask(strings.Repeat("legacy-failure", 6_000))
	require.NoError(t, db.Create(&legacy).Error)

	require.NoError(t, migrateDB())
	assertReferenceTaskSchemaMatchesSQLite(t, db)
	assertReferenceTaskSchema(t, db)
	assertLegacyReferenceTaskPreserved(t, db, legacy)
	insertReferenceTaskWithNoDatabaseDefaults(t, db, "task-legacy-direct")

	columnsBefore := sqliteColumnSignatures(t, db, Task{}.TableName())
	indexesBefore := sqliteIndexSignatures(t, db, Task{}.TableName())
	require.NoError(t, migrateDB())
	assert.Equal(t, columnsBefore, sqliteColumnSignatures(t, db, Task{}.TableName()))
	assert.Equal(t, indexesBefore, sqliteIndexSignatures(t, db, Task{}.TableName()))
	assertLegacyReferenceTaskPreserved(t, db, legacy)
}

func assertReferenceTaskSchemaMatchesSQLite(t *testing.T, db *gorm.DB) {
	t.Helper()
	reference, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "reference-task.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, reference.AutoMigrate(&referenceTaskSchema{}))

	referenceColumns := sqliteColumnSignatures(t, reference, Task{}.TableName())
	targetColumns := sqliteColumnSignatures(t, db, Task{}.TableName())
	require.Len(t, targetColumns, len(referenceColumns))
	referenceByName := make(map[string]sqliteColumnSignature, len(referenceColumns))
	for _, column := range referenceColumns {
		referenceByName[column.Name] = column
	}
	for i := range targetColumns {
		if !referenceTaskJSONColumn(targetColumns[i].Name) {
			continue
		}
		referenceColumn, ok := referenceByName[targetColumns[i].Name]
		require.True(t, ok, "reference Task column %s is missing", targetColumns[i].Name)
		assert.Equal(t, "json", strings.ToLower(referenceColumn.Type), targetColumns[i].Name+" reference type")
		assert.Equal(t, "text", strings.ToLower(targetColumns[i].Type), targetColumns[i].Name+" retained target type")
		targetColumns[i].Type = referenceColumn.Type
	}
	assert.Equal(t, referenceColumns, targetColumns,
		"Task columns after normalizing only the three documented JSON/text residuals")
	assert.Equal(t,
		sqliteIndexSignatures(t, reference, Task{}.TableName()),
		sqliteIndexSignatures(t, db, Task{}.TableName()),
		"Task indexes",
	)
}

func referenceTaskJSONColumn(name string) bool {
	switch name {
	case "properties", "private_data", "data":
		return true
	default:
		return false
	}
}

// assertReferenceTaskSchema is also used by the opt-in MySQL/PostgreSQL gate.
func assertReferenceTaskSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, index := range expectedReferenceTaskIndexes {
		assertTaskIndex(t, db, index.name, index.column)
	}

	statement := &gorm.Statement{DB: db}
	require.NoError(t, statement.Parse(&Task{}))
	failReason := statement.Schema.LookUpField("FailReason")
	require.NotNil(t, failReason)
	assert.Empty(t, failReason.TagSettings["TYPE"])
	for _, fieldName := range []string{"Properties", "PrivateData", "Data"} {
		field := statement.Schema.LookUpField(fieldName)
		require.NotNil(t, field)
		assert.Equal(t, "text", strings.ToLower(field.TagSettings["TYPE"]),
			"Task.%s must preserve deployed text payload semantics", fieldName)
	}

	switch db.Dialector.Name() {
	case "sqlite":
		assertColumnDatabaseType(t, db, Task{}.TableName(), "id", "integer")
		assertColumnDatabaseType(t, db, Task{}.TableName(), "fail_reason", "text")
		for _, column := range []string{"properties", "private_data", "data"} {
			assertColumnDatabaseType(t, db, Task{}.TableName(), column, "text")
		}
	case "mysql":
		assertColumnDatabaseType(t, db, Task{}.TableName(), "id", "bigint")
		assertColumnDatabaseType(t, db, Task{}.TableName(), "fail_reason", "longtext")
		for _, column := range []string{"properties", "private_data", "data"} {
			assertColumnDatabaseType(t, db, Task{}.TableName(), column, "text")
		}
		assertReferenceTaskStringLengths(t, db)
	case "postgres":
		assertColumnDatabaseType(t, db, Task{}.TableName(), "id", "int8")
		assertColumnDatabaseType(t, db, Task{}.TableName(), "fail_reason", "text")
		for _, column := range []string{"properties", "private_data", "data"} {
			assertColumnDatabaseType(t, db, Task{}.TableName(), column, "text")
		}
		assertReferenceTaskStringLengths(t, db)
	default:
		require.FailNow(t, "unsupported Task schema test dialect", db.Dialector.Name())
	}
}

func assertReferenceTaskStringLengths(t *testing.T, db *gorm.DB) {
	t.Helper()
	for column, length := range map[string]int64{
		"task_id": 191, "platform": 30, "group": 50,
		"action": 40, "status": 20, "progress": 20,
	} {
		assertColumnLength(t, db, Task{}.TableName(), column, length)
	}
}

func assertTaskIndex(t *testing.T, db *gorm.DB, name, column string) {
	t.Helper()
	columns, unique := requirePortableIndex(t, db, &Task{}, Task{}.TableName(), name)
	assert.Equal(t, []string{column}, columns, name+" columns")
	assert.False(t, unique, name+" must remain an ordinary index")
}

func insertReferenceTaskWithNoDatabaseDefaults(t *testing.T, db *gorm.DB, taskID string) {
	t.Helper()
	require.NoError(t, db.Exec("INSERT INTO tasks (task_id) VALUES (?)", taskID).Error)

	var id int64
	var createdAt, updatedAt, userID, channelID, quota, submitTime, startTime, finishTime sql.NullInt64
	var platform, group, action, status, failReason, progress, properties, privateData, data sql.NullString
	selection := fmt.Sprintf(`id, created_at, updated_at, platform, user_id,
		%s, channel_id, quota, action, status, fail_reason, submit_time, start_time,
		finish_time, progress, properties, private_data, data`, quoteReferenceSchemaIdentifier(db, "group"))
	row := db.Table(Task{}.TableName()).Select(selection).Where("task_id = ?", taskID).Row()
	require.NoError(t, row.Scan(&id, &createdAt, &updatedAt, &platform, &userID,
		&group, &channelID, &quota, &action, &status, &failReason, &submitTime, &startTime,
		&finishTime, &progress, &properties, &privateData, &data))
	assert.Positive(t, id, "Task ID must remain database-generated")
	for name, valid := range map[string]bool{
		"created_at": createdAt.Valid, "updated_at": updatedAt.Valid, "platform": platform.Valid,
		"user_id": userID.Valid, "group": group.Valid, "channel_id": channelID.Valid,
		"quota": quota.Valid, "action": action.Valid, "status": status.Valid,
		"fail_reason": failReason.Valid, "submit_time": submitTime.Valid,
		"start_time": startTime.Valid, "finish_time": finishTime.Valid,
		"progress": progress.Valid, "properties": properties.Valid,
		"private_data": privateData.Valid, "data": data.Valid,
	} {
		assert.False(t, valid, "Task.%s must retain the reference's absent database default", name)
	}
}

func makeLegacyReferenceTask(failReason string) legacyTaskSchema {
	return legacyTaskSchema{
		CreatedAt: 101, UpdatedAt: 102, TaskID: "task-schema-legacy", Platform: "video",
		UserId: 103, Group: "legacy", ChannelId: 104, Quota: 105, Action: "generate",
		Status: TaskStatusFailure, FailReason: failReason, SubmitTime: 106, StartTime: 107,
		FinishTime: 108, Progress: "100%", Properties: "legacy-property",
		PrivateData: `jimeng-v2:{"key":"encrypted-secret","reservation_id":"reservation"}`,
		Data:        "",
	}
}

func assertLegacyReferenceTaskPreserved(t *testing.T, db *gorm.DB, legacy legacyTaskSchema) {
	t.Helper()
	var migrated Task
	require.NoError(t, db.First(&migrated, legacy.ID).Error)
	assert.Equal(t, legacy.CreatedAt, migrated.CreatedAt)
	assert.Equal(t, legacy.UpdatedAt, migrated.UpdatedAt)
	assert.Equal(t, legacy.TaskID, migrated.TaskID)
	assert.Equal(t, legacy.Platform, migrated.Platform)
	assert.Equal(t, legacy.UserId, migrated.UserId)
	assert.Equal(t, legacy.Group, migrated.Group)
	assert.Equal(t, legacy.ChannelId, migrated.ChannelId)
	assert.Equal(t, legacy.Quota, migrated.Quota)
	assert.Equal(t, legacy.Action, migrated.Action)
	assert.Equal(t, legacy.Status, migrated.Status)
	assert.Equal(t, legacy.SubmitTime, migrated.SubmitTime)
	assert.Equal(t, legacy.StartTime, migrated.StartTime)
	assert.Equal(t, legacy.FinishTime, migrated.FinishTime)
	assert.Equal(t, legacy.Progress, migrated.Progress)
	assert.Equal(t, legacy.FailReason, migrated.FailReason)
	assert.Equal(t, legacy.Properties, migrated.Properties)
	assert.Equal(t, legacy.PrivateData, migrated.PrivateData)
	assert.Equal(t, legacy.Data, migrated.Data)
	assert.False(t, json.Valid([]byte(legacy.Properties)), "the established malformed property value documents why JSON conversion is unsafe")
	assert.False(t, json.Valid([]byte(legacy.PrivateData)), "the Jimeng fencing prefix intentionally is not JSON")
}

func assertTaskPayloadsPreserved(t *testing.T, db *gorm.DB, id int64, failReason, properties, privateData, data string) {
	t.Helper()
	var stored Task
	require.NoError(t, db.First(&stored, id).Error)
	assert.Equal(t, failReason, stored.FailReason)
	assert.Equal(t, properties, stored.Properties)
	assert.Equal(t, privateData, stored.PrivateData)
	assert.Equal(t, data, stored.Data)
}

func exerciseReferenceTaskSchemaLegacyExternal(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.Contains(t, []string{"mysql", "postgres"}, db.Dialector.Name())
	require.NoError(t, db.Migrator().DropTable(&Task{}))
	require.NoError(t, db.AutoMigrate(&legacyTaskSchema{}))

	legacy := makeLegacyReferenceTask(strings.Repeat("legacy-failure", 4_500))
	require.NoError(t, db.Create(&legacy).Error)
	require.NoError(t, migrateDB())
	assertReferenceTaskSchema(t, db)
	assertLegacyReferenceTaskPreserved(t, db, legacy)
	insertReferenceTaskWithNoDatabaseDefaults(t, db, "task-server-direct")

	if db.Dialector.Name() == "mysql" {
		wideFailure := strings.Repeat("w", 70_000)
		widened := Task{TaskID: "task-server-wide-failure", Platform: "video", FailReason: wideFailure,
			Properties: "legacy-property", PrivateData: `jimeng-v2:{"key":"ciphertext"}`, Data: ""}
		require.NoError(t, db.Create(&widened).Error,
			"the safe reference widening must accept a value larger than MySQL TEXT")
		assertTaskPayloadsPreserved(t, db, widened.ID, wideFailure, widened.Properties, widened.PrivateData, widened.Data)
	}

	require.NoError(t, migrateDB())
	assertReferenceTaskSchema(t, db)
	assertLegacyReferenceTaskPreserved(t, db, legacy)
}
