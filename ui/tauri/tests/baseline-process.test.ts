import { test } from 'node:test';
import assert from 'node:assert/strict';
import { executeOwnedProcess } from '../native-smoke/baseline-process.ts';

void test('自己所有childの終了と期限超過を区別し、生出力を返さない', async () => {
  assert.equal(await executeOwnedProcess(process.execPath, ['-e', 'process.exit(0)'], 2000), 'passed');
  assert.equal(await executeOwnedProcess(process.execPath, ['-e', 'console.log("PRIVATE_FIXTURE"); process.exit(2)'], 2000), 'failed');
  assert.equal(await executeOwnedProcess(process.execPath, ['-e', 'setTimeout(() => {}, 5000)'], 100), 'timeout');
});
