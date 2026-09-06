import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import {
  describeBillingExpression,
  type BillingExpressionVariable,
} from './billing-expression';

const OUTPUT_VARIABLES = new Set<BillingExpressionVariable>(['c', 'ao', 'img_o']);

export function DynamicBillingExplanation({ expression }: { expression: string }) {
  const { t } = useTranslation();
  const description = useMemo(() => describeBillingExpression(expression), [expression]);
  return (
    <div className="pricing-dynamic-billing">
      <dl>
        <div><dt>{t('Billing mode')}</dt><dd><code>tiered_expr</code></dd></div>
        <div><dt>{t('Price breakdown')}</dt><dd><code>{expression}</code></dd></div>
      </dl>
      {(description.variables.length > 0 || description.functions.length > 0) && (
        <div className="pricing-dynamic-legend" aria-label={t('Price breakdown')}>
          {description.variables.map((variable) => (
            <span key={variable}>
              <code>{variable}</code>
              {' · '}
              {OUTPUT_VARIABLES.has(variable) ? t('Output') : t('Input')}
            </span>
          ))}
          {description.functions.map((name) => <span key={name}><code>{name}()</code></span>)}
        </div>
      )}
    </div>
  );
}
