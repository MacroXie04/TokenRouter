import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { getData } from '../../shared/api/client';
import {
  documentFormat,
  documentText,
  externalDocumentURL,
  safeDocumentLink,
  type DocumentKind,
} from './content';

const DOCUMENTS: Record<DocumentKind, { endpoint: string; title: string }> = {
  about: { endpoint: '/about', title: 'About' },
  'privacy-policy': { endpoint: '/privacy-policy', title: 'Privacy Policy' },
  'user-agreement': { endpoint: '/user-agreement', title: 'User Agreement' },
};

function MarkdownDocument({ content }: { content: string }) {
  return (
    <div className="document-markdown">
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
    </div>
  );
}

export function PublicDocumentView({ kind }: { kind: DocumentKind }) {
  const { t } = useTranslation();
  const definition = DOCUMENTS[kind];
  const [content, setContent] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    let active = true;
    setLoading(true);
    setError('');
    setContent('');
    getData<unknown>(definition.endpoint)
      .then((value) => {
        if (active) setContent(documentText(value, kind));
      })
      .catch(() => {
        if (active) setError(t('Unable to load this page.'));
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => { active = false; };
  }, [definition.endpoint, kind, t]);

  const format = useMemo(() => documentFormat(content), [content]);
  const externalURL = format === 'external-url' ? externalDocumentURL(content) : null;

  return (
    <main className="app app-document">
      <header className="header row">
        <div>
          <h1>{t(definition.title)}</h1>
          <p className="tagline">TokenRouter</p>
        </div>
        <a className="button" href="/">{t('Back to home')}</a>
      </header>
      <article className="card document-content" aria-busy={loading}>
        {loading && <p className="muted">{t('Loading…')}</p>}
        {error && <p className="error" role="alert">{error}</p>}
        {!loading && !error && format === 'empty' && (
          <p className="muted">{t('No content has been published yet.')}</p>
        )}
        {!loading && !error && format === 'too-large' && (
          <p className="error" role="alert">{t('Document is too large to display safely.')}</p>
        )}
        {!loading && !error && format === 'markdown' && <MarkdownDocument content={content} />}
        {!loading && !error && format === 'html' && (
          <iframe
            className="document-frame"
            referrerPolicy="no-referrer"
            sandbox=""
            srcDoc={content}
            title={t(definition.title)}
          />
        )}
        {!loading && !error && externalURL && kind === 'about' && (
          <iframe
            className="document-frame"
            referrerPolicy="no-referrer"
            sandbox="allow-forms allow-popups allow-popups-to-escape-sandbox allow-scripts"
            src={externalURL}
            title={t(definition.title)}
          />
        )}
        {!loading && !error && externalURL && kind !== 'about' && (
          <div>
            <p className="muted">{t('The administrator configured an external link for this document.')}</p>
            <a className="button" href={externalURL} target="_blank" rel="noopener noreferrer">
              {t('View document')}
            </a>
          </div>
        )}
      </article>
    </main>
  );
}

export { documentFormat, documentText, externalDocumentURL, safeDocumentLink };
export type { DocumentKind };
