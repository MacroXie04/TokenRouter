package store

import (
	"context"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPrimaryDatabaseUnixTimestampFailsClosedWithoutDatabaseOrContext(t *testing.T) {
	previous := DB
	DB = nil
	t.Cleanup(func() { DB = previous })

	_, err := PrimaryDatabaseUnixTimestamp(context.Background())
	require.EqualError(t, err, "database is nil")
	_, err = PrimaryDatabaseUnixTimestamp(nil)
	require.EqualError(t, err, "database clock context is nil")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = PrimaryDatabaseUnixTimestamp(ctx)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestPrimaryDatabaseUnixTimestampUsesDatabaseClock(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	previous := DB
	DB = db
	t.Cleanup(func() { DB = previous })

	now, err := PrimaryDatabaseUnixTimestamp(context.Background())
	require.NoError(t, err)
	assert.Positive(t, now)
}

func TestDatabaseTimeExpressionsUseStatementTimeAcrossDialects(t *testing.T) {
	tests := []struct {
		dialect    string
		want       string
		disallowed string
	}{
		{dialect: "sqlite", want: "strftime('%s', 'now')"},
		{dialect: "mysql", want: "UNIX_TIMESTAMP()"},
		{dialect: "postgres", want: "FLOOR(EXTRACT(EPOCH FROM clock_timestamp()))", disallowed: "CURRENT_TIMESTAMP"},
	}
	for _, test := range tests {
		t.Run(test.dialect, func(t *testing.T) {
			expression, err := databaseTimeExpression(test.dialect)
			require.NoError(t, err)
			assert.Contains(t, expression, test.want)
			if test.disallowed != "" {
				assert.NotContains(t, strings.ToUpper(expression), test.disallowed)
			}
		})
	}

	_, err := databaseTimeExpression("unsupported")
	require.EqualError(t, err, `unsupported database clock dialect "unsupported"`)
}
