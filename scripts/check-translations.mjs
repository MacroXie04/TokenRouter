#!/usr/bin/env node
// check-translations.mjs — verifies every locale file has an identical key set.
import fs from 'node:fs';
import path from 'node:path';

const localesDir = path.resolve(process.cwd(), 'web/src/i18n/locales');
const files = fs.readdirSync(localesDir).filter((f) => f.endsWith('.json'));
if (files.length === 0) {
  console.error('no locale files found in', localesDir);
  process.exit(1);
}

let reference = null;
let ok = true;
for (const f of files) {
  const data = JSON.parse(fs.readFileSync(path.join(localesDir, f), 'utf8'));
  const keySet = new Set(Object.keys(data));
  if (reference === null) {
    reference = keySet;
    continue;
  }
  const missing = [...reference].filter((k) => !keySet.has(k));
  const extra = [...keySet].filter((k) => !reference.has(k));
  if (missing.length || extra.length) {
    ok = false;
    console.error(`${f}: ${missing.length} missing key(s), ${extra.length} extra key(s)`);
    if (missing.length) console.error('  missing:', missing.slice(0, 8).join(' | '));
    if (extra.length) console.error('  extra:', extra.slice(0, 8).join(' | '));
  }
}

if (!ok) {
  process.exit(1);
}
console.log(`Translation completeness OK: ${files.length} locales, ${reference.size} keys each.`);
