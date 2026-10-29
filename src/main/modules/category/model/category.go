package model

import "time"

type Category struct {
	ID          int64     `json:"id"`
	ParentID    *int64    `json:"parent_id"`
	Code        string    `json:"code"`
	Name        string    `json:"name"`
	Description *string   `json:"description"`
	IsActive    bool      `json:"is_active"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ListFilter struct {
	ParentID *int64
	TopLevel bool
	IsActive *bool
}
