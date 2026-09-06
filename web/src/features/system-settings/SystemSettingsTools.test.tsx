// @vitest-environment jsdom

import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { testDeploymentConnection } from '../models';
import {
  clearAffinityCache,
  confirmPaymentCompliance,
  loadAffinityCacheStats,
  resetModelPricing,
} from './system-settings-api';
import {
  AffinityCachePanel,
  DeploymentConnectionPanel,
  PaymentCompliancePanel,
  PricingResetPanel,
} from './SystemSettingsTools';

vi.mock('./system-settings-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./system-settings-api')>();
  return {
    ...actual,
    clearAffinityCache: vi.fn(),
    confirmPaymentCompliance: vi.fn(),
    loadAffinityCacheStats: vi.fn(),
    resetModelPricing: vi.fn(),
  };
});
vi.mock('../models', () => ({ testDeploymentConnection: vi.fn() }));
vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  return { ...actual, useTranslation: () => ({ t: (key: string) => key }) };
});

const mockedCompliance = vi.mocked(confirmPaymentCompliance);
const mockedReset = vi.mocked(resetModelPricing);
const mockedLoadAffinity = vi.mocked(loadAffinityCacheStats);
const mockedClearAffinity = vi.mocked(clearAffinityCache);
const mockedTestDeployment = vi.mocked(testDeploymentConnection);

beforeEach(() => {
  vi.resetAllMocks();
  mockedCompliance.mockResolvedValue({ confirmed: true, termsVersion: 'v1', confirmedAt: 1, confirmedBy: 1 });
  mockedReset.mockResolvedValue(undefined);
  mockedLoadAffinity.mockResolvedValue({
    enabled: true,
    total: 2,
    unknown: 0,
    byRuleName: { premium: 2 },
    cacheCapacity: 100,
    cacheAlgorithm: 'lru',
  });
  mockedClearAffinity.mockResolvedValue(2);
  mockedTestDeployment.mockResolvedValue(undefined);
});

afterEach(cleanup);

describe('dedicated system settings tools', () => {
  it('requires an explicit acknowledgement for payment compliance', async () => {
    const user = userEvent.setup();
    const onChanged = vi.fn();
    render(<PaymentCompliancePanel options={[]} onChanged={onChanged} />);
    const button = screen.getByRole('button', { name: 'Confirm payment compliance' }) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    await user.click(screen.getByRole('checkbox'));
    expect(button.disabled).toBe(false);
    await user.click(button);
    await waitFor(() => expect(mockedCompliance).toHaveBeenCalledOnce());
    expect(mockedCompliance.mock.calls[0][0]).toBeInstanceOf(AbortSignal);
    expect(onChanged).toHaveBeenCalledOnce();
  });

  it('does not reset pricing until the destructive alert dialog is confirmed', async () => {
    const user = userEvent.setup();
    const onChanged = vi.fn();
    render(<PricingResetPanel onChanged={onChanged} />);
    await user.click(screen.getByRole('button', { name: 'Reset model pricing' }));
    expect(mockedReset).not.toHaveBeenCalled();
    expect(screen.getByRole('alertdialog', { name: 'Reset model pricing?' })).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'Reset' }));
    await waitFor(() => expect(mockedReset).toHaveBeenCalledOnce());
    expect(onChanged).toHaveBeenCalledOnce();
  });

  it('loads affinity stats and explicitly confirms a rule-scoped clear before refreshing', async () => {
    const user = userEvent.setup();
    render(<AffinityCachePanel />);
    expect(await screen.findByText('2')).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'Clear premium (2)' }));
    expect(mockedClearAffinity).not.toHaveBeenCalled();
    expect(screen.getByRole('alertdialog', { name: 'Clear affinity cache?' })).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'Clear cache' }));
    await waitFor(() => expect(mockedClearAffinity).toHaveBeenCalledOnce());
    expect(mockedClearAffinity.mock.calls[0][0]).toEqual({ ruleName: 'premium' });
    expect(mockedLoadAffinity).toHaveBeenCalledTimes(2);
    expect(screen.getByRole('button', { name: 'Refresh' })).toHaveProperty('disabled', false);
  });

  it('redacts rejected compliance and pricing mutations in the dedicated panels', async () => {
    const user = userEvent.setup();
    mockedCompliance.mockRejectedValue(new Error('sk-private-compliance'));
    mockedReset.mockRejectedValue(new Error('sk-private-pricing'));
    const view = render(<PaymentCompliancePanel options={[]} onChanged={vi.fn()} />);
    await user.click(screen.getByRole('checkbox'));
    await user.click(screen.getByRole('button', { name: 'Confirm payment compliance' }));
    expect(await screen.findByRole('alert')).toHaveProperty('textContent', 'Request failed.');
    expect(screen.queryByText(/sk-private/)).toBeNull();

    view.rerender(<PricingResetPanel onChanged={vi.fn()} />);
    await user.click(screen.getByRole('button', { name: 'Reset model pricing' }));
    await user.click(screen.getByRole('button', { name: 'Reset' }));
    expect(await screen.findByRole('alert')).toHaveProperty('textContent', 'Request failed.');
    expect(screen.queryByText(/sk-private/)).toBeNull();
  });

  it('fails an invalid affinity read closed and redacts rejected cache mutations', async () => {
    mockedLoadAffinity.mockRejectedValueOnce(new Error('sk-private-statistics'));
    render(<AffinityCachePanel />);
    expect(await screen.findByRole('alert')).toHaveProperty('textContent', 'Unable to load affinity cache statistics.Retry');
    expect(screen.queryByRole('button', { name: 'Clear all affinity cache' })).toBeNull();
    expect(screen.queryByText(/sk-private/)).toBeNull();

    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'Retry' }));
    await screen.findByRole('button', { name: 'Clear all affinity cache' });
    mockedClearAffinity.mockRejectedValueOnce(new Error('sk-private-mutation'));
    await user.click(screen.getByRole('button', { name: 'Clear all affinity cache' }));
    await user.click(screen.getByRole('button', { name: 'Clear cache' }));
    expect(await screen.findByRole('alert')).toHaveProperty('textContent', 'Request failed.');
    expect(screen.queryByText(/sk-private/)).toBeNull();
  });

  it('tests only the stored deployment credential through the existing exact API', async () => {
    const user = userEvent.setup();
    render(<DeploymentConnectionPanel />);
    await user.click(screen.getByRole('button', { name: 'Test saved connection' }));
    await waitFor(() => expect(mockedTestDeployment).toHaveBeenCalledOnce());
    expect(mockedTestDeployment.mock.calls[0][0]).toBeInstanceOf(AbortSignal);
    expect(screen.getByRole('status').textContent).toBe('io.net connection successful.');
  });
});
