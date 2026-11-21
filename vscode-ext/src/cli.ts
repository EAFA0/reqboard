import { execFile, spawn } from 'child_process';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import * as vscode from 'vscode';

export interface CLIResult {
  data: any;
  stdout: string;
  stderr: string;
}

export class CLIError extends Error {
  constructor(
    message: string,
    public readonly data?: any,
    public readonly stdout?: string,
    public readonly stderr?: string
  ) {
    super(message);
    this.name = 'CLIError';
  }
}

/** Thin wrapper around the local reqboard binary. */
export class ReqboardCLI {
  private resolved?: string;

  constructor(private readonly root: string, private readonly extensionPath: string) {}

  /** Resolve the CLI binary with sensible fallbacks. */
  resolvePath(): string {
    if (this.resolved) {
      return this.resolved;
    }
    const configured = vscode.workspace.getConfiguration('reqboard').get<string>('cliPath') || 'reqboard';
    const candidates: string[] = [];
    if (path.isAbsolute(configured)) {
      candidates.push(configured);
    }
    candidates.push(path.join(os.homedir(), '.local', 'bin', 'reqboard'));
    candidates.push(path.resolve(this.extensionPath, '..', 'bin', 'reqboard'));
    candidates.push(path.resolve(this.extensionPath, '..', '..', 'bin', 'reqboard'));
    for (const c of candidates) {
      try {
        if (fs.existsSync(c)) {
          this.resolved = c;
          return c;
        }
      } catch {
        // ignore
      }
    }
    this.resolved = configured;
    return configured;
  }

  /** Run a CLI command with --root and (by default) --json. */
  run(args: string[], json = true): Promise<CLIResult> {
    const bin = this.resolvePath();
    const full = [...args, '--root', this.root];
    if (json) {
      full.push('--json');
    }
    return new Promise<CLIResult>((resolve, reject) => {
      let stdout = '';
      let stderr = '';
      let child;
      try {
        child = spawn(bin, full, { cwd: this.root, env: process.env });
      } catch (e: any) {
        reject(new CLIError(`无法启动 reqboard (${bin}): ${e?.message || e}`));
        return;
      }
      child.stdout?.on('data', (d) => (stdout += d.toString()));
      child.stderr?.on('data', (d) => (stderr += d.toString()));
      child.on('error', (err: any) => {
        if (err?.code === 'ENOENT') {
          reject(new CLIError(`找不到 reqboard CLI（尝试: ${bin}）。请先构建并加入 PATH，或在设置 reqboard.cliPath 指定绝对路径。`));
        } else {
          reject(new CLIError(err?.message || String(err)));
        }
      });
      child.on('close', (code) => {
        let parsed: any;
        let parseError: unknown;
        const trimmed = stdout.trim();
        if (trimmed.startsWith('{') || trimmed.startsWith('[')) {
          try {
            parsed = JSON.parse(trimmed);
          } catch (e) {
            parseError = e;
          }
        }
        if (code === 0) {
          resolve({ data: parsed, stdout, stderr });
          return;
        }
        let message = '';
        if (parsed && typeof parsed === 'object' && parsed.error) {
          message = String(parsed.error);
        }
        if (!message) {
          message = stderr.trim() || trimmed || `reqboard exited with code ${code}`;
        }
        if (parseError && !message) {
          message = String(parseError);
        }
        reject(new CLIError(message, parsed?.data, stdout, stderr));
      });
    });
  }

  async status(): Promise<any> {
    const res = await this.run(['status']);
    return res.data;
  }
}

/** Read a file from a git ref (used for the focused-git fallback diff). */
export function gitShow(repoPath: string, baseRef: string, file: string): Promise<string> {
  return new Promise<string>((resolve, reject) => {
    execFile(
      'git',
      ['-C', repoPath, 'show', `${baseRef}:${file}`],
      { maxBuffer: 20 * 1024 * 1024 },
      (err, stdout) => {
        if (err) {
          reject(err);
          return;
        }
        resolve(stdout);
      }
    );
  });
}
