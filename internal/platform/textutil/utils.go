package textutil

import (
	"fmt"
	"strconv"
)

// Sprint formats any value with fmt.Sprintf("%v", v).
func Sprint(v any) string {
	return fmt.Sprintf("%v", v)
}

// Str2Int parses a string to int, returning 0 on error.
func Str2Int(s string) int {
	i, _ := strconv.Atoi(s)
	return i
}

// Int2Str formats an int to a string.
func Int2Str(i int) string {
	return strconv.Itoa(i)
}
