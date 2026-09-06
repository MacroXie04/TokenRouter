package service

import (
	"github.com/tokenrouter/tokenrouter/common"
)

const trustQuotaUnits = 10

// RelayTrustQuota returns the ordinary-relay trust threshold. QuotaPerUnit is
// an immutable accounting invariant in TokenRouter, so a stale or corrupted
// compatibility option must never lower this threshold and broaden the
// zero-hold bypass.
func RelayTrustQuota() (quota int, valid bool) {
	return trustQuotaUnits * common.QuotaPerUnit, true
}
