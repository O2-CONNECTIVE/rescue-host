import type { KnipConfig } from 'knip';
const config: KnipConfig = {
  entry: ['tests/**/*.test.{ts,mjs}', 'operator/build.mjs', 'native-smoke/inspect-app-startup.ts', 'native-smoke/inspect-wry-startup.ts', 'native-smoke/run-wry-baseline.ts', 'native-smoke/inspect-wry-baseline.ts'],
  project: ['src/**/*.{ts,tsx}!', 'tests/**/*.{ts,mjs}', 'scripts/**/*.mjs', 'native-smoke/**/*.ts', 'operator/*.{ts,mjs}', '*.{ts,mjs}'],
  vite: { config: ['vite.config.ts', 'operator/vite.config.ts'] }
};
export default config;
