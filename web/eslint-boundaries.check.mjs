import process from 'node:process';
import assert from 'node:assert/strict';
import { ESLint } from 'eslint';

const eslint = new ESLint();
const cases = [
  ['shared/util.ts', "import { X } from '../features/models';", 'shared'],
  ['shared/util.ts', "export { X } from '../app/App';", 'shared'],
  ['features/home/util.ts', "import X from '../../app/App';", 'composition'],
  ['features/home/util.ts', "export * from '../../shared/../app/App';", 'composition'],
  ['features/home/util.ts', "import { X } from '../models/deployment-api';", 'publicEntry'],
  ['features/home/util.ts', "export { X } from '../models/deployment-api';", 'publicEntry'],
  ['features/home/util.ts', "const value = import('../models/deployment-api');", 'publicEntry'],
  ['features/home/util.ts', "type Value = import('../models/deployment-contracts').DeploymentSummary;", 'publicEntry'],
  ['features/home/util.ts', "import { X } from '../models';", null],
  ['features/home/util.ts', "import { X } from '../models/index';", null],
  ['features/home/util.ts', "import { X } from './home-api';", null],
  ['features/home/util.ts', "import { X } from '../../shared/api/client';", null],
  ['features/home/util.test.ts', "import { X } from '../models/deployment-api';", null],
];

for (const [file, source, expected] of cases) {
  const [result] = await eslint.lintText(source, { filePath: `src/${file}` });
  assert.equal(result.fatalErrorCount, 0, `Parse failed: ${file}`);
  const messages = result.messages.filter((message) => message.ruleId === 'architecture/feature-boundaries');
  assert.deepEqual(messages.map((message) => message.messageId), expected ? [expected] : [], `${file}: ${source}`);
}
process.stdout.write(`Verified ${cases.length} frontend dependency boundary cases.\n`);
