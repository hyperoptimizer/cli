import { spawnSync } from 'node:child_process';
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../', import.meta.url));
const path = new URL('../internal/graphql/generated.go', import.meta.url);
const previous = readFileSync(path);
const result = spawnSync('go', ['generate', './internal/graphql'], { cwd: root, stdio: 'inherit' });
if (process.argv.includes('--check')) {
  const normalize = (bytes) => bytes.toString('utf8').replace(/\r\n/g, '\n');
  const changed = normalize(readFileSync(path)) !== normalize(previous);
  writeFileSync(path, previous);
  if (changed) {
    console.error('CLI GraphQL client is stale. Run go generate ./internal/graphql.');
    process.exit(1);
  }
}
process.exit(result.status ?? 1);
