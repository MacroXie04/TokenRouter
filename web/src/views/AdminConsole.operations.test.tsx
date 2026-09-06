// @vitest-environment jsdom

import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api, getData, postData, putData, type User } from '../api';
import {
  createWaffoPancakeSubscriptionProduct,
  listWaffoPancakeSubscriptionProducts,
} from '../features/wallet/waffo-pancake-admin-api';
import { AdminConsole } from './AdminConsole';

vi.mock('react-i18next', () => {
  const t = (key: string, values?: Record<string, unknown>) => key.replace(/\{\{(\w+)\}\}/g, (token, name: string) => (
    values?.[name] === undefined ? token : String(values[name])
  ));
  return { useTranslation: () => ({ t }) };
});

vi.mock('../api', () => ({
  api: { post: vi.fn(), put: vi.fn(), delete: vi.fn() },
  getData: vi.fn(),
  postData: vi.fn(),
  putData: vi.fn(),
}));

vi.mock('../features/wallet/waffo-pancake-admin-api', () => ({
  createWaffoPancakePair: vi.fn(),
  createWaffoPancakeSubscriptionProduct: vi.fn(),
  listWaffoPancakeCatalog: vi.fn(),
  listWaffoPancakeSubscriptionProducts: vi.fn(),
  saveWaffoPancakeConfig: vi.fn(),
}));

const mockedGet = vi.mocked(getData);
const mockedPost = vi.mocked(postData);
const mockedPut = vi.mocked(putData);
const mockedDelete = vi.mocked(api.delete);
const mockedCreatePancakeProduct = vi.mocked(createWaffoPancakeSubscriptionProduct);
const mockedListPancakeProducts = vi.mocked(listWaffoPancakeSubscriptionProducts);

const rootUser = {
  id: 1,
  username: 'root',
  display_name: 'Root',
  role: 100,
  group: 'default',
  quota: 0,
  used_quota: 0,
  request_count: 0,
} as User;

const adminUser = { ...rootUser, id: 2, username: 'admin', role: 10 };

let optionsResponse: unknown[];
let affinityResponse: unknown;
let prefillResponse: unknown;
let failingRead: string | undefined;

function installDefaultReads() {
  mockedGet.mockImplementation(async (path: string) => {
    if (path === failingRead) throw new Error('private unrelated read failure');
    switch (path) {
      case '/dashboard/stats': return { user_count: 0, token_count: 0, channel_count: 0, request_count: 0 };
      case '/option/': return optionsResponse;
      case '/log': return { items: [] };
      case '/ability': return [];
      case '/subscription/admin/plans': return [];
      case '/token': return { items: [] };
      case '/models': return [];
      case '/instance': return [];
      case '/option/channel_affinity_cache': return affinityResponse;
      case '/prefill_group/': return prefillResponse;
      default: throw new Error(`unexpected endpoint ${path}`);
    }
  });
}

function renderAdmin(initialTab: 'channels' | 'options' | 'prefill' | 'plans', user = rootUser, settingsPath?: string) {
  return render(<AdminConsole user={user} initialTab={initialTab} settingsPath={settingsPath} onLogout={vi.fn()} />);
}

beforeEach(() => {
  vi.resetAllMocks();
  failingRead = undefined;
  optionsResponse = [
    { key: 'payment_setting.compliance_confirmed', value: 'false' },
    { key: 'payment_setting.compliance_terms_version', value: '' },
    { key: 'payment_setting.compliance_confirmed_at', value: '0' },
  ];
  affinityResponse = {
    enabled: true,
    total: 3,
    unknown: 0,
    by_rule_name: { sticky: 2, regional: 1 },
    cache_capacity: 100,
    cache_algo: 'lru',
  };
  prefillResponse = [{
    id: 7,
    name: 'default-models',
    type: 'model',
    items: ['gpt-4.1', 'o3'],
    description: 'Defaults',
    created_time: 1,
    updated_time: 2,
  }];
  installDefaultReads();
  mockedPut.mockResolvedValue(undefined);
  mockedPost.mockResolvedValue(undefined);
  mockedDelete.mockResolvedValue({ data: { success: true, data: { deleted: 2 } } } as never);
  mockedListPancakeProducts.mockResolvedValue({ storeId: 'STO_AbCdEfGhIjKlMnOpQrStUv', products: [{
    id: 'PROD_AbCdEfGhIjKlMnOpQrStUv', name: 'Existing product', status: 'active',
  }] });
  mockedCreatePancakeProduct.mockResolvedValue({
    id: 'PROD_ZyXwVuTsRqPoNmLkJiHgFe', name: 'Pro plan', status: '',
  });
  vi.spyOn(window, 'confirm').mockReturnValue(true);
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('AdminConsole root operations', () => {
  it('passes effective channel read denial through without issuing the protected ability read', async () => {
    const deniedAdmin: User = {
      ...adminUser,
      permissions: {
        admin_permissions: {
          channel: {
            read: false,
            operate: true,
            write: true,
            sensitive_write: true,
            secret_view: false,
          },
        },
      },
    };

    renderAdmin('channels', deniedAdmin);
    expect(screen.getByRole('alert').textContent).toBe('Your account does not have permission to view this page.');
    await waitFor(() => expect(mockedGet).toHaveBeenCalledWith('/dashboard/stats'));
    expect(mockedGet).not.toHaveBeenCalledWith('/ability');
    expect(screen.queryByRole('button', { name: 'Channels' })).toBeNull();
  });

  it('mints and binds a dedicated Pancake product when creating a subscription plan', async () => {
    renderAdmin('plans');
    const user = userEvent.setup();
    expect(await screen.findByRole('option', { name: /Existing product/ })).toBeTruthy();

    await user.type(screen.getByLabelText('Title'), 'Pro plan');
    const price = screen.getByLabelText('Price (USD)');
    await user.clear(price);
    await user.type(price, '19.95');
    await user.click(screen.getByRole('button', { name: 'Create Pancake product from this plan' }));
    expect(mockedCreatePancakeProduct).toHaveBeenCalledWith({ name: 'Pro plan', amount: '19.95' });
    expect(await screen.findByText('Waffo Pancake plan product created.')).toBeTruthy();
    expect((screen.getByLabelText('Waffo Pancake product') as HTMLSelectElement).value)
      .toBe('PROD_ZyXwVuTsRqPoNmLkJiHgFe');

    await user.click(screen.getByRole('button', { name: 'Add plan' }));
    await waitFor(() => expect(mockedPost).toHaveBeenCalledWith('/subscription/plan', {
      title: 'Pro plan',
      price_amount: '19.95',
      total_amount: 1000000,
      duration_unit: 'month',
      duration_value: 1,
      enabled: true,
      waffo_pancake_product_id: 'PROD_ZyXwVuTsRqPoNmLkJiHgFe',
    }));
  });

  it('does not fetch or render root settings for an ordinary administrator', async () => {
    renderAdmin('options', adminUser);
    await waitFor(() => expect(mockedGet).toHaveBeenCalledWith('/dashboard/stats'));
    expect(mockedGet).not.toHaveBeenCalledWith('/option/');
    expect(screen.queryByRole('heading', { name: 'Payment compliance' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Options' })).toBeNull();
  });

  it('keeps root settings usable when an unrelated console resource fails', async () => {
    optionsResponse = [{ key: 'SystemName', value: 'Independent settings' }];
    failingRead = '/ability';
    renderAdmin('options', rootUser, 'site/system-info');

    const siteName = await screen.findByLabelText('Site name') as HTMLInputElement;
    await waitFor(() => expect(siteName.value).toBe('Independent settings'));
    expect(screen.queryByText(/private unrelated read failure/)).toBeNull();
  });

  it('confirms payment compliance and resets pricing with exact requests and refreshed state', async () => {
    mockedPost.mockImplementation(async (path: string) => {
      if (path === '/option/payment_compliance') {
        return { confirmed: true, terms_version: 'v1', confirmed_at: 12, confirmed_by: 1 };
      }
      if (path === '/option/rest_model_ratio') return undefined;
      throw new Error('unexpected write');
    });
    const view = renderAdmin('options', rootUser, 'billing/payment');
    const user = userEvent.setup();

    const compliance = (await screen.findByRole('heading', { name: 'Payment compliance' })).parentElement;
    expect(compliance).not.toBeNull();
    const complianceUI = within(compliance as HTMLElement);
    const confirmButton = complianceUI.getByRole('button', { name: 'Confirm compliance' });
    expect((confirmButton as HTMLButtonElement).disabled).toBe(true);
    await user.click(complianceUI.getByRole('checkbox'));
    await user.click(confirmButton);
    await waitFor(() => expect(mockedPost).toHaveBeenCalledWith('/option/payment_compliance', { confirmed: true }));
    expect(await complianceUI.findByText('Confirmed terms v1.')).toBeTruthy();

    view.rerender(<AdminConsole user={rootUser} initialTab="options" settingsPath="billing/model-pricing" onLogout={vi.fn()} />);
    const pricing = (await screen.findByRole('button', { name: 'Reset model pricing' })).parentElement;
    expect(pricing).not.toBeNull();
    await user.click(within(pricing as HTMLElement).getByRole('button', { name: 'Reset model pricing' }));
    expect(window.confirm).toHaveBeenCalledWith('Reset all model prices to the built-in TokenRouter defaults?');
    await waitFor(() => expect(mockedPost).toHaveBeenCalledWith('/option/rest_model_ratio'));
    expect(await within(pricing as HTMLElement).findByText('Model pricing defaults restored.')).toBeTruthy();
    expect(mockedGet.mock.calls.filter(([path]) => path === '/option/').length).toBeGreaterThanOrEqual(3);
  });

  it('rejects malformed compliance responses and redacts backend failures', async () => {
    mockedPost.mockImplementation(async (path: string) => {
      if (path === '/option/payment_compliance') return { confirmed: true, terms_version: 'future' };
      throw new Error('private backend detail sk-live-secret');
    });
    const view = renderAdmin('options', rootUser, 'billing/payment');
    const user = userEvent.setup();
    const compliance = (await screen.findByRole('heading', { name: 'Payment compliance' })).parentElement as HTMLElement;
    await user.click(within(compliance).getByRole('checkbox'));
    await user.click(within(compliance).getByRole('button', { name: 'Confirm compliance' }));
    expect(await within(compliance).findByText('Confirmation failed')).toBeTruthy();
    expect(screen.queryByText(/sk-live-secret/)).toBeNull();

    view.rerender(<AdminConsole user={rootUser} initialTab="options" settingsPath="billing/model-pricing" onLogout={vi.fn()} />);
    const pricing = (await screen.findByRole('button', { name: 'Reset model pricing' })).parentElement as HTMLElement;
    await user.click(within(pricing).getByRole('button', { name: 'Reset model pricing' }));
    expect(await within(pricing).findByText('Pricing reset failed')).toBeTruthy();
    expect(screen.queryByText(/private backend detail/)).toBeNull();
  });

  it('validates affinity statistics and clears one rule or the entire cache with confirmation', async () => {
    renderAdmin('options', rootUser, 'models/channel-affinity');
    const user = userEvent.setup();
    const panel = (await screen.findByRole('heading', { name: 'Channel affinity cache' })).parentElement as HTMLElement;
    expect(within(panel).getByText('Enabled · 3 / 100 entries · lru')).toBeTruthy();
    expect(within(panel).getByText('sticky: 2')).toBeTruthy();

    const ruleButtons = within(panel).getAllByRole('button', { name: 'Clear rule' });
    await user.click(ruleButtons[0]);
    await waitFor(() => expect(mockedDelete).toHaveBeenCalledWith('/option/channel_affinity_cache', { params: { rule_name: 'sticky' } }));
    expect(await within(panel).findByText('Cleared 2 affinity cache entries.')).toBeTruthy();

    await user.click(within(panel).getByRole('button', { name: 'Clear all affinity entries' }));
    await waitFor(() => expect(mockedDelete).toHaveBeenCalledWith('/option/channel_affinity_cache', { params: { all: true } }));
    expect(window.confirm).toHaveBeenCalledWith('Clear all channel-affinity entries?');
  });

  it('fails malformed affinity data closed without echoing private details', async () => {
    affinityResponse = { enabled: true, total: 5, unknown: 6, by_rule_name: {}, cache_capacity: 4, cache_algo: 'secret-sk-value' };
    renderAdmin('options', rootUser, 'models/channel-affinity');
    const panel = (await screen.findByRole('heading', { name: 'Channel affinity cache' })).parentElement as HTMLElement;
    expect(await within(panel).findByText('Could not load affinity cache')).toBeTruthy();
    expect(within(panel).queryByText(/secret-sk-value/)).toBeNull();
    expect((within(panel).getByRole('button', { name: 'Clear all affinity entries' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('rejects malformed affinity mutation responses without rendering their contents', async () => {
    mockedDelete.mockResolvedValue({ data: { success: true, data: { deleted: 'sk-private-result' } } } as never);
    renderAdmin('options', rootUser, 'models/channel-affinity');
    const user = userEvent.setup();
    const panel = (await screen.findByRole('heading', { name: 'Channel affinity cache' })).parentElement as HTMLElement;
    await user.click(within(panel).getByRole('button', { name: 'Clear all affinity entries' }));
    expect(await within(panel).findByText('Cache clear failed')).toBeTruthy();
    expect(within(panel).queryByText(/sk-private-result/)).toBeNull();
  });

  it('creates, edits, filters, validates, and deletes bounded prefill groups', async () => {
    let completeCreate: ((value: unknown) => void) | undefined;
    mockedPost.mockImplementation(() => new Promise((resolve) => { completeCreate = resolve; }));
    mockedPut.mockResolvedValue({});
    renderAdmin('prefill');
    const user = userEvent.setup();
    expect(await screen.findByText('default-models')).toBeTruthy();

    await user.selectOptions(screen.getByLabelText('Filter by type'), 'model');
    await waitFor(() => expect(mockedGet).toHaveBeenCalledWith('/prefill_group/', { type: 'model' }));

    await user.click(screen.getByRole('button', { name: 'Edit' }));
    expect((screen.getByLabelText('Name') as HTMLInputElement).value).toBe('default-models');
    expect((screen.getByLabelText('Items (one per line or comma-separated)') as HTMLTextAreaElement).value).toBe('gpt-4.1\no3');
    await user.clear(screen.getByLabelText('Description'));
    await user.type(screen.getByLabelText('Description'), 'Updated');
    await user.click(screen.getByRole('button', { name: 'Update group' }));
    await waitFor(() => expect(mockedPut).toHaveBeenCalledWith('/prefill_group/', {
      id: 7,
      name: 'default-models',
      type: 'model',
      items: ['gpt-4.1', 'o3'],
      description: 'Updated',
    }));
    expect(await screen.findByText('Prefill group updated.')).toBeTruthy();

    await user.clear(screen.getByLabelText('Name'));
    await user.type(screen.getByLabelText('Name'), 'endpoints');
    await user.selectOptions(screen.getByLabelText('Type'), 'endpoint');
    fireEvent.change(screen.getByLabelText('Endpoint JSON'), { target: { value: '{' } });
    await user.click(screen.getByRole('button', { name: 'Create group' }));
    expect(await screen.findByText('Endpoint items must contain valid JSON.')).toBeTruthy();
    expect(mockedPost).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText('Endpoint JSON'), { target: { value: '{"path":"/v1/chat/completions"}' } });
    await user.click(screen.getByRole('button', { name: 'Create group' }));
    expect(await screen.findByRole('button', { name: 'Saving…' })).toBeTruthy();
    expect((screen.getByRole('button', { name: 'Saving…' }) as HTMLButtonElement).disabled).toBe(true);
    expect(mockedPost).toHaveBeenCalledWith('/prefill_group/', {
      id: undefined,
      name: 'endpoints',
      type: 'endpoint',
      items: '{"path":"/v1/chat/completions"}',
      description: '',
    });
    await act(async () => { completeCreate?.({}); });
    expect(await screen.findByText('Prefill group created.')).toBeTruthy();

    await user.click(screen.getByRole('button', { name: 'Delete' }));
    expect(window.confirm).toHaveBeenCalledWith('Delete prefill group “default-models”?');
    await waitFor(() => expect(mockedDelete).toHaveBeenCalledWith('/prefill_group/7'));
    expect(await screen.findByText('Prefill group deleted.')).toBeTruthy();
  });

  it('rejects malformed prefill responses and does not render server-controlled secrets', async () => {
    prefillResponse = [{ id: 7, name: 'sk-private', type: 'invalid', items: [], created_time: 1, updated_time: 1 }];
    renderAdmin('prefill');
    expect(await screen.findByText('Could not load prefill groups')).toBeTruthy();
    expect(screen.queryByText('sk-private')).toBeNull();
    expect(screen.getByText('No prefill groups.')).toBeTruthy();
  });
});
