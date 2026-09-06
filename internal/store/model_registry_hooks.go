package store

import "gorm.io/gorm"

// BeforeCreate maintains the portable active-name uniqueness key for every
// write path, including imports and direct GORM callers. Process-local locks
// cannot protect two application nodes from inserting the same active name.
func (vendor *Vendor) BeforeCreate(_ *gorm.DB) error {
	if vendor.DeletedAt.Valid {
		vendor.ActiveName = nil
		return nil
	}
	name := vendor.Name
	vendor.ActiveName = &name
	return nil
}

// BeforeCreate keeps the database uniqueness key populated on new active
// rows. Deleted historical rows deliberately release the key.
func (metadata *Model) BeforeCreate(_ *gorm.DB) error {
	if metadata.DeletedAt.Valid {
		metadata.ActiveName = nil
		return nil
	}
	name := metadata.ModelName
	metadata.ActiveName = &name
	return nil
}
