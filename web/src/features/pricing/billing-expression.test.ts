import { describe, expect, it } from 'vitest';
import { describeBillingExpression, isDynamicPricingItem } from './billing-expression';
import type { PricingCatalogItem } from './catalog';

const baseItem: PricingCatalogItem = {
  model_name: 'alpha', vendor_id: 0, quota_type: 0, model_ratio: 1, model_price: 0,
  prompt_price: 1, completion_price: 3, owner_by: 'custom', completion_ratio: 3,
  enable_groups: ['default'], supported_endpoint_types: ['openai'],
};

describe('billing expression description', () => {
  it('finds supported variables and functions without evaluating the expression', () => {
    expect(describeBillingExpression('v1:len < 100 ? tier("base", p * 2 + cr * .2) : tier("long", c * 8)')).toEqual({
      variables: ['len', 'p', 'cr', 'c'],
      functions: ['tier'],
    });
  });

  it('does not mistake identifiers inside quoted request values for pricing variables', () => {
    expect(describeBillingExpression('header("img") == "p" ? ai * 2 : ao * 4')).toEqual({
      variables: ['ai', 'ao'],
      functions: ['header'],
    });
  });

  it('requires both the exact mode and an expression to identify dynamic pricing', () => {
    expect(isDynamicPricingItem({ ...baseItem, billing_mode: 'tiered_expr', billing_expr: 'p * 2' })).toBe(true);
    expect(isDynamicPricingItem(baseItem)).toBe(false);
  });
});
