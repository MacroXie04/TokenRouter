package common

import (
	"os"
	"strconv"
	"strings"
)

// GetEnv returns the environment variable value or a default when unset/empty.
func GetEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

// GetEnvOrDefault returns the env value or the provided default (alias).
func GetEnvOrDefault(key, defaultValue string) string {
	return GetEnv(key, defaultValue)
}

// GetEnvBool parses an env var as a boolean, returning default on error.
func GetEnvBool(key string, defaultValue bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return defaultValue
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return defaultValue
	}
	return b
}

// GetEnvInt parses an env var as an integer, returning default on error.
func GetEnvInt(key string, defaultValue int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultValue
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return defaultValue
	}
	return i
}

// GetEnvFloat parses an env var as a float64, returning default on error.
func GetEnvFloat(key string, defaultValue float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return defaultValue
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return defaultValue
	}
	return f
}

// GetEnvStrings parses a comma-separated env var into a trimmed string slice.
func GetEnvStrings(key string) []string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// UsingSQLite reports whether the primary database DSN is a SQLite path.
func UsingSQLite() bool {
	dsn := os.Getenv("SQL_DSN")
	path := os.Getenv("SQLITE_PATH")
	return dsn == "" && path != ""
}

// UsingPostgreSQL reports whether the primary database is PostgreSQL.
func UsingPostgreSQL() bool {
	return strings.HasPrefix(os.Getenv("SQL_DSN"), "postgres://") ||
		strings.HasPrefix(os.Getenv("SQL_DSN"), "postgresql://")
}

// UsingMySQL reports whether the primary database is MySQL.
func UsingMySQL() bool {
	dsn := os.Getenv("SQL_DSN")
	// InitDB treats every non-empty, non-PostgreSQL SQL_DSN as a MySQL DSN.
	// That includes the driver's supported Unix-socket form
	// (user@unix(/path/to/socket)/database), which must still enable row locks.
	return dsn != "" && !strings.HasPrefix(dsn, "postgres://") &&
		!strings.HasPrefix(dsn, "postgresql://")
}
