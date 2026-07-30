import { readFileSync, writeFileSync } from 'fs';
const stamp = Date.now().toString(36);
const p = 'build/sw.js';
writeFileSync(p, readFileSync(p, 'utf8').replaceAll('__BUILD__', stamp));
console.log('  sw cache: badal-shell-' + stamp);
