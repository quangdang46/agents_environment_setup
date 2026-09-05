#!/usr/bin/env bun
/**
 * Manifest drift contract checks for non-generated release surfaces.
 *
 * The generator diff already compares bytes for generated files. This module
 * adds semantic checks so release gates can explain which manifest-derived
 * surface is missing coverage.
 */

import { existsSync, readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parse as parseYaml } from 'yaml';
import { parseManifestFile } from './parser.js';
import {
  validateVerifiedInstallerChecksums,
  type InstallerChecksumEntry,
} from './validate.js';
import type { Manifest, Module } from './types.js';

export type DriftContractCode =
  | 'MANIFEST_PARSE_FAILED'
  | 'CHECKSUMS_PARSE_FAILED'
  | 'MISSING_FILE'
  | 'MANIFEST_INDEX_MODULE_MISSING'
  | 'DOCTOR_CHECK_MISSING'
  | 'ONBOARDING_LESSON_MISSING'
  | 'README_SNIPPET_MISSING'
  | 'MISSING_VERIFIED_INSTALLER_CHECKSUM'
  | 'INVALID_VERIFIED_INSTALLER_CHECKSUM'
  | 'VERIFIED_INSTALLER_URL_MISMATCH';

export interface DriftContractMismatch {
  code: DriftContractCode;
  message: string;
  file: string;
  moduleId?: string;
  expected?: string;
  actual?: string;
}

export interface DriftContractSummary {
  modules: number;
  verifiedInstallers: number;
  lessonLinkedModules: number;
  doctorChecksExpected: number;
  readmeSnippetsExpected: number;
  checked: number;
}

export interface DriftContractResult {
  ok: boolean;
  root: string;
  summary: DriftContractSummary;
  mismatches: DriftContractMismatch[];
}

const SCRIPT_FILE = fileURLToPath(import.meta.url);
const DEFAULT_ROOT = resolve(dirname(SCRIPT_FILE), '../../..');

const REQUIRED_README_SNIPPETS = [
  {
    snippet: 'scripts/check-manifest-drift.sh --json',
    reason: 'release drift gate',
  },
  {
    snippet: 'bun run generate:diff',
    reason: 'generated artifact byte comparison',
  },
  {
    snippet: 'scripts/generated/doctor_checks.sh',
    reason: 'manifest-derived doctor checks',
  },
  {
    snippet: 'acfs/onboard/lessons',
    reason: 'manifest-linked onboarding lesson content',
  },
  {
    snippet: 'checksums.yaml',
    reason: 'verified installer checksum coverage',
  },
];

function readText(
  root: string,
  relPath: string,
  mismatches: DriftContractMismatch[]
): string | null {
  const absPath = join(root, relPath);
  if (!existsSync(absPath)) {
    mismatches.push({
      code: 'MISSING_FILE',
      file: relPath,
      message: `Required manifest drift contract file is missing: ${relPath}`,
    });
    return null;
  }
  return readFileSync(absPath, 'utf-8');
}

function extractDoctorCheckIds(content: string): Set<string> {
  const ids = new Set<string>();
  const regex = /^\s*"([^"\t]+)\t/gm;
  let match: RegExpExecArray | null;
  while ((match = regex.exec(content)) !== null) {
    ids.add(match[1]);
  }
  return ids;
}

function extractManifestIndexModuleIds(content: string): Set<string> {
  const ids = new Set<string>();
  const arrayMatch = content.match(/ACFS_MODULES_IN_ORDER=\(\n([\s\S]*?)\n\)/);
  if (!arrayMatch) {
    return ids;
  }

  const regex = /"([^"]+)"/g;
  let match: RegExpExecArray | null;
  while ((match = regex.exec(arrayMatch[1])) !== null) {
    ids.add(match[1]);
  }
  return ids;
}

function lessonLinkedModules(_manifest: Manifest): Module[] {
  // Web metadata removed — no lesson-linked modules
  return [];
}

function expectedDoctorCheckIds(manifest: Manifest): Array<{ module: Module; id: string }> {
  const ids: Array<{ module: Module; id: string }> = [];
  for (const module of manifest.modules) {
    for (let i = 0; i < module.verify.length; i += 1) {
      const suffix = module.verify.length > 1 ? `.${i + 1}` : '';
      ids.push({ module, id: `${module.id}${suffix}` });
    }
  }
  return ids;
}

function addMissingModuleIds(
  mismatches: DriftContractMismatch[],
  code: DriftContractCode,
  file: string,
  expectedModules: Module[],
  actualIds: Set<string>,
  label: string
): void {
  for (const module of expectedModules) {
    if (actualIds.has(module.id)) {
      continue;
    }
    mismatches.push({
      code,
      file,
      moduleId: module.id,
      expected: module.id,
      message: `${label} is missing manifest module "${module.id}"`,
    });
  }
}

function checkOnboardingLessons(
  _root: string,
  _modules: Module[],
  _mismatches: DriftContractMismatch[]
): void {
  // Web metadata removed — no lesson-linked modules to check
}

function checkReadmeSnippets(
  readme: string | null,
  mismatches: DriftContractMismatch[]
): void {
  if (readme === null) return;

  for (const { snippet, reason } of REQUIRED_README_SNIPPETS) {
    if (readme.includes(snippet)) {
      continue;
    }
    mismatches.push({
      code: 'README_SNIPPET_MISSING',
      file: 'README.md',
      expected: snippet,
      message: `README is missing manifest drift snippet "${snippet}" for ${reason}`,
    });
  }
}

export function checkManifestDriftContract(rootDir = DEFAULT_ROOT): DriftContractResult {
  const root = resolve(rootDir);
  const mismatches: DriftContractMismatch[] = [];
  const summary: DriftContractSummary = {
    modules: 0,
    verifiedInstallers: 0,
    lessonLinkedModules: 0,
    doctorChecksExpected: 0,
    readmeSnippetsExpected: REQUIRED_README_SNIPPETS.length,
    checked: 0,
  };

  const manifestPath = join(root, 'acfs.manifest.yaml');
  const parseResult = parseManifestFile(manifestPath);
  if (!parseResult.success || !parseResult.data) {
    mismatches.push({
      code: 'MANIFEST_PARSE_FAILED',
      file: 'acfs.manifest.yaml',
      message: parseResult.error?.message ?? 'Failed to parse manifest',
    });
    return { ok: false, root, summary, mismatches };
  }

  const manifest = parseResult.data;
  const verifiedModules = manifest.modules.filter((module) => Boolean(module.verified_installer));
  const lessonModules = lessonLinkedModules(manifest);
  const doctorIds = expectedDoctorCheckIds(manifest);

  summary.modules = manifest.modules.length;
  summary.verifiedInstallers = verifiedModules.length;
  summary.lessonLinkedModules = lessonModules.length;
  summary.doctorChecksExpected = doctorIds.length;

  const checksumsText = readText(root, 'checksums.yaml', mismatches);
  if (checksumsText !== null) {
    try {
      const checksums = parseYaml(checksumsText) as {
        installers?: Record<string, InstallerChecksumEntry>;
      };
      const checksumErrors = validateVerifiedInstallerChecksums(
        manifest,
        checksums.installers ?? {}
      );
      for (const err of checksumErrors) {
        mismatches.push({
          code: err.code as DriftContractCode,
          file: 'checksums.yaml',
          moduleId: err.moduleId,
          message: err.message,
        });
      }
    } catch (err) {
      mismatches.push({
        code: 'CHECKSUMS_PARSE_FAILED',
        file: 'checksums.yaml',
        message: `Failed to parse checksums.yaml: ${err instanceof Error ? err.message : String(err)}`,
      });
    }
  }

  const manifestIndex = readText(root, 'scripts/generated/manifest_index.sh', mismatches);
  if (manifestIndex !== null) {
    const actualIds = extractManifestIndexModuleIds(manifestIndex);
    addMissingModuleIds(
      mismatches,
      'MANIFEST_INDEX_MODULE_MISSING',
      'scripts/generated/manifest_index.sh',
      manifest.modules,
      actualIds,
      'Generated manifest index'
    );
  }

  const doctorChecks = readText(root, 'scripts/generated/doctor_checks.sh', mismatches);
  if (doctorChecks !== null) {
    const actualDoctorIds = extractDoctorCheckIds(doctorChecks);
    for (const { module, id } of doctorIds) {
      if (actualDoctorIds.has(id)) {
        continue;
      }
      mismatches.push({
        code: 'DOCTOR_CHECK_MISSING',
        file: 'scripts/generated/doctor_checks.sh',
        moduleId: module.id,
        expected: id,
        message: `Generated doctor checks are missing manifest check "${id}"`,
      });
    }
  }

  checkOnboardingLessons(root, lessonModules, mismatches);
  checkReadmeSnippets(readText(root, 'README.md', mismatches), mismatches);

  summary.checked =
    summary.verifiedInstallers +
    summary.modules +
    summary.doctorChecksExpected +
    summary.lessonLinkedModules +
    summary.readmeSnippetsExpected;

  return {
    ok: mismatches.length === 0,
    root,
    summary,
    mismatches,
  };
}

function showHelp(): void {
  console.log(`Usage: bun run src/drift-contract.ts [--root DIR] [--json] [--quiet]

Checks manifest-derived surfaces for semantic drift:
  - checksums.yaml coverage for verified installers
  - scripts/generated/manifest_index.sh module coverage
  - scripts/generated/doctor_checks.sh verify coverage
  - acfs/onboard/lessons lesson files
  - README release gate snippets
`);
}

function parseArgs(args: string[]): { root: string; json: boolean; quiet: boolean; help: boolean } {
  let root = DEFAULT_ROOT;
  let json = false;
  let quiet = false;
  let help = false;

  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i];
    switch (arg) {
      case '--root':
        i += 1;
        if (!args[i]) {
          throw new Error('--root requires a directory argument');
        }
        root = args[i];
        break;
      case '--json':
        json = true;
        break;
      case '--quiet':
        quiet = true;
        break;
      case '--help':
      case '-h':
        help = true;
        break;
      default:
        throw new Error(`Unknown argument: ${arg}`);
    }
  }

  return { root, json, quiet, help };
}

async function main(): Promise<void> {
  let args: ReturnType<typeof parseArgs>;
  try {
    args = parseArgs(process.argv.slice(2));
  } catch (err) {
    console.error(err instanceof Error ? err.message : String(err));
    process.exit(2);
  }

  if (args.help) {
    showHelp();
    process.exit(0);
  }

  const result = checkManifestDriftContract(args.root);

  if (args.json) {
    console.log(JSON.stringify(result, null, 2));
  } else if (result.ok) {
    if (!args.quiet) {
      console.log(`Manifest drift contract clean: ${result.summary.checked} checks`);
    }
  } else {
    for (const mismatch of result.mismatches) {
      console.error(`[${mismatch.code}] ${mismatch.file}: ${mismatch.message}`);
    }
  }

  process.exit(result.ok ? 0 : 1);
}

if (import.meta.main) {
  main();
}
