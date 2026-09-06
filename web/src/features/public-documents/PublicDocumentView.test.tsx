// @vitest-environment jsdom

import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { getData } from '../../api';
import { MAX_DOCUMENT_CHARACTERS } from './content';
import { PublicDocumentView } from './PublicDocumentView';

vi.mock('react-i18next', () => {
  const t = (key: string) => key;
  return { useTranslation: () => ({ t }) };
});

vi.mock('../../api', () => ({
  getData: vi.fn(),
}));

const mockedGetData = vi.mocked(getData);

afterEach(() => {
  cleanup();
  mockedGetData.mockReset();
});

describe('PublicDocumentView', () => {
  it.each([
    ['about', '/about', '# About TokenRouter', 'About TokenRouter'],
    ['privacy-policy', '/privacy-policy', '## Privacy body', 'Privacy body'],
    ['user-agreement', '/user-agreement', '## Agreement body', 'Agreement body'],
  ] as const)('loads and renders %s markdown', async (kind, endpoint, payload, visibleText) => {
    mockedGetData.mockResolvedValueOnce(payload);
    render(<PublicDocumentView kind={kind} />);

    expect(screen.getByText('Loading…')).toBeTruthy();
    expect(screen.getByRole('article').getAttribute('aria-busy')).toBe('true');
    expect(await screen.findByText(visibleText)).toBeTruthy();
    expect(mockedGetData).toHaveBeenCalledWith(endpoint);
    expect(screen.getByRole('article').getAttribute('aria-busy')).toBe('false');
  });

  it('renders empty and failed states without exposing a stale document', async () => {
    mockedGetData.mockResolvedValueOnce('   ');
    const rendered = render(<PublicDocumentView kind="privacy-policy" />);
    expect(await screen.findByText('No content has been published yet.')).toBeTruthy();

    rendered.unmount();
    mockedGetData.mockRejectedValueOnce(new Error('private upstream detail'));
    render(<PublicDocumentView kind="privacy-policy" />);
    expect((await screen.findByRole('alert')).textContent).toBe('Unable to load this page.');
    expect(screen.queryByText('private upstream detail')).toBeNull();
  });

  it('isolates configured HTML and rejects oversized content', async () => {
    mockedGetData.mockResolvedValueOnce('<style>body{color:red}</style><h2>Configured HTML</h2><script>window.top.hacked=true</script>');
    const rendered = render(<PublicDocumentView kind="user-agreement" />);
    const frame = await screen.findByTitle('User Agreement');
    expect(frame.getAttribute('sandbox')).toBe('');
    expect(frame.getAttribute('srcdoc')).toContain('Configured HTML');

    rendered.unmount();
    mockedGetData.mockResolvedValueOnce('x'.repeat(MAX_DOCUMENT_CHARACTERS + 1));
    render(<PublicDocumentView kind="user-agreement" />);
    expect((await screen.findByRole('alert')).textContent).toBe('Document is too large to display safely.');
  });

  it('uses explicit safe surfaces for external documents', async () => {
    mockedGetData.mockResolvedValueOnce('https://docs.example.test/privacy');
    const rendered = render(<PublicDocumentView kind="privacy-policy" />);
    const link = await screen.findByRole('link', { name: 'View document' });
    expect(link.getAttribute('href')).toBe('https://docs.example.test/privacy');
    expect(link.getAttribute('target')).toBe('_blank');
    expect(link.getAttribute('rel')).toBe('noopener noreferrer');

    rendered.unmount();
    mockedGetData.mockResolvedValueOnce('https://about.example.test/');
    render(<PublicDocumentView kind="about" />);
    const frame = await screen.findByTitle('About');
    expect(frame.getAttribute('src')).toBe('https://about.example.test/');
    expect(frame.getAttribute('sandbox')).not.toContain('allow-same-origin');
  });

  it('sanitizes unsafe markdown links while retaining safe links', async () => {
    mockedGetData.mockResolvedValueOnce('[safe](https://docs.example.test) [unsafe](javascript:alert(1)) [network-path](//evil.example.test)');
    render(<PublicDocumentView kind="privacy-policy" />);

    const safe = await screen.findByRole('link', { name: 'safe' });
    expect(safe.getAttribute('href')).toBe('https://docs.example.test/');
    expect(safe.getAttribute('rel')).toBe('noopener noreferrer');
    expect(screen.getByText('unsafe').getAttribute('href')).toBeNull();
    expect(screen.getByText('network-path').getAttribute('href')).toBeNull();
  });

  it('ignores a completion after unmount', async () => {
    let resolveRequest: ((value: unknown) => void) | undefined;
    mockedGetData.mockReturnValueOnce(new Promise((resolve) => { resolveRequest = resolve; }));
    const rendered = render(<PublicDocumentView kind="about" />);
    rendered.unmount();

    await act(async () => {
      resolveRequest?.('late response');
      await Promise.resolve();
    });
    await waitFor(() => expect(screen.queryByText('late response')).toBeNull());
  });
});
