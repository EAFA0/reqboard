import * as fs from 'fs';
import * as path from 'path';

/** Normalize a user-selected path to the requirement worktree root. */
export function normalizeRoot(input: string): string {
  let p = path.resolve(input);
  if (path.basename(p) === '.reqboard') {
    p = path.dirname(p);
  }
  return p;
}

/** Does dir contain a .reqboard workspace? */
export function isReqboardRoot(dir: string): boolean {
  try {
    return fs.statSync(path.join(dir, '.reqboard')).isDirectory();
  } catch {
    return false;
  }
}

/** Walk up until a .reqboard workspace is found. */
export function findRootUp(start: string): string | undefined {
  let cur = normalizeRoot(start);
  for (;;) {
    if (isReqboardRoot(cur)) {
      return cur;
    }
    const parent = path.dirname(cur);
    if (parent === cur) {
      return undefined;
    }
    cur = parent;
  }
}

/** CSS class for a PRD block coverage state. */
export function coverageClass(coverage: string): string {
  switch (coverage) {
    case 'covered':
      return 'cov-covered';
    case 'ignored':
      return 'cov-ignored';
    case 'out_of_scope':
      return 'cov-redline';
    case 'unmapped':
      return 'cov-unmapped';
    default:
      return 'cov-heading';
  }
}

/** Compact +add/-del label. */
export function formatDiffStat(added: number, deleted: number): string {
  return `+${added || 0}/-${deleted || 0}`;
}

function escapeRe(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/**
 * Best-effort symbol line lookup (1-based) without a language server.
 * Supports `Func`, `Type.Method`, `type Name`, const/var and thrift blocks.
 */
export function findSymbolLine(source: string, symbol: string): number {
  if (!symbol) {
    return 0;
  }
  const lines = source.split(/\r?\n/);
  const parts = symbol.split('.');
  const name = parts[parts.length - 1];
  const recv = parts.length > 1 ? parts[parts.length - 2] : '';
  const nameRe = escapeRe(name);
  const candidates: RegExp[] = [];
  if (recv) {
    const recvRe = escapeRe(recv);
    candidates.push(new RegExp(`\\bfunc\\s*\\([^)]*\\b${recvRe}\\b[^)]*\\)\\s*${nameRe}\\b`));
  }
  candidates.push(new RegExp(`\\bfunc\\s+(?:\\([^)]*\\)\\s*)?${nameRe}\\s*\\(`));
  candidates.push(new RegExp(`\\btype\\s+${nameRe}\\b`));
  candidates.push(new RegExp(`\\b(?:struct|service|enum|union|exception|message)\\s+${nameRe}\\b`));
  candidates.push(new RegExp(`\\b(?:const|var)\\s+${nameRe}\\b`));
  candidates.push(new RegExp(`^\\s*${nameRe}\\b`));
  for (let i = 0; i < lines.length; i++) {
    for (const re of candidates) {
      if (re.test(lines[i])) {
        return i + 1;
      }
    }
  }
  return 0;
}

/** Ensure a value is a finite number. */
export function num(v: unknown, def: number): number {
  return typeof v === 'number' && isFinite(v) ? v : def;
}

/** Truncate text for compact UI labels. */
export function clip(s: string, max = 80): string {
  const runes = Array.from(s || '');
  return runes.length > max ? runes.slice(0, max).join('') + '…' : s;
}
