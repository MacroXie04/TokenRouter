// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  checkDeploymentName,
  createDeployment,
  createModel,
  createVendor,
  deleteDeployment,
  deleteModel,
  deleteVendor,
  extendDeployment,
  estimateDeploymentPrice,
  getDeployment,
  getDeploymentContainer,
  getModel,
  getVendor,
  loadDeploymentContainers,
  loadDeploymentHardware,
  loadDeploymentLogs,
  loadDeploymentReplicas,
  loadDeploymentSettings,
  loadDeployments,
  loadMissingModels,
  loadModels,
  loadVendors,
  previewUpstream,
  renameDeployment,
  setModelStatus,
  syncUpstream,
  testDeploymentConnection,
  updateDeployment,
  updateModel,
  updateVendor,
  type DeploymentPage,
  type DeploymentSummary,
  type MetadataPage,
  type ModelMetadata,
  type VendorMetadata,
  type VendorPage,
} from './models-api';
import { ModelsView } from './ModelsView';

vi.mock('react-i18next', () => {
  const t = (key: string, values?: Record<string, unknown>) => key.replace(
    /\{\{(\w+)\}\}/gu,
    (_match, name: string) => String(values?.[name] ?? ''),
  );
  return { useTranslation: () => ({ t }) };
});

vi.mock('./models-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./models-api')>();
  return {
    ...actual,
    checkDeploymentName: vi.fn(),
    createDeployment: vi.fn(),
    createModel: vi.fn(),
    createVendor: vi.fn(),
    deleteDeployment: vi.fn(),
    deleteModel: vi.fn(),
    deleteVendor: vi.fn(),
    extendDeployment: vi.fn(),
    estimateDeploymentPrice: vi.fn(),
    getDeployment: vi.fn(),
    getDeploymentContainer: vi.fn(),
    getModel: vi.fn(),
    getVendor: vi.fn(),
    loadDeploymentContainers: vi.fn(),
    loadDeploymentHardware: vi.fn(),
    loadDeploymentLogs: vi.fn(),
    loadDeploymentReplicas: vi.fn(),
    loadDeploymentSettings: vi.fn(),
    loadDeployments: vi.fn(),
    loadMissingModels: vi.fn(),
    loadModels: vi.fn(),
    loadVendors: vi.fn(),
    previewUpstream: vi.fn(),
    renameDeployment: vi.fn(),
    setModelStatus: vi.fn(),
    syncUpstream: vi.fn(),
    testDeploymentConnection: vi.fn(),
    updateDeployment: vi.fn(),
    updateModel: vi.fn(),
    updateVendor: vi.fn(),
  };
});

const mockedCreateDeployment = vi.mocked(createDeployment);
const mockedCheckName = vi.mocked(checkDeploymentName);
const mockedCreateModel = vi.mocked(createModel);
const mockedCreateVendor = vi.mocked(createVendor);
const mockedDeleteDeployment = vi.mocked(deleteDeployment);
const mockedDeleteModel = vi.mocked(deleteModel);
const mockedDeleteVendor = vi.mocked(deleteVendor);
const mockedExtendDeployment = vi.mocked(extendDeployment);
const mockedEstimatePrice = vi.mocked(estimateDeploymentPrice);
const mockedGetDeployment = vi.mocked(getDeployment);
const mockedGetContainer = vi.mocked(getDeploymentContainer);
const mockedGetModel = vi.mocked(getModel);
const mockedGetVendor = vi.mocked(getVendor);
const mockedLoadContainers = vi.mocked(loadDeploymentContainers);
const mockedLoadHardware = vi.mocked(loadDeploymentHardware);
const mockedLoadLogs = vi.mocked(loadDeploymentLogs);
const mockedLoadReplicas = vi.mocked(loadDeploymentReplicas);
const mockedLoadSettings = vi.mocked(loadDeploymentSettings);
const mockedLoadDeployments = vi.mocked(loadDeployments);
const mockedLoadMissing = vi.mocked(loadMissingModels);
const mockedLoadModels = vi.mocked(loadModels);
const mockedLoadVendors = vi.mocked(loadVendors);
const mockedPreview = vi.mocked(previewUpstream);
const mockedRename = vi.mocked(renameDeployment);
const mockedStatus = vi.mocked(setModelStatus);
const mockedSync = vi.mocked(syncUpstream);
const mockedTestConnection = vi.mocked(testDeploymentConnection);
const mockedUpdateDeployment = vi.mocked(updateDeployment);
const mockedUpdateModel = vi.mocked(updateModel);
const mockedUpdateVendor = vi.mocked(updateVendor);

const model: ModelMetadata = {
  id: 7,
  modelName: 'gpt-4o',
  description: 'Fast model',
  icon: 'openai',
  tags: 'chat',
  vendorId: 3,
  endpoints: '["chat"]',
  status: 1,
  syncOfficial: 1,
  createdTime: 1_780_000_000,
  updatedTime: 1_780_000_100,
  nameRule: 0,
  boundChannels: [],
  enableGroups: ['default'],
  matchedModels: [],
  matchedCount: 0,
  supportedEndpointTypes: ['openai'],
};

const vendor: VendorMetadata = {
  id: 3,
  name: 'OpenAI',
  description: 'Provider',
  icon: 'openai',
  status: 1,
  createdTime: 1_780_000_000,
  updatedTime: 1_780_000_100,
};

const deployment: DeploymentSummary = {
  id: 'cluster:abc',
  name: 'production-chat',
  status: 'running',
  provider: 'io.net',
  timeRemaining: '2 hours',
  hardwareInfo: 'NVIDIA H100 x2',
  hardwareName: 'H100',
  brandName: 'NVIDIA',
  hardwareQuantity: 2,
  completedPercent: 25,
  computeMinutesServed: 20,
  computeMinutesRemaining: 120,
  createdAt: 1_780_000_000,
};

function metadataPage(items = [model], total = 21): MetadataPage {
  return { items, total, page: 1, pageSize: 20, vendorCounts: { 3: total } };
}

function vendorPage(items = [vendor]): VendorPage {
  return { items, total: items.length, page: 1, pageSize: 100 };
}

function deploymentPage(items = [deployment], total = 1): DeploymentPage {
  return { items, total, page: 1, pageSize: 20, statusCounts: { all: total, running: items.length } };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

beforeEach(() => {
  vi.resetAllMocks();
  mockedLoadModels.mockResolvedValue(metadataPage());
  mockedLoadVendors.mockResolvedValue(vendorPage());
  mockedGetModel.mockResolvedValue(model);
  mockedGetVendor.mockResolvedValue(vendor);
  mockedCreateModel.mockResolvedValue(model);
  mockedUpdateModel.mockResolvedValue(model);
  mockedCreateVendor.mockResolvedValue(vendor);
  mockedUpdateVendor.mockResolvedValue(vendor);
  mockedStatus.mockResolvedValue();
  mockedDeleteModel.mockResolvedValue();
  mockedDeleteVendor.mockResolvedValue();
  mockedLoadMissing.mockResolvedValue(['new-model']);
  mockedPreview.mockResolvedValue({
    missing: ['new-model'], conflicts: [{ modelName: 'gpt-4o', fields: ['description'] }],
  });
  mockedSync.mockResolvedValue({ createdModels: 1, createdVendors: 0, updatedModels: 1, skippedModels: [] });
  mockedLoadSettings.mockResolvedValue({ provider: 'io.net', enabled: true, configured: true, canConnect: true });
  mockedTestConnection.mockResolvedValue();
  mockedLoadDeployments.mockResolvedValue(deploymentPage());
  mockedGetDeployment.mockResolvedValue({
    id: deployment.id,
    status: 'running',
    hardwareId: 9,
    hardwareName: 'H100',
    brandName: 'NVIDIA',
    totalGPUs: 2,
    GPUsPerContainer: 1,
    totalContainers: 2,
    completedPercent: 25,
    computeMinutesServed: 20,
    computeMinutesRemaining: 120,
    amountPaid: 1.25,
    createdAt: 1_780_000_000,
  });
  const container = {
    containerId: 'worker:1', deviceId: 'device-1', status: 'running', hardware: 'H100', brandName: 'NVIDIA',
    createdAt: 1_780_000_000, uptimePercent: 99, GPUsPerContainer: 1,
    publicURL: 'https://worker.example.test/', events: [{ time: 1_780_000_100, message: 'container started' }],
  };
  mockedLoadContainers.mockResolvedValue([container]);
  mockedGetContainer.mockResolvedValue(container);
  mockedLoadLogs.mockResolvedValue('container ready');
  mockedCreateDeployment.mockResolvedValue();
  mockedCheckName.mockResolvedValue(true);
  mockedLoadHardware.mockResolvedValue({
    items: [{ id: 9, name: 'H100', brandName: 'NVIDIA', maxGPUs: 8, available: true, availableCount: 12 }],
    total: 1,
    totalAvailable: 12,
  });
  mockedLoadReplicas.mockResolvedValue([
    { locationId: 4, locationName: 'California', hardwareId: 9, availableCount: 6, maxGPUs: 1 },
    { locationId: 5, locationName: 'Virginia', hardwareId: 9, availableCount: 4, maxGPUs: 1 },
  ]);
  mockedEstimatePrice.mockResolvedValue({
    estimatedCost: 5, currency: 'usdc', computeCost: 5, networkCost: 0, storageCost: 0, totalCost: 5, hourlyRate: 2.5,
  });
  mockedUpdateDeployment.mockResolvedValue();
  mockedRename.mockResolvedValue();
  mockedExtendDeployment.mockResolvedValue();
  mockedDeleteDeployment.mockResolvedValue();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('ModelsView metadata workflows', () => {
  it('renders the registry, submits exact filters, paginates, and navigates sections', async () => {
    const onNavigate = vi.fn();
    render(<ModelsView section="metadata" role={10} onNavigate={onNavigate} />);
    const user = userEvent.setup();
    expect(await screen.findByText('gpt-4o')).toBeTruthy();
    expect(screen.getByText('Fast model')).toBeTruthy();
    expect(screen.getAllByText('OpenAI').length).toBeGreaterThan(0);

    const search = screen.getByRole('search', { name: 'Search model metadata' });
    await user.type(within(search).getByLabelText('Name, description, or tag'), ' gpt ');
    await user.selectOptions(within(search).getByLabelText('Vendor'), '3');
    await user.selectOptions(within(search).getByLabelText('Status'), 'enabled');
    await user.selectOptions(within(search).getByLabelText('Official sync'), 'yes');
    await user.click(within(search).getByRole('button', { name: 'Apply filters' }));
    await waitFor(() => expect(mockedLoadModels).toHaveBeenLastCalledWith({
      keyword: 'gpt', vendor: '3', status: 'enabled', syncOfficial: 'yes', page: 1, pageSize: 20,
    }, expect.any(AbortSignal)));

    await user.click(screen.getByRole('button', { name: 'Next' }));
    await waitFor(() => expect(mockedLoadModels).toHaveBeenLastCalledWith(expect.objectContaining({ page: 2 }), expect.any(AbortSignal)));
    await user.click(screen.getByRole('button', { name: 'Deployments' }));
    expect(onNavigate).toHaveBeenCalledWith('/models/deployments');
  });

  it('loads fresh edit records, validates endpoints, saves models, toggles status, and confirms deletion', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValue(true);
    render(<ModelsView section="metadata" role={10} />);
    const user = userEvent.setup();
    const row = (await screen.findByText('gpt-4o')).closest('tr') as HTMLTableRowElement;

    await user.click(within(row).getByRole('button', { name: 'Edit' }));
    expect(mockedGetModel).toHaveBeenCalledWith(7, expect.any(AbortSignal));
    const form = await screen.findByRole('form', { name: 'Edit model' });
    const name = within(form).getByLabelText('Model name');
    await user.clear(name);
    await user.type(name, 'gpt-4.1');
    const endpoints = within(form).getByLabelText('Endpoints JSON');
    await user.clear(endpoints);
    await user.type(endpoints, '{{}bad');
    await user.click(within(form).getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('Endpoints must be valid JSON.')).toBeTruthy();
    expect(mockedUpdateModel).not.toHaveBeenCalled();
    fireEvent.change(endpoints, { target: { value: '[]' } });
    await user.click(within(form).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(mockedUpdateModel).toHaveBeenCalledWith(expect.objectContaining({ id: 7, modelName: 'gpt-4.1', endpoints: '[]' })));

    await user.click(screen.getByRole('button', { name: 'Create model' }));
    const createForm = await screen.findByRole('form', { name: 'Create model' });
    await user.type(within(createForm).getByLabelText('Model name'), 'claude-sonnet');
    await user.click(within(createForm).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(mockedCreateModel).toHaveBeenCalledWith(expect.objectContaining({
      modelName: 'claude-sonnet', endpoints: '[]', status: 1,
    })));

    await user.click(within(row).getByRole('button', { name: 'Disable' }));
    await waitFor(() => expect(mockedStatus).toHaveBeenCalledWith(7, 0));
    await user.click(within(row).getByRole('button', { name: 'Delete' }));
    expect(mockedDeleteModel).not.toHaveBeenCalled();
    await user.click(within(row).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(mockedDeleteModel).toHaveBeenCalledWith(7));
    expect(confirm).toHaveBeenNthCalledWith(1, 'Delete model “gpt-4o”?');
  });

  it('supports vendor search and CRUD with confirmation', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    render(<ModelsView section="metadata" role={100} />);
    const user = userEvent.setup();
    await screen.findByText('gpt-4o');
    const vendorSearch = screen.getByRole('search', { name: 'Search vendors' });
    await user.type(within(vendorSearch).getByLabelText('Vendor keyword'), ' open ');
    await user.click(within(vendorSearch).getByRole('button', { name: 'Search' }));
    await waitFor(() => expect(mockedLoadVendors).toHaveBeenLastCalledWith(expect.any(AbortSignal), 'open', 1, 100));

    const vendorTable = screen.getByRole('table', { name: 'Vendors' });
    await user.click(within(vendorTable).getByRole('button', { name: 'Edit' }));
    expect(mockedGetVendor).toHaveBeenCalledWith(3, expect.any(AbortSignal));
    const edit = await screen.findByRole('form', { name: 'Edit vendor' });
    const name = within(edit).getByLabelText('Vendor name');
    await user.clear(name);
    await user.type(name, 'OpenAI Inc.');
    await user.click(within(edit).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(mockedUpdateVendor).toHaveBeenCalledWith(expect.objectContaining({ id: 3, name: 'OpenAI Inc.' })));

    await user.click(screen.getByRole('button', { name: 'Create vendor' }));
    const create = await screen.findByRole('form', { name: 'Create vendor' });
    await user.type(within(create).getByLabelText('Vendor name'), 'Anthropic');
    await user.click(within(create).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(mockedCreateVendor).toHaveBeenCalledWith(expect.objectContaining({ name: 'Anthropic' })));

    await user.click(within(vendorTable).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(mockedDeleteVendor).toHaveBeenCalledWith(3));
  });

  it('paginates the bounded vendor catalog and keeps discovered vendors available as filters', async () => {
    const secondVendor = { ...vendor, id: 4, name: 'Anthropic' };
    mockedLoadVendors
      .mockResolvedValueOnce({ items: [vendor], total: 101, page: 1, pageSize: 100 })
      .mockResolvedValueOnce({ items: [secondVendor], total: 101, page: 2, pageSize: 100 });
    render(<ModelsView section="metadata" role={10} />);
    const user = userEvent.setup();
    await screen.findByText('gpt-4o');
    const pagination = screen.getByRole('navigation', { name: 'Vendor pages' });
    await user.click(within(pagination).getByRole('button', { name: 'Next' }));
    await waitFor(() => expect(mockedLoadVendors).toHaveBeenLastCalledWith(expect.any(AbortSignal), '', 2, 100));
    expect(await within(screen.getByRole('table', { name: 'Vendors' })).findByText('Anthropic')).toBeTruthy();
    expect(screen.getByRole('combobox', { name: 'Vendor' }).querySelector('option[value="3"]')).toBeTruthy();
    expect(screen.getByRole('combobox', { name: 'Vendor' }).querySelector('option[value="4"]')).toBeTruthy();
  });

  it('shows missing models and requires approval before applying a bounded sync preview', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValue(true);
    render(<ModelsView section="metadata" role={10} />);
    const user = userEvent.setup();
    await screen.findByText('gpt-4o');
    await user.click(screen.getByRole('button', { name: 'Show missing models' }));
    expect(await screen.findByRole('dialog', { name: 'Missing models' })).toBeTruthy();
    expect(screen.getByText('new-model')).toBeTruthy();
    await user.click(within(screen.getByRole('dialog', { name: 'Missing models' })).getAllByRole('button', { name: 'Close' }).at(-1) as HTMLButtonElement);

    await user.selectOptions(screen.getByLabelText('Sync language'), 'en');
    await user.click(screen.getByRole('button', { name: 'Preview upstream sync' }));
    const dialog = await screen.findByRole('dialog', { name: 'Upstream sync preview' });
    expect(mockedPreview).toHaveBeenCalledWith('en', expect.any(AbortSignal));
    expect(within(dialog).getByText('Unselected fields keep their local values.')).toBeTruthy();
    await user.click(within(dialog).getByRole('checkbox', { name: 'description' }));
    await user.click(within(dialog).getByRole('button', { name: 'Apply sync' }));
    expect(mockedSync).not.toHaveBeenCalled();
    await user.click(within(dialog).getByRole('button', { name: 'Apply sync' }));
    await waitFor(() => expect(mockedSync).toHaveBeenCalledWith('en', {
      missing: ['new-model'], conflicts: [{ modelName: 'gpt-4o', fields: ['description'] }],
    }));
    expect(confirm).toHaveBeenNthCalledWith(1, 'Apply this upstream sync?');
  });

  it('provides deterministic loading, empty, error, retry, and generic redacted error states', async () => {
    const pending = deferred<MetadataPage>();
    mockedLoadModels.mockReturnValueOnce(pending.promise);
    const { unmount } = render(<ModelsView section="metadata" role={10} />);
    expect(screen.getByText('Loading model metadata…').closest('[role="status"]')).toBeTruthy();
    unmount();

    mockedLoadModels.mockRejectedValueOnce(new Error('postgres://admin:secret@internal'));
    render(<ModelsView section="metadata" role={10} />);
    expect(await screen.findByText('Unable to load model metadata.')).toBeTruthy();
    expect(document.body.textContent).not.toContain('postgres://');
    mockedLoadModels.mockResolvedValueOnce(metadataPage([], 0));
    await userEvent.setup().click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByText('No models match these filters.')).toBeTruthy();
  });

  it('aborts stale and unmounted loads so older results cannot replace newer filters', async () => {
    const first = deferred<MetadataPage>();
    const second = deferred<MetadataPage>();
    const signals: AbortSignal[] = [];
    mockedLoadModels.mockImplementation((_query, signal) => {
      if (signal) signals.push(signal);
      return signals.length === 1 ? first.promise : second.promise;
    });
    const view = render(<ModelsView section="metadata" role={10} />);
    const user = userEvent.setup();
    const search = screen.getByRole('search', { name: 'Search model metadata' });
    await user.type(within(search).getByLabelText('Name, description, or tag'), 'new');
    await user.click(within(search).getByRole('button', { name: 'Apply filters' }));
    expect(signals[0].aborted).toBe(true);
    second.resolve(metadataPage([{ ...model, id: 8, modelName: 'new-model' }], 1));
    expect(await screen.findByText('new-model')).toBeTruthy();
    first.resolve(metadataPage([{ ...model, modelName: 'stale-model' }], 1));
    await Promise.resolve();
    expect(screen.queryByText('stale-model')).toBeNull();
    view.unmount();
    expect(signals.at(-1)?.aborted).toBe(true);
  });
});

describe('ModelsView deployment workflows', () => {
  it('fails closed before listing when disabled or unconfigured, and never requests or displays an API key', async () => {
    mockedLoadSettings.mockResolvedValueOnce({ provider: 'io.net', enabled: false, configured: true, canConnect: false });
    const first = render(<ModelsView section="deployments" role={10} />);
    expect(await screen.findByText('Model deployment is disabled')).toBeTruthy();
    expect(mockedTestConnection).not.toHaveBeenCalled();
    expect(mockedLoadDeployments).not.toHaveBeenCalled();
    first.unmount();

    mockedLoadSettings.mockResolvedValueOnce({ provider: 'io.net', enabled: true, configured: false, canConnect: false });
    render(<ModelsView section="deployments" role={10} />);
    expect(await screen.findByText('Model deployment is not configured')).toBeTruthy();
    expect(document.body.textContent).toContain('The key is never shown here.');
    expect(document.body.textContent).not.toMatch(/sk-|api_key/);
    expect(mockedLoadDeployments).not.toHaveBeenCalled();
  });

  it('checks settings and connection in order, then lists and searches deployments', async () => {
    const order: string[] = [];
    mockedLoadSettings.mockImplementation(async () => { order.push('settings'); return { provider: 'io.net', enabled: true, configured: true, canConnect: true }; });
    mockedTestConnection.mockImplementation(async () => { order.push('connection'); });
    mockedLoadDeployments.mockImplementation(async () => { order.push('list'); return deploymentPage(); });
    render(<ModelsView section="deployments" role={10} />);
    const user = userEvent.setup();
    expect(await screen.findByText('production-chat')).toBeTruthy();
    expect(order).toEqual(['settings', 'connection', 'list']);
    const search = screen.getByRole('search', { name: 'Search deployments' });
    await user.type(within(search).getByLabelText('Deployment name'), ' chat ');
    await user.selectOptions(within(search).getByLabelText('Status'), 'running');
    await user.click(within(search).getByRole('button', { name: 'Apply filters' }));
    await waitFor(() => expect(mockedLoadDeployments).toHaveBeenLastCalledWith({ keyword: 'chat', status: 'running', page: 1, pageSize: 20 }, expect.any(AbortSignal)));
  });

  it('supports redacted detail, container detail, safe public URL, and bounded logs workflows', async () => {
    render(<ModelsView section="deployments" role={100} />);
    const user = userEvent.setup();
    const row = (await screen.findByText('production-chat')).closest('tr') as HTMLTableRowElement;
    await user.click(within(row).getByRole('button', { name: 'Details' }));
    const details = await screen.findByRole('dialog', { name: 'Deployment details' });
    expect(within(details).getByText('cluster:abc')).toBeTruthy();
    expect(details.textContent).not.toMatch(/api.?key|secret/i);
    await user.click(within(details).getAllByRole('button', { name: 'Close' }).at(-1) as HTMLButtonElement);

    await user.click(within(row).getByRole('button', { name: 'Containers' }));
    const containersDialog = await screen.findByRole('dialog', { name: 'Containers for production-chat' });
    expect(await within(containersDialog).findByText('worker:1')).toBeTruthy();
    await user.click(within(containersDialog).getByRole('button', { name: 'Details' }));
    expect(await within(containersDialog).findByText('device-1')).toBeTruthy();
    expect(within(containersDialog).getByRole('table', { name: 'Container events' })).toBeTruthy();
    expect(within(containersDialog).getByText('container started')).toBeTruthy();
    expect(mockedGetContainer).toHaveBeenCalledWith('cluster:abc', 'worker:1', expect.any(AbortSignal));
    expect(within(containersDialog).getByRole('link', { name: 'Open public container URL' }).getAttribute('href')).toBe('https://worker.example.test/');
    await user.click(within(containersDialog).getByRole('button', { name: 'Logs' }));
    expect(await within(containersDialog).findByText('container ready')).toBeTruthy();
    expect(mockedLoadLogs).toHaveBeenCalledWith('cluster:abc', 'worker:1', expect.any(AbortSignal));
  });

  it('distinguishes retryable container failures from an empty container list', async () => {
    mockedLoadContainers
      .mockRejectedValueOnce(new Error('provider response with secret'))
      .mockResolvedValueOnce([{
        containerId: 'worker:1', deviceId: 'device-1', status: 'running', hardware: 'H100', brandName: 'NVIDIA',
        createdAt: 1_780_000_000, uptimePercent: 99, GPUsPerContainer: 1,
        publicURL: 'https://worker.example.test/', events: [],
      }]);
    render(<ModelsView section="deployments" role={10} />);
    const user = userEvent.setup();
    const row = (await screen.findByText('production-chat')).closest('tr') as HTMLTableRowElement;
    await user.click(within(row).getByRole('button', { name: 'Containers' }));
    const dialog = await screen.findByRole('dialog', { name: 'Containers for production-chat' });
    expect((await within(dialog).findByRole('alert')).textContent).toContain('Unable to load container data.');
    expect(within(dialog).queryByText('No containers found.')).toBeNull();
    expect(document.body.textContent).not.toContain('provider response with secret');
    await user.click(within(dialog).getByRole('button', { name: 'Retry' }));
    expect(await within(dialog).findByText('worker:1')).toBeTruthy();
    expect(mockedLoadContainers).toHaveBeenCalledTimes(2);
  });

  it('creates deployments without displaying secrets and serializes the mutation', async () => {
    const pending = deferred<void>();
    mockedCreateDeployment.mockReturnValueOnce(pending.promise);
    render(<ModelsView section="deployments" role={10} />);
    const user = userEvent.setup();
    await screen.findByText('production-chat');
    await user.click(screen.getByRole('button', { name: 'Create deployment' }));
    const form = await screen.findByRole('form', { name: 'Create deployment' });
    expect(await within(form).findByRole('option', { name: /NVIDIA H100/ })).toBeTruthy();
    await waitFor(() => expect(mockedLoadReplicas).toHaveBeenCalledWith(9, 1, expect.any(AbortSignal)));
    await user.type(within(form).getByLabelText('Deployment name'), 'new-cluster');
    await user.type(within(form).getByLabelText('Container image'), 'registry.example/model:v1');
    await user.click(await within(form).findByLabelText(/California/));
    await user.click(within(form).getByLabelText(/Virginia/));
    await user.selectOptions(within(form).getByLabelText('Billing currency'), 'iocoin');
    await user.click(within(form).getByText('Advanced configuration'));
    await user.type(within(form).getByLabelText('Registry secret'), 'registry-password');
    expect(document.body.textContent).not.toContain('registry-password');
    expect(await within(form).findByText('Name is available')).toBeTruthy();
    await user.click(within(form).getByRole('button', { name: 'Create deployment' }));
    await waitFor(() => expect(mockedCreateDeployment).toHaveBeenCalledWith(expect.objectContaining({
      name: 'new-cluster', image: 'registry.example/model:v1', hardwareId: 9, locationIds: [4, 5],
      trafficPort: 5_000, registrySecret: 'registry-password',
    })));
    await waitFor(() => expect(mockedCheckName).toHaveBeenCalledWith('new-cluster', expect.any(AbortSignal)));
    await waitFor(() => expect(mockedEstimatePrice).toHaveBeenCalledWith(expect.objectContaining({
      hardwareId: 9, locationIds: [4, 5], replicaCount: 1,
    }), 'iocoin', expect.any(AbortSignal)));
    const submit = within(form).getByRole('button', { name: 'Creating…' }) as HTMLButtonElement;
    expect(submit.disabled).toBe(true);
    await user.click(submit);
    expect(mockedCreateDeployment).toHaveBeenCalledTimes(1);
    pending.resolve();
    await waitFor(() => expect(screen.queryByRole('form', { name: 'Create deployment' })).toBeNull());
  });

  it('updates only entered deployment configuration and keeps secret values out of rendered notices', async () => {
    render(<ModelsView section="deployments" role={10} />);
    const user = userEvent.setup();
    const row = (await screen.findByText('production-chat')).closest('tr') as HTMLTableRowElement;
    await user.click(within(row).getByRole('button', { name: 'Edit configuration' }));
    const form = await screen.findByRole('form', { name: 'Update deployment' });
    await user.click(within(form).getByRole('button', { name: 'Update' }));
    expect((await within(form).findByRole('alert')).textContent).toContain('Enter at least one setting to update.');

    await user.type(within(form).getByLabelText('Container image'), 'registry.example/model:v2');
    await user.type(within(form).getByLabelText('Command'), 'serve');
    await user.type(within(form).getByLabelText('Registry secret'), 'replacement-secret');
    fireEvent.change(within(form).getByLabelText('Environment variables (JSON)'), { target: { value: '{"MODE":"fast"}' } });
    await user.click(within(form).getByRole('button', { name: 'Update' }));
    await waitFor(() => expect(mockedUpdateDeployment).toHaveBeenCalledWith('cluster:abc', {
      image: 'registry.example/model:v2', command: 'serve', registrySecret: 'replacement-secret',
      environmentVariables: { MODE: 'fast' },
    }));
    expect(await screen.findByText('Deployment updated.')).toBeTruthy();
    expect(document.body.textContent).not.toContain('replacement-secret');
  });

  it('retries create resources and price estimation and cancels the pending name check', async () => {
    mockedLoadHardware.mockRejectedValueOnce(new Error('provider internals'));
    mockedEstimatePrice.mockRejectedValueOnce(new Error('pricing internals'));
    let nameSignal: AbortSignal | undefined;
    mockedCheckName.mockImplementationOnce((_name, signal) => {
      nameSignal = signal;
      return new Promise(() => {});
    });
    render(<ModelsView section="deployments" role={10} />);
    const user = userEvent.setup();
    await screen.findByText('production-chat');
    await user.click(screen.getByRole('button', { name: 'Create deployment' }));
    const form = await screen.findByRole('form', { name: 'Create deployment' });
    expect(await within(form).findByText('Unable to load hardware.')).toBeTruthy();
    await user.click(within(form).getByRole('button', { name: 'Retry' }));
    expect(await within(form).findByRole('option', { name: /NVIDIA H100/ })).toBeTruthy();
    await user.click(await within(form).findByLabelText(/California/));
    expect(await within(form).findByText('Unable to estimate price.')).toBeTruthy();
    const estimate = within(form).getByText('Unable to estimate price.').closest('section') as HTMLElement;
    await user.click(within(estimate).getByRole('button', { name: 'Retry' }));
    expect(await within(form).findByText(/Estimated cost: 5.00 USDC/)).toBeTruthy();

    await user.type(within(form).getByLabelText('Deployment name'), 'pending-name');
    await waitFor(() => expect(mockedCheckName).toHaveBeenCalledWith('pending-name', expect.any(AbortSignal)));
    await user.click(within(form).getByRole('button', { name: 'Cancel' }));
    expect(nameSignal?.aborted).toBe(true);
    expect(document.body.textContent).not.toContain('provider internals');
  });

  it('renames, confirms extension and deletion, and reports provider failures without reflecting details', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValueOnce(true).mockReturnValueOnce(true);
    render(<ModelsView section="deployments" role={10} />);
    const user = userEvent.setup();
    const row = (await screen.findByText('production-chat')).closest('tr') as HTMLTableRowElement;
    await user.click(within(row).getByRole('button', { name: 'Rename' }));
    const renameForm = await screen.findByRole('form', { name: 'Rename deployment' });
    const name = within(renameForm).getByLabelText('New deployment name');
    await user.clear(name);
    await user.type(name, 'renamed-cluster');
    await user.click(within(renameForm).getByRole('button', { name: 'Rename' }));
    await waitFor(() => expect(mockedRename).toHaveBeenCalledWith('cluster:abc', 'renamed-cluster'));

    await user.click(within(row).getByRole('button', { name: 'Extend' }));
    let extendForm = await screen.findByRole('form', { name: 'Extend deployment' });
    await user.click(within(extendForm).getByRole('button', { name: 'Extend' }));
    expect(mockedExtendDeployment).not.toHaveBeenCalled();
    extendForm = screen.getByRole('form', { name: 'Extend deployment' });
    await user.click(within(extendForm).getByRole('button', { name: 'Extend' }));
    await waitFor(() => expect(mockedExtendDeployment).toHaveBeenCalledWith('cluster:abc', 1));

    mockedDeleteDeployment.mockRejectedValueOnce(new Error('Authorization: Bearer provider-secret'));
    await user.click(within(row).getByRole('button', { name: 'Delete' }));
    expect(await screen.findByText('The deployment operation could not be completed.')).toBeTruthy();
    expect(document.body.textContent).not.toContain('provider-secret');
    expect(confirm).toHaveBeenLastCalledWith('Delete deployment “production-chat”?');
  });

  it('shows settings, connection, list error/retry, empty, and abort states accessibly', async () => {
    const settings = deferred<{ provider: 'io.net'; enabled: boolean; configured: boolean; canConnect: boolean }>();
    const connection = deferred<void>();
    mockedLoadSettings.mockReturnValueOnce(settings.promise);
    mockedTestConnection.mockReturnValueOnce(connection.promise);
    const view = render(<ModelsView section="deployments" role={10} />);
    expect(screen.getByRole('status').textContent).toContain('Loading deployment settings…');
    settings.resolve({ provider: 'io.net', enabled: true, configured: true, canConnect: true });
    await waitFor(() => expect(screen.getByRole('status').textContent).toContain('Checking deployment connection…'));
    view.unmount();

    mockedTestConnection.mockRejectedValueOnce(new Error('secret upstream body'));
    render(<ModelsView section="deployments" role={10} />);
    expect(await screen.findByText('The deployment provider connection could not be verified.')).toBeTruthy();
    expect(document.body.textContent).not.toContain('secret upstream body');
    cleanup();

    mockedLoadDeployments.mockRejectedValueOnce(new Error('provider stack trace'));
    render(<ModelsView section="deployments" role={10} />);
    expect(await screen.findByText('Unable to load deployments.')).toBeTruthy();
    mockedLoadDeployments.mockResolvedValueOnce(deploymentPage([], 0));
    await userEvent.setup().click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByText('No deployments match these filters.')).toBeTruthy();
  });
});

describe('ModelsView authorization', () => {
  it('fails closed for non-administrators before any model or deployment request', () => {
    render(<ModelsView section="metadata" role={1} />);
    expect(screen.getByRole('alert').textContent).toContain('Administrator access required');
    expect(mockedLoadModels).not.toHaveBeenCalled();
    expect(mockedLoadVendors).not.toHaveBeenCalled();
    expect(mockedLoadSettings).not.toHaveBeenCalled();
  });
});
