// Thin launcher: the grader runs `node /bot/main.ts` (no Go runtime), so this
// spawns the statically linked Go binary next to it and waits for it to exit.
// The binary owns the whole lifecycle: bind 127.0.0.1:0, register with the
// harness, poll /done, exit 0.
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const binary = join(here, 'forge');

const child = spawn(binary, [], {
  cwd: here,
  stdio: 'inherit',
  env: process.env,
});

child.on('error', (error) => {
  console.error(`failed to start ${binary}: ${error.message}`);
  process.exit(1);
});

child.on('exit', (code, signal) => {
  if (signal) process.exit(1);
  process.exit(code ?? 0);
});

for (const signal of ['SIGINT', 'SIGTERM']) {
  process.on(signal, () => {
    child.kill(signal);
  });
}
