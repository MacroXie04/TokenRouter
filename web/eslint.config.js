import path from 'node:path';
import eslint from '@eslint/js';
import reactHooks from 'eslint-plugin-react-hooks';
import tseslint from 'typescript-eslint';

// Resolve from the owning file so ../shared/../app cannot bypass a boundary.
const architecture = {
  rules: {
    'feature-boundaries': {
      meta: {
        type: 'problem',
        schema: [],
        messages: {
          shared: 'Shared modules cannot depend on app or feature modules.',
          composition: 'Features cannot depend on app composition modules.',
          publicEntry: 'Import another feature through its index.ts public entry point.',
        },
      },
      create(context) {
        const filename = context.filename.replaceAll('\\', '/');
        const sourceRoot = filename.slice(0, filename.lastIndexOf('/src/') + 5);
        const owner = filename.slice(sourceRoot.length).split('/');
        function check(source) {
          if (!source || typeof source.value !== 'string' || !source.value.startsWith('.')) return;
          const target = path.resolve(path.dirname(filename), source.value).replaceAll('\\', '/');
          if (!target.startsWith(sourceRoot)) return;
          const destination = target.slice(sourceRoot.length).split('/');
          if (owner[0] === 'shared' && ['app', 'features'].includes(destination[0])) {
            context.report({ node: source, messageId: 'shared' });
          } else if (owner[0] === 'features' && destination[0] === 'app') {
            context.report({ node: source, messageId: 'composition' });
          } else if (owner[0] === 'features' && destination[0] === 'features'
            && owner[1] !== destination[1] && destination.length > 2
            && !/^index(?:\.[cm]?[jt]sx?)?$/.test(destination.slice(2).join('/'))) {
            context.report({ node: source, messageId: 'publicEntry' });
          }
        }
        return {
          ImportDeclaration: (node) => check(node.source),
          ExportNamedDeclaration: (node) => check(node.source),
          ExportAllDeclaration: (node) => check(node.source),
          ImportExpression: (node) => check(node.source),
          TSImportType: (node) => check(node.argument?.literal ?? node.argument),
        };
      },
    },
  },
};

export default tseslint.config(
  {
    ignores: ['dist/**', 'node_modules/**'],
  },
  {
    files: ['src/**/*.{ts,tsx}'],
    ignores: ['src/**/*.test.{ts,tsx}'],
    plugins: { architecture },
    rules: { 'architecture/feature-boundaries': 'error' },
  },
  eslint.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ['src/**/*.{ts,tsx}'],
    plugins: {
      'react-hooks': reactHooks,
    },
    rules: {
      'react-hooks/rules-of-hooks': 'error',
      'react-hooks/exhaustive-deps': 'error',
      // Existing API and WebAuthn boundaries intentionally accept browser and
      // server payloads whose exact shape varies by provider.
      '@typescript-eslint/no-explicit-any': 'off',
    },
  },
);
