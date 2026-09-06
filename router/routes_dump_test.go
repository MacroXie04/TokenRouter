package router

import (
	"fmt"
	"os"
	"sort"
	"testing"
)

// TestDumpRoutes writes every route registered by SetUpRouter as sorted,
// tab-separated method, path, and runtime handler name fields. The API-matrix
// tooling uses the handler field to distinguish a mounted endpoint from a
// behavioral implementation; route presence by itself is not parity evidence.
func TestDumpRoutes(t *testing.T) {
	r := SetUpRouter()
	outputPath := os.Getenv("TOKENROUTER_ROUTE_DUMP")
	if outputPath == "" {
		outputPath = "/tmp/tokenrouter_routes.txt"
	}
	f, err := os.Create(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Errorf("close route dump: %v", err)
		}
	}()
	routes := r.Routes()
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Method != routes[j].Method {
			return routes[i].Method < routes[j].Method
		}
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		return routes[i].Handler < routes[j].Handler
	})
	for _, route := range routes {
		if _, err := fmt.Fprintf(f, "%s\t%s\t%s\n", route.Method, route.Path, route.Handler); err != nil {
			t.Fatalf("write route dump: %v", err)
		}
	}
}
