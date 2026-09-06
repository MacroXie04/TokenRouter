// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api, getData, postData, putData } from '../../shared/api/client';
import { PrefillGroupPanel } from './PrefillGroupPanel';
vi.mock('react-i18next', () => {
  const t = (key: string, values?: Record<string, unknown>) => key.replace(/\{\{(\w+)\}\}/g,
    (token, name: string) => values?.[name] === undefined ? token : String(values[name]));
  return { useTranslation: () => ({ t }) };
});
vi.mock('../../shared/api/client', () => ({ api: { delete: vi.fn() }, getData: vi.fn(), postData: vi.fn(), putData: vi.fn() }));
const mockedGet = vi.mocked(getData);
const mockedPost = vi.mocked(postData);
const mockedPut = vi.mocked(putData);
const mockedDelete = vi.mocked(api.delete);
let prefillResponse: unknown;
beforeEach(() => {
  vi.resetAllMocks();
  prefillResponse = [{ id: 7, name: 'default-models', type: 'model', items: ['gpt-4.1', 'o3'], description: 'Defaults', created_time: 1, updated_time: 2 }];
  mockedGet.mockImplementation(async () => prefillResponse);
  mockedDelete.mockResolvedValue({ data: { success: true, data: null } } as never);
  vi.spyOn(window, 'confirm').mockReturnValue(true);
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
describe('PrefillGroupPanel', () => {
  it('creates, edits, filters, validates, and deletes bounded prefill groups', async () => {
    let completeCreate: ((value: unknown) => void) | undefined;
    mockedPost.mockImplementation(() => new Promise((resolve) => { completeCreate = resolve; }));
    mockedPut.mockResolvedValue({});
    render(<PrefillGroupPanel />);
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
    render(<PrefillGroupPanel />);
    expect(await screen.findByText('Could not load prefill groups')).toBeTruthy();
    expect(screen.queryByText('sk-private')).toBeNull();
    expect(screen.getByText('No prefill groups.')).toBeTruthy();
  });
});
