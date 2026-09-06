#!/usr/bin/env node
// Enumerate the current Git-scoped source snapshot, including untracked moves
// and excluding genuine worktree deletions without hiding unreadable paths.
import fs from 'node:fs';
import path from 'node:path';
import { execFileSync } from 'node:child_process';

const root = path.resolve(process.argv[2] || '.');
const goOnly = process.argv.includes('--go');
const listed = execFileSync('git', ['ls-files', '-z', '--cached', '--others', '--exclude-standard'], { cwd: root })
  .toString('utf8').split('\0').filter(Boolean);
const deleted = new Set(execFileSync('git', ['ls-files', '-z', '--deleted'], { cwd: root })
  .toString('utf8').split('\0').filter(Boolean));
for (const file of [...new Set(listed)].sort()) {
  if (goOnly && !file.endsWith('.go')) continue;
  try {
    fs.lstatSync(path.join(root, file));
  } catch (error) {
    if (error.code === 'ENOENT' && deleted.has(file)) continue;
    throw error;
  }
  process.stdout.write(`${file}\0`);
}
