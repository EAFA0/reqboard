import { execFile } from 'child_process';
import * as fs from 'fs';
import * as path from 'path';

export interface DiffLine {
  kind: 'ctx' | 'add' | 'del';
  old_line: number;
  new_line: number;
  text: string;
}

export interface DiffHunk {
  header: string;
  lines: DiffLine[];
}

export interface DiffFileResult {
  status: string;
  added: number;
  deleted: number;
  binary: boolean;
  base_ref: string;
  hunks: DiffHunk[];
}

function runGit(repoPath: string, args: string[]): Promise<string> {
  return new Promise<string>((resolve, reject) => {
    execFile(
      'git',
      ['-C', repoPath, ...args],
      { maxBuffer: 20 * 1024 * 1024 },
      (err, stdout, stderr) => {
        if (err) {
          reject(new Error((stderr || err.message || String(err)).trim()));
          return;
        }
        resolve(stdout);
      }
    );
  });
}

export async function resolveMergeBase(repoPath: string, baseRef: string): Promise<string> {
  const ref = (baseRef || '').trim() || 'HEAD';
  try {
    const out = await runGit(repoPath, ['merge-base', ref, 'HEAD']);
    const sha = out.trim();
    if (sha) {
      return sha;
    }
  } catch {
    // Fall back to baseRef when merge-base cannot be computed.
  }
  return ref;
}

const HUNK_RE = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/;

export function parseUnifiedDiff(raw: string): {
  status: string;
  added: number;
  deleted: number;
  binary: boolean;
  hunks: DiffHunk[];
} {
  let status = 'modified';
  let added = 0;
  let deleted = 0;
  let binary = false;
  const hunks: DiffHunk[] = [];
  let cur: DiffHunk | null = null;
  let oldN = 0;
  let newN = 0;

  for (const line of raw.split('\n')) {
    if (line.startsWith('new file mode')) {
      status = 'added';
    } else if (line.startsWith('deleted file mode') || line === '+++ /dev/null') {
      status = 'deleted';
    } else if (line.startsWith('Binary files ') || line.startsWith('GIT binary patch')) {
      binary = true;
    } else if (line.startsWith('@@ ')) {
      const m = HUNK_RE.exec(line);
      if (!m) {
        continue;
      }
      oldN = Number(m[1]);
      newN = Number(m[3]);
      cur = { header: line, lines: [] };
      hunks.push(cur);
    } else if (cur) {
      if (line === '\\ No newline at end of file') {
        continue;
      }
      if (line.startsWith('+')) {
        cur.lines.push({ kind: 'add', old_line: 0, new_line: newN++, text: line.slice(1) });
        added++;
      } else if (line.startsWith('-')) {
        cur.lines.push({ kind: 'del', old_line: oldN++, new_line: 0, text: line.slice(1) });
        deleted++;
      } else if (line.startsWith(' ')) {
        cur.lines.push({ kind: 'ctx', old_line: oldN++, new_line: newN++, text: line.slice(1) });
      }
    }
  }
  return { status, added, deleted, binary, hunks };
}

export function buildReadOnlyFileDiff(raw: Buffer, status: string, baseRef: string, headerLabel: string): DiffFileResult {
  if (raw.includes(0)) {
    return { status, added: 0, deleted: 0, binary: true, base_ref: baseRef, hunks: [] };
  }
  const text = raw.toString('utf8');
  const lines = text.length === 0 ? [] : text.replace(/\n$/, '').split('\n');
  const isNew = status === 'untracked';
  const diffLines: DiffLine[] = lines.map((l, idx) => ({
    kind: isNew ? 'add' : 'ctx',
    old_line: isNew ? 0 : idx + 1,
    new_line: idx + 1,
    text: l,
  }));
  const hunks: DiffHunk[] = diffLines.length
    ? [{
      header: isNew
        ? `@@ -0,0 +1,${diffLines.length} @@ (${headerLabel})`
        : `@@ -1,${diffLines.length} +1,${diffLines.length} @@ (${headerLabel})`,
      lines: diffLines,
    }]
    : [];
  return {
    status,
    added: isNew ? diffLines.length : 0,
    deleted: 0,
    binary: false,
    base_ref: baseRef,
    hunks,
  };
}

/** Collect 3-dot merge-base diff for one file, with untracked and clean-file fallbacks. */
export async function collectFileDiff(
  repoPath: string,
  baseRef: string,
  relFile: string,
  contextLines = 5
): Promise<DiffFileResult> {
  const ctx = Math.max(0, Math.floor(contextLines));
  const base = await resolveMergeBase(repoPath, baseRef);
  const out = await runGit(repoPath, [
    'diff',
    `--unified=${ctx}`,
    '--no-color',
    '--no-ext-diff',
    '--no-prefix',
    base,
    '--',
    relFile,
  ]);
  if (out.trim()) {
    const parsed = parseUnifiedDiff(out);
    return { ...parsed, base_ref: base };
  }
  const abs = path.join(repoPath, relFile);
  if (!fs.existsSync(abs)) {
    return { status: 'clean', added: 0, deleted: 0, binary: false, base_ref: base, hunks: [] };
  }
  const raw = fs.readFileSync(abs);
  let isUntracked = false;
  try {
    const u = await runGit(repoPath, ['ls-files', '--others', '--exclude-standard', '--', relFile]);
    isUntracked = u.trim().length > 0;
  } catch {
    isUntracked = false;
  }
  return buildReadOnlyFileDiff(
    raw,
    isUntracked ? 'untracked' : 'clean',
    base,
    isUntracked ? 'untracked new file' : 'no diff vs base'
  );
}
