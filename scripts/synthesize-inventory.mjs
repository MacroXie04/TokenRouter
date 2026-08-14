#!/usr/bin/env node
// synthesize-inventory.mjs — transforms the Phase 0 inventory workflow result
// into TokenRouter parity documents. Run with:
//   node scripts/synthesize-inventory.mjs <output-file>
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(__dirname, '..');
const outDir = path.join(repo, 'docs', 'parity');

const inputPath = process.argv[2];
if (!inputPath) {
  console.error('usage: node scripts/synthesize-inventory.mjs <workflow-output.json>');
  process.exit(2);
}

const raw = fs.readFileSync(inputPath, 'utf8');
const doc = JSON.parse(raw);
const result = Array.isArray(doc) ? doc : doc.result;
if (!Array.isArray(result)) {
  console.error('no result array found in output');
  process.exit(2);
}

fs.mkdirSync(outDir, { recursive: true });

// Persist the raw machine-readable inventory.
fs.writeFileSync(path.join(outDir, 'inventory.json'), JSON.stringify(result, null, 2));

const byKey = {};
for (const r of result) {
  byKey[r.key] = r.items || [];
}

function esc(s) {
  return String(s ?? '').replace(/\|/g, '\\|').replace(/\n/g, ' ');
}

// ---- INVENTORY.md ---------------------------------------------------------
const sections = {
  routes: 'HTTP Routes',
  entities: 'Persistent Entities',
  channels: 'Channel Types & Providers',
  relay: 'Relay Formats, Modes & DTOs',
  settings: 'Configuration & Settings',
  frontend: 'Frontend Routes, Settings Pages & Locales',
  'billing-security': 'Billing, Security & Background Jobs',
};

let inv = `# TokenRouter Forensic Inventory

> Generated from a read-only inspection of the reference system. This document is
> a neutral functional specification, not a copy of reference code.

## Summary

`;
const counts = {};
for (const r of result) {
  counts[r.key] = (r.items || []).length;
  inv += `- **${sections[r.key] ?? r.key}**: ${counts[r.key]} entries\n`;
}

for (const r of result) {
  const items = r.items || [];
  inv += `\n## ${sections[r.key] ?? r.key}\n\n`;
  inv += `| Name | Detail | Evidence |\n|---|---|---|\n`;
  for (const it of items) {
    inv += `| ${esc(it.name)} | ${esc(it.detail)} | ${esc(it.evidence)} |\n`;
  }
}

fs.writeFileSync(path.join(outDir, 'INVENTORY.md'), inv);

// ---- API_MATRIX.md --------------------------------------------------------
let api = `# API Matrix

Every HTTP route in the reference system, with TokenRouter implementation status.

> Status values: NOT_STARTED, IN_PROGRESS, PASS, REFERENCE_PLACEHOLDER, BLOCKED_EXTERNAL

| # | Method + Path | Handler | Middleware / Permission | Status | Target Evidence |
|---|---|---|---|---|---|
`;
const routes = byKey.routes || [];
routes.forEach((r, i) => {
  const parts = (r.name || '').split(' ');
  const method = parts[0] || '';
  const p = parts.slice(1).join(' ') || '';
  api += `| ${i + 1} | ${esc(method)} ${esc(p)} | ${esc(r.detail)} | ${esc(r.evidence)} | NOT_STARTED |  |\n`;
});
fs.writeFileSync(path.join(outDir, 'API_MATRIX.md'), api);

// ---- DATABASE_MATRIX.md ---------------------------------------------------
let db = `# Database Matrix

Every persistent entity in the reference system, with TokenRouter implementation status.

| # | Entity (table) | Fields / Indexes / Relations | Status | Target Evidence |
|---|---|---|---|---|
`;
(byKey.entities || []).forEach((r, i) => {
  db += `| ${i + 1} | ${esc(r.name)} | ${esc(r.detail)} | NOT_STARTED |  |\n`;
});
fs.writeFileSync(path.join(outDir, 'DATABASE_MATRIX.md'), db);

// ---- PROVIDER_MATRIX.md ---------------------------------------------------
let prov = `# Provider Matrix

Channel types and provider adapters in the reference system.

| # | Channel / Provider | Detail | Status | Target Evidence |
|---|---|---|---|---|
`;
(byKey.channels || []).forEach((r, i) => {
  prov += `| ${i + 1} | ${esc(r.name)} | ${esc(r.detail)} | NOT_STARTED |  |\n`;
});
fs.writeFileSync(path.join(outDir, 'PROVIDER_MATRIX.md'), prov);

// ---- FRONTEND_MATRIX.md ---------------------------------------------------
let fe = `# Frontend Matrix

Frontend routes, settings pages, and locale keys in the reference system.

| # | Route / Page / Locale | Detail | Status | Target Evidence |
|---|---|---|---|---|
`;
(byKey.frontend || []).forEach((r, i) => {
  fe += `| ${i + 1} | ${esc(r.name)} | ${esc(r.detail)} | NOT_STARTED |  |\n`;
});
fs.writeFileSync(path.join(outDir, 'FRONTEND_MATRIX.md'), fe);

// ---- machine-readable summary --------------------------------------------
const summary = {
  generated: new Date().toISOString(),
  counts,
  anchors: {
    entities: '~34', routes: '~300-350', channel_types: '~57', providers: '~37',
    relay_formats: '~13', relay_modes: '~38', frontend_domains: '~23',
    frontend_routes: '~59', settings_pages: '~40', languages: '~7',
    translation_keys: '~5266',
  },
};
fs.writeFileSync(path.join(outDir, 'summary.json'), JSON.stringify(summary, null, 2));

console.log('wrote parity docs to', outDir);
console.log(JSON.stringify(counts, null, 2));
