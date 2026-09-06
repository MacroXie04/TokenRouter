import type { PricingCatalogItem } from './catalog';

export const BILLING_EXPRESSION_VARIABLES = [
  'p', 'c', 'len', 'cr', 'cc', 'cc1h', 'img', 'ai', 'ao', 'img_o',
] as const;
export type BillingExpressionVariable = (typeof BILLING_EXPRESSION_VARIABLES)[number];

export const BILLING_EXPRESSION_FUNCTIONS = [
  'tier', 'param', 'header', 'has', 'hour', 'minute', 'weekday', 'month', 'day', 'max', 'min', 'abs', 'ceil', 'floor',
] as const;
export type BillingExpressionFunction = (typeof BILLING_EXPRESSION_FUNCTIONS)[number];

export interface BillingExpressionDescription {
  variables: BillingExpressionVariable[];
  functions: BillingExpressionFunction[];
}

const VARIABLE_SET = new Set<string>(BILLING_EXPRESSION_VARIABLES);
const FUNCTION_SET = new Set<string>(BILLING_EXPRESSION_FUNCTIONS);

// Introspection only: this scanner never evaluates operator-controlled billing
// code. Quoted strings are skipped so text such as header("img") does not
// falsely claim that image-token pricing is active.
export function describeBillingExpression(expression: string): BillingExpressionDescription {
  const variables = new Set<BillingExpressionVariable>();
  const functions = new Set<BillingExpressionFunction>();
  let quote = '';
  let escaped = false;
  for (let index = 0; index < expression.length;) {
    const character = expression[index];
    if (quote) {
      if (escaped) escaped = false;
      else if (character === '\\') escaped = true;
      else if (character === quote) quote = '';
      index += 1;
      continue;
    }
    if (character === '"' || character === "'" || character === '`') {
      quote = character;
      index += 1;
      continue;
    }
    if (!/[A-Za-z_]/.test(character)) {
      index += 1;
      continue;
    }
    let end = index + 1;
    while (end < expression.length && /[A-Za-z0-9_]/.test(expression[end])) end += 1;
    const identifier = expression.slice(index, end);
    if (VARIABLE_SET.has(identifier)) variables.add(identifier as BillingExpressionVariable);
    if (FUNCTION_SET.has(identifier)) functions.add(identifier as BillingExpressionFunction);
    index = end;
  }
  return { variables: [...variables], functions: [...functions] };
}

export function isDynamicPricingItem(item: PricingCatalogItem): boolean {
  return item.billing_mode === 'tiered_expr' && Boolean(item.billing_expr);
}
