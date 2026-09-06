// Package dto defines API request and response transfer objects.
package dto

const maxPaginationPage = 1_000_000

// Response is the standard JSON envelope for dashboard API responses.
type Response struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
}

// Ok builds a success response.
func Ok(data any) *Response {
	return &Response{Success: true, Data: data}
}

// OkMessage builds a success response with a message.
func OkMessage(message string) *Response {
	return &Response{Success: true, Message: message}
}

// Fail builds a failure response.
func Fail(message string) *Response {
	return &Response{Success: false, Message: message}
}

// Pagination is a standard pagination cursor.
type Pagination struct {
	Page       int   `json:"page" form:"page"`
	PageSize   int   `json:"page_size" form:"page_size"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
}

// Normalize clamps page/page_size to sane bounds.
func (p *Pagination) Normalize() {
	if p.Page <= 0 {
		p.Page = 1
	}
	if p.Page > maxPaginationPage {
		p.Page = maxPaginationPage
	}
	if p.PageSize <= 0 || p.PageSize > 100 {
		p.PageSize = 10
	}
}

// Offset returns the SQL offset for the current page.
func (p *Pagination) Offset() int {
	return (p.Page - 1) * p.PageSize
}
