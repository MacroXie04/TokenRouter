package dto

import (
	"math"
	"testing"
)

func TestPaginationNormalizeBoundsOffset(t *testing.T) {
	tests := []struct {
		name       string
		pagination Pagination
		wantPage   int
		wantSize   int
		wantOffset int
	}{
		{name: "defaults", pagination: Pagination{}, wantPage: 1, wantSize: 10, wantOffset: 0},
		{name: "valid", pagination: Pagination{Page: 3, PageSize: 25}, wantPage: 3, wantSize: 25, wantOffset: 50},
		{
			name:       "extreme values are bounded",
			pagination: Pagination{Page: math.MaxInt, PageSize: math.MaxInt},
			wantPage:   maxPaginationPage,
			wantSize:   10,
			wantOffset: (maxPaginationPage - 1) * 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.pagination.Normalize()
			if tt.pagination.Page != tt.wantPage {
				t.Fatalf("Page = %d, want %d", tt.pagination.Page, tt.wantPage)
			}
			if tt.pagination.PageSize != tt.wantSize {
				t.Fatalf("PageSize = %d, want %d", tt.pagination.PageSize, tt.wantSize)
			}
			if got := tt.pagination.Offset(); got != tt.wantOffset {
				t.Fatalf("Offset() = %d, want %d", got, tt.wantOffset)
			}
		})
	}
}
