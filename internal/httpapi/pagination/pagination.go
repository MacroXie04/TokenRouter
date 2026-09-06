// Package pagination implements the bounded paging contract shared by HTTP
// list handlers.
package pagination

import (
	"github.com/gin-gonic/gin"
	"strconv"
)

const maxPageNumber = 1_000_000

// Page is the reference paging response shape.
type Page struct {
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
	Total    int `json:"total"`
	Items    any `json:"items"`
}

// FromContext parses the reference paging query and compatibility aliases.
func FromContext(c *gin.Context) *Page {
	page, _ := strconv.Atoi(c.DefaultQuery("p", "1"))
	if page < 1 {
		page = 1
	}
	if page > maxPageNumber {
		page = maxPageNumber
	}
	size := 0
	for _, key := range []string{"page_size", "ps", "size"} {
		parsed, err := strconv.Atoi(c.Query(key))
		if err == nil && parsed != 0 {
			size = parsed
			break
		}
	}
	if size < 1 {
		size = 10
	}
	if size > 100 {
		size = 100
	}
	return &Page{Page: page, PageSize: size}
}

// Offset returns the bounded zero-based row offset.
func (p *Page) Offset() int {
	return (p.Page - 1) * p.PageSize
}
