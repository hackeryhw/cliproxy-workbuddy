#!/usr/bin/env node
// Append the WorkBuddy panel integration to a CLIProxyAPI control panel build.
//
// The integration lives in its own <script> block at the end of <body>, so the
// panel's minified bundle is never modified. Re-running this script replaces the
// previous block instead of stacking copies, which also makes it the way to
// re-apply the integration after the panel is upgraded.
//
// Usage:
//   node scripts/inject-panel.mjs <path-to-management.html> [--check|--remove]
//
//   (default)  inject or refresh the block
//   --check    report whether the block is present and current
//   --remove   strip the block

import { readFileSync, writeFileSync, copyFileSync, existsSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));
const PAYLOAD = resolve(HERE, '..', 'panel', 'workbuddy-panel.js');

const START = '<!-- workbuddy-panel-integration:start -->';
const END = '<!-- workbuddy-panel-integration:end -->';

const target = process.argv[2];
const mode = process.argv[3] || '';
if (!target) {
  console.error('usage: inject-panel.mjs <management.html> [--check|--remove]');
  process.exit(2);
}
if (!existsSync(target)) {
  console.error(`target not found: ${target}`);
  process.exit(2);
}

const payload = readFileSync(PAYLOAD, 'utf8');
const block = [
  START,
  '<script id="workbuddy-panel-integration">',
  payload.trimEnd(),
  '</script>',
  END,
].join('\n');

let html = readFileSync(target, 'utf8');
const previous = extractBlock(html);

function extractBlock(source) {
  const start = source.indexOf(START);
  if (start < 0) return null;
  const end = source.indexOf(END, start);
  if (end < 0) return null;
  return source.slice(start, end + END.length);
}

function stripBlock(source) {
  const found = extractBlock(source);
  if (!found) return source;
  return source.replace(found, '');
}

if (mode === '--check') {
  if (!previous) {
    console.log('NOT INJECTED');
    process.exit(1);
  }
  const current = previous.trim() === block.trim();
  console.log(current ? 'INJECTED (current)' : 'INJECTED (stale — re-run without --check)');
  process.exit(current ? 0 : 1);
}

if (mode === '--remove') {
  if (!previous) {
    console.log('nothing to remove');
    process.exit(0);
  }
  const backup = target + '.bak';
  copyFileSync(target, backup);
  writeFileSync(target, stripBlock(html));
  console.log(`removed integration (backup: ${backup})`);
  process.exit(0);
}

// Inject / refresh
const backup = target + '.bak';
copyFileSync(target, backup);

const cleaned = stripBlock(html);
const bodyClose = cleaned.lastIndexOf('</body>');
if (bodyClose < 0) {
  console.error('no </body> found — refusing to inject');
  process.exit(2);
}
const next = cleaned.slice(0, bodyClose) + block + '\n' + cleaned.slice(bodyClose);

writeFileSync(target, next);

console.log(`target      : ${target}`);
console.log(`backup      : ${backup}`);
console.log(`payload     : ${payload.length} chars`);
console.log(`html before : ${html.length} chars${previous ? ' (replacing a previous block)' : ''}`);
console.log(`html after  : ${next.length} chars`);
