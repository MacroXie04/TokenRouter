package quota

import (
	"fmt"
)

// QuotaPerUnit is the internal accounting unit per one US dollar of value.
// A quota column value of QuotaPerUnit therefore represents $1.00.
const QuotaPerUnit = 500000

// Validate the persisted accounting unit independently of runtime settings.
func init() {
	// Guard against accidental zero quota unit.
	if QuotaPerUnit <= 0 {
		panic(fmt.Sprintf("invalid QuotaPerUnit: %d", QuotaPerUnit))
	}
}
