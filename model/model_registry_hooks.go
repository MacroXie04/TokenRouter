package model

import "gorm.io/gorm"

func (vendor *Vendor) BeforeCreate(_ *gorm.DB) error {
	name := vendor.Name
	vendor.ActiveName = &name
	return nil
}

func (metadata *Model) BeforeCreate(_ *gorm.DB) error {
	name := metadata.ModelName
	metadata.ActiveName = &name
	return nil
}
