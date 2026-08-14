package router

import (
	"fmt"
	"os"
	"testing"
)

// TestDumpRoutes writes every route registered by SetUpRouter
// ("METHOD /path" per line) to /tmp/tokenrouter_routes.txt for the
// API-matrix verification script (scripts/verify-api-matrix.sh).
func TestDumpRoutes(t *testing.T) {
	r := SetUpRouter()
	f, err := os.Create("/tmp/tokenrouter_routes.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, route := range r.Routes() {
		fmt.Fprintf(f, "%s %s\n", route.Method, route.Path)
	}
}
