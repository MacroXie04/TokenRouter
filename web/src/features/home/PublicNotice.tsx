import { useEffect, useId, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import type { PublicAnnouncementInfo } from '../../lib/public-status';
import { externalDocumentURL, safeDocumentLink } from '../public-documents/content';
import { loadPublicContent } from './home-api';

const NOTICE_READ_KEY = 'tokenrouter.public_notice_read.v1';
const ANNOUNCEMENTS_READ_KEY = 'tokenrouter.public_announcements_read.v1';
const MAX_VISIBLE_ANNOUNCEMENTS = 20;

type NoticeState =
  | { status: 'loading' }
  | { status: 'empty' | 'error' }
  | { status: 'ready'; content: string; signature: string };

export function publicNoticeSignature(content: string): string {
  let hash = 0x811c9dc5;
  for (let index = 0; index < content.length; index += 1) {
    hash ^= content.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193);
  }
  return `${content.length}:${(hash >>> 0).toString(36)}`;
}

function readNoticeSignature(): string {
  try {
    return window.localStorage?.getItem(NOTICE_READ_KEY) ?? '';
  } catch {
    return '';
  }
}

function markNoticeRead(signature: string): void {
  try {
    window.localStorage?.setItem(NOTICE_READ_KEY, signature);
  } catch {
    // Persistence is optional; the notice remains usable in this view.
  }
}

function announcementSignature(announcement: PublicAnnouncementInfo): string {
  if (announcement.id !== undefined) return `${typeof announcement.id}:${announcement.id}`;
  return `hash:${publicNoticeSignature(JSON.stringify({
    content: announcement.content,
    extra: announcement.extra ?? '',
    publishDate: announcement.publishDate,
    type: announcement.type ?? '',
  }))}`;
}

function readAnnouncementSignatures(): string[] {
  try {
    const stored = window.localStorage?.getItem(ANNOUNCEMENTS_READ_KEY);
    if (!stored || stored.length > 32 * 1024) return [];
    const parsed: unknown = JSON.parse(stored);
    if (!Array.isArray(parsed) || parsed.length > 100) return [];
    return [...new Set(parsed.filter(
      (entry): entry is string => typeof entry === 'string' && entry.length > 0 && entry.length <= 256,
    ))];
  } catch {
    return [];
  }
}

function markAnnouncementsRead(signatures: string[]): void {
  try {
    window.localStorage?.setItem(ANNOUNCEMENTS_READ_KEY, JSON.stringify(signatures.slice(-100)));
  } catch {
    // Persistence is optional; the timeline remains usable in this view.
  }
}

function formattedPublishDate(value: string): string {
  return new Date(value).toLocaleString();
}

export function SafePublicMarkdown({ content }: { content: string }) {
  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm]}
      urlTransform={safeDocumentLink}
      components={{
        a: ({ href, children }) => {
          const external = Boolean(href && externalDocumentURL(href));
          return (
            <a
              href={href || undefined}
              rel={external ? 'noopener noreferrer' : undefined}
              target={external ? '_blank' : undefined}
            >
              {children}
            </a>
          );
        },
      }}
    >
      {content}
    </ReactMarkdown>
  );
}

export function PublicNotice({
  announcementsEnabled = false,
  announcements = [],
}: {
  announcementsEnabled?: boolean;
  announcements?: readonly PublicAnnouncementInfo[];
}) {
  const { t } = useTranslation();
  const regionId = useId();
  const [state, setState] = useState<NoticeState>({ status: 'loading' });
  const [expanded, setExpanded] = useState(false);
  const [activeTab, setActiveTab] = useState<'notice' | 'announcements'>('notice');
  const [readSignature, setReadSignature] = useState(readNoticeSignature);
  const [readAnnouncements, setReadAnnouncements] = useState(readAnnouncementSignatures);
  const visibleAnnouncements = useMemo(
    () => (announcementsEnabled ? announcements.slice(0, MAX_VISIBLE_ANNOUNCEMENTS) : []),
    [announcements, announcementsEnabled],
  );
  const announcementSignatures = useMemo(
    () => visibleAnnouncements.map(announcementSignature),
    [visibleAnnouncements],
  );
  const readAnnouncementSet = useMemo(() => new Set(readAnnouncements), [readAnnouncements]);

  useEffect(() => {
    const controller = new AbortController();
    let active = true;
    loadPublicContent('/notice', controller.signal)
      .then((content) => {
        if (!active) return;
        setState(content
          ? { status: 'ready', content, signature: publicNoticeSignature(content) }
          : { status: 'empty' });
      })
      .catch(() => {
        if (active && !controller.signal.aborted) setState({ status: 'error' });
      });
    return () => {
      active = false;
      controller.abort();
    };
  }, []);

  useEffect(() => {
    if (!announcementsEnabled && activeTab === 'announcements') setActiveTab('notice');
  }, [activeTab, announcementsEnabled]);

  const noticeUnread = state.status === 'ready' && state.signature !== readSignature;
  const announcementUnreadCount = announcementSignatures.filter(
    (signature) => !readAnnouncementSet.has(signature),
  ).length;
  const unreadCount = Number(noticeUnread) + announcementUnreadCount;

  const markActiveTabRead = (tab: 'notice' | 'announcements') => {
    if (tab === 'notice' && state.status === 'ready' && state.signature !== readSignature) {
      markNoticeRead(state.signature);
      setReadSignature(state.signature);
    }
    if (tab === 'announcements' && announcementUnreadCount > 0) {
      const next = [...new Set([...readAnnouncements, ...announcementSignatures])].slice(-100);
      markAnnouncementsRead(next);
      setReadAnnouncements(next);
    }
  };

  useEffect(() => {
    if (!expanded) return;
    if (activeTab === 'notice' && state.status === 'ready' && state.signature !== readSignature) {
      markNoticeRead(state.signature);
      setReadSignature(state.signature);
    }
    if (activeTab === 'announcements' && announcementUnreadCount > 0) {
      const next = [...new Set([...readAnnouncements, ...announcementSignatures])].slice(-100);
      markAnnouncementsRead(next);
      setReadAnnouncements(next);
    }
  }, [
    activeTab,
    announcementSignatures,
    announcementUnreadCount,
    expanded,
    readAnnouncements,
    readSignature,
    state,
  ]);

  const toggle = () => {
    const next = !expanded;
    setExpanded(next);
    if (next) markActiveTabRead(activeTab);
  };
  const selectTab = (tab: 'notice' | 'announcements') => {
    setActiveTab(tab);
    markActiveTabRead(tab);
  };

  return (
    <aside className="public-notice" aria-label={t('Notifications')}>
      <button
        aria-controls={regionId}
        aria-expanded={expanded}
        className="public-notice-toggle"
        onClick={toggle}
        type="button"
      >
        <span aria-hidden="true">●</span>
        <span>{t('System notice')}</span>
        {unreadCount > 0 && (
          <span className="public-notice-new">{t('New')} {unreadCount > 99 ? '99+' : unreadCount}</span>
        )}
        <span className="public-notice-action">{expanded ? t('Hide') : t('Read')}</span>
      </button>
      {expanded && (
        <div className="public-notice-content" id={regionId} role="region" aria-label={t('System announcements')}>
          <div className="public-notice-tabs" role="tablist" aria-label={t('Notification type')}>
            <button
              aria-controls={`${regionId}-notice`}
              aria-selected={activeTab === 'notice'}
              onClick={() => selectTab('notice')}
              role="tab"
              type="button"
            >
              {t('Notice')}
            </button>
            {announcementsEnabled && (
              <button
                aria-controls={`${regionId}-announcements`}
                aria-selected={activeTab === 'announcements'}
                onClick={() => selectTab('announcements')}
                role="tab"
                type="button"
              >
                {t('Announcements')}
                {announcementUnreadCount > 0 && <span>{announcementUnreadCount}</span>}
              </button>
            )}
          </div>
          {activeTab === 'notice' ? (
            <div className="document-markdown public-notice-panel" id={`${regionId}-notice`} role="tabpanel">
              {state.status === 'loading' && <p className="muted" role="status">{t('Loading system notice…')}</p>}
              {state.status === 'error' && <p className="error" role="alert">{t('Unable to load system notice.')}</p>}
              {state.status === 'empty' && <p className="muted">{t('No system notice is currently published.')}</p>}
              {state.status === 'ready' && <SafePublicMarkdown content={state.content} />}
            </div>
          ) : (
            <div className="public-notice-panel" id={`${regionId}-announcements`} role="tabpanel">
              {visibleAnnouncements.length === 0 ? (
                <p className="muted">{t('No system announcements')}</p>
              ) : (
                <ol className="public-announcement-list">
                  {visibleAnnouncements.map((announcement, index) => (
                    <li className={`public-announcement public-announcement-${announcement.type ?? 'default'}`} key={announcementSignatures[index]}>
                      <div className="document-markdown"><SafePublicMarkdown content={announcement.content} /></div>
                      {announcement.extra && (
                        <div className="document-markdown public-announcement-extra">
                          <SafePublicMarkdown content={announcement.extra} />
                        </div>
                      )}
                      <time dateTime={announcement.publishDate}>{formattedPublishDate(announcement.publishDate)}</time>
                    </li>
                  ))}
                </ol>
              )}
            </div>
          )}
        </div>
      )}
    </aside>
  );
}
