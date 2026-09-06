package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDatabaseTypeDetectionAcceptsMySQLUnixSocketDSN(t *testing.T) {
	t.Setenv("SQL_DSN", "root@unix(/tmp/mysql.sock)/tokenrouter?parseTime=true")
	assert.True(t, UsingMySQL())
	assert.False(t, UsingPostgreSQL())
	assert.False(t, UsingSQLite())

	t.Setenv("SQL_DSN", "postgresql://tokenrouter@example.invalid/tokenrouter")
	assert.False(t, UsingMySQL())
	assert.True(t, UsingPostgreSQL())
}
