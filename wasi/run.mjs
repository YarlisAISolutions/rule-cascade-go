#!/usr/bin/env node
// Runs a WASI (preview 1) command module under Node.js 20 or later:
//
//   node run.mjs <module.wasm> [arguments...]
//
// Standard input, output and error are those of this process, and the exit status is the
// module's. The module sees the host file system from the root of the current drive, with the
// current directory as its working directory, so relative paths mean what they mean in a shell.
import { readFile } from 'node:fs/promises';
import path from 'node:path';

const [file, ...args] = process.argv.slice(2);
if (!file) {
  console.error('usage: node run.mjs <module.wasm> [arguments...]');
  process.exit(2);
}

// node:wasi announces on every start that it is experimental; keep that off the engine's stderr.
const emitWarning = process.emitWarning;
process.emitWarning = (warning, ...rest) =>
  String(warning).includes('WASI') ? undefined : emitWarning.call(process, warning, ...rest);
const { WASI } = await import('node:wasi');

const cwd = process.cwd();
const root = path.parse(cwd).root;
const wasi = new WASI({
  version: 'preview1',
  args: [path.basename(file), ...args],
  env: { PWD: '/' + path.relative(root, cwd).split(path.sep).join('/') },
  preopens: { '/': root },
  returnOnExit: true,
});
const module = await WebAssembly.compile(await readFile(file));
const instance = await WebAssembly.instantiate(module, wasi.getImportObject());
process.exitCode = wasi.start(instance);
