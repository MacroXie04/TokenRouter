package env

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func StrictScheduledIntegerEnv(key string, fallback, minimum, maximum int) (int, error) {
	raw, configured := os.LookupEnv(key)
	if !configured || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	trimmed := strings.TrimSpace(raw)
	value, err := strconv.Atoi(trimmed)
	if err != nil || strconv.Itoa(value) != trimmed || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be an integer from %d to %d", key, minimum, maximum)
	}
	return value, nil
}
