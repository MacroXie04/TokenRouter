export type DocumentKind = 'about' | 'privacy-policy' | 'user-agreement';
export type DocumentFormat = 'empty' | 'external-url' | 'html' | 'markdown' | 'too-large';

export const MAX_DOCUMENT_CHARACTERS = 1_000_000;
const MAX_DOCUMENT_URL_CHARACTERS = 2_048;
const HTML_CONTENT = /<!doctype\s+html|<html(?:\s|>)|<head(?:\s|>)|<body(?:\s|>)|<style(?:\s|>)|<script(?:\s|>)|<\/?[a-z][\s\S]*>/i;

export function documentText(value: unknown, kind: DocumentKind): string {
  if (typeof value === 'string') return value.trim();
  if (kind === 'about' && value && typeof value === 'object') {
    const about = (value as { about?: unknown }).about;
    if (typeof about === 'string') return about.trim();
  }
  return '';
}

export function externalDocumentURL(value: string): string | null {
  if (value.length === 0 || value.length > MAX_DOCUMENT_URL_CHARACTERS) return null;
  try {
    const parsed = new URL(value);
    if ((parsed.protocol !== 'https:' && parsed.protocol !== 'http:') || parsed.username || parsed.password) {
      return null;
    }
    return parsed.toString();
  } catch {
    return null;
  }
}

export function documentFormat(content: string): DocumentFormat {
  if (content.length === 0) return 'empty';
  if (content.length > MAX_DOCUMENT_CHARACTERS) return 'too-large';
  if (externalDocumentURL(content)) return 'external-url';
  if (HTML_CONTENT.test(content)) return 'html';
  return 'markdown';
}

export function safeDocumentLink(url: string): string {
  if ((url.startsWith('/') && !url.startsWith('//')) || url.startsWith('#')) return url;
  try {
    const parsed = new URL(url);
    if ((parsed.protocol === 'https:' || parsed.protocol === 'http:') && !parsed.username && !parsed.password) {
      return parsed.toString();
    }
    if (parsed.protocol === 'mailto:' && parsed.username === '' && parsed.password === '') return parsed.toString();
  } catch {
    return '';
  }
  return '';
}
