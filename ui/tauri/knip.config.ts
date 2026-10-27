import type { KnipConfig } from 'knip';
const config: KnipConfig = {
  entry: ['tests/**/*.test.{ts,mjs}'],
  project: ['src/**/*.{ts,tsx}!', 'tests/**/*.{ts,mjs}', 'scripts/**/*.mjs', 'native-smoke/**/*.ts', '*.{ts,mjs}'],
  vite: { config: 'vite.config.ts' }
};
export default config;
