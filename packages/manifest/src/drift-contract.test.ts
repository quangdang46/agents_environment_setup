import { describe, expect, test } from 'bun:test';
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import {
  checkManifestDriftContract,
  type DriftContractCode,
} from './drift-contract.js';

const HASH = 'a'.repeat(64);
const INSTALLER_URL = 'https://example.com/example/install.sh';

function write(path: string, content: string): void {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, content);
}

function writeFixtureFile(root: string, relPath: string, content: string): void {
  write(join(root, relPath), content);
}

function cleanFixture(): string {
  const root = mkdtempSync(join(tmpdir(), 'acfs-manifest-drift-contract-'));

  writeFixtureFile(
    root,
    'acfs.manifest.yaml',
    `version: 1
name: Test ACFS
id: test_acfs
defaults:
  user: ubuntu
  workspace_root: /data/projects
  mode: vibe
modules:
  - id: stack.example
    description: Example stack tool
    run_as: target_user
    optional: false
    enabled_by_default: true
    generated: true
    install: []
    verify:
      - example --version
    verified_installer:
      tool: example
      url: ${INSTALLER_URL}
      runner: bash
      args: []
    web:
      lesson_slug: example
  - id: stack.hidden
    description: Hidden stack tool
    run_as: target_user
    optional: true
    enabled_by_default: false
    generated: true
    install:
      - echo hidden
    verify:
      - hidden --version
  - id: base.local
    description: Local base tool
    run_as: target_user
    optional: false
    enabled_by_default: true
    generated: true
    install:
      - echo local
    verify:
      - local --version
`
  );

  writeFixtureFile(
    root,
    'checksums.yaml',
    `installers:
  example:
    url: ${INSTALLER_URL}
    sha256: ${HASH}
`
  );

  writeFixtureFile(
    root,
    'scripts/generated/manifest_index.sh',
    `ACFS_MODULES_IN_ORDER=(
    "stack.example"
    "stack.hidden"
    "base.local"
)
`
  );

  writeFixtureFile(
    root,
    'scripts/generated/doctor_checks.sh',
    `declare -a MANIFEST_CHECKS=(
    "stack.example\tExample stack tool\texample --version\trequired\ttarget_user"
    "stack.hidden\tHidden stack tool\thidden --version\toptional\ttarget_user"
    "base.local\tLocal base tool\tlocal --version\trequired\ttarget_user"
)
`
  );

  writeFixtureFile(root, 'acfs/onboard/lessons/01_example.md', '# Example\n');
  writeFixtureFile(
    root,
    'README.md',
    [
      'scripts/check-manifest-drift.sh --json',
      'bun run generate:diff',
      'scripts/generated/doctor_checks.sh',
      'acfs/onboard/lessons',
      'checksums.yaml',
    ].join('\n')
  );

  return root;
}

function codes(root: string): DriftContractCode[] {
  return checkManifestDriftContract(root).mismatches.map((mismatch) => mismatch.code);
}

describe('manifest drift contract', () => {
  test('passes clean fixtures and honors intentional skip cases', () => {
    const result = checkManifestDriftContract(cleanFixture());

    expect(result.ok).toBe(true);
    expect(result.mismatches).toEqual([]);
    expect(result.summary.verifiedInstallers).toBe(1);
    expect(result.summary.lessonLinkedModules).toBe(1);
  });

  test('detects stale generated manifest index output', () => {
    const root = cleanFixture();
    writeFixtureFile(
      root,
      'scripts/generated/manifest_index.sh',
      `ACFS_MODULES_IN_ORDER=(
    "stack.hidden"
    "base.local"
)
`
    );

    expect(codes(root)).toContain('MANIFEST_INDEX_MODULE_MISSING');
  });

  test('detects missing generated doctor checks', () => {
    const root = cleanFixture();
    writeFixtureFile(
      root,
      'scripts/generated/doctor_checks.sh',
      `declare -a MANIFEST_CHECKS=(
    "stack.hidden\tHidden stack tool\thidden --version\toptional\ttarget_user"
    "base.local\tLocal base tool\tlocal --version\trequired\ttarget_user"
)
`
    );

    expect(codes(root)).toContain('DOCTOR_CHECK_MISSING');
  });

  test('detects missing verified installer checksum coverage', () => {
    const root = cleanFixture();
    writeFixtureFile(root, 'checksums.yaml', 'installers: {}\n');

    expect(codes(root)).toContain('MISSING_VERIFIED_INSTALLER_CHECKSUM');
  });

  test('detects missing README contract snippets', () => {
    const root = cleanFixture();
    writeFixtureFile(root, 'README.md', 'checksums.yaml\n');

    expect(codes(root)).toContain('README_SNIPPET_MISSING');
  });

  test('detects missing onboarding lesson files for lesson-linked modules', () => {
    const root = cleanFixture();
    writeFixtureFile(
      root,
      'acfs.manifest.yaml',
      `version: 1
name: Test ACFS
id: test_acfs
defaults:
  user: ubuntu
  workspace_root: /data/projects
  mode: vibe
modules:
  - id: stack.example
    description: Example stack tool
    run_as: target_user
    optional: false
    enabled_by_default: true
    generated: true
    install: []
    verify:
      - example --version
    verified_installer:
      tool: example
      url: ${INSTALLER_URL}
      runner: bash
      args: []
    web:
      lesson_slug: missing
  - id: base.local
    description: Local base tool
    run_as: target_user
    optional: false
    enabled_by_default: true
    generated: true
    install:
      - echo local
    verify:
      - local --version
`
    );

    expect(codes(root)).toContain('ONBOARDING_LESSON_MISSING');
  });
});
