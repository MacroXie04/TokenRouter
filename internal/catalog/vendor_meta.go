package catalog

import (
	"errors"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"strings"
	"unicode/utf8"
)

func ValidateVendorMetadata(vendor *model.Vendor) error {
	if vendor == nil {
		return errors.New("供应商不能为空")
	}
	if vendor.Id < 0 {
		return errors.New("供应商 ID 无效")
	}
	vendor.Name = strings.TrimSpace(vendor.Name)
	vendor.Description = strings.TrimSpace(vendor.Description)
	vendor.Icon = strings.TrimSpace(vendor.Icon)
	if vendor.Name == "" {
		return errors.New("供应商名称不能为空")
	}
	if utf8.RuneCountInString(vendor.Name) > 128 {
		return errors.New("供应商名称不能超过 128 个字符")
	}
	if utf8.RuneCountInString(vendor.Icon) > 128 {
		return errors.New("供应商图标不能超过 128 个字符")
	}
	if len(vendor.Description) > 1<<20 {
		return errors.New("供应商描述不能超过 1 MiB")
	}
	return nil
}
