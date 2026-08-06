import { z } from 'zod';
import { ModuleWebMetadataSchema } from './schema.js';
import type { InstallerChecksumEntry } from './validate.js';
import type { Manifest, Module, ModuleCategory, RunAs } from './types.js';

const PLUGIN_SCHEMA = 'acfs.plugin-package.v1';
const SUPPORTED_SCHEMA_VERSION = 1;
const SHA256_HEX_PATTERN = /^[a-f0-9]{64}$/i;
const MODULE_ID_PATTERN =
  /^plugin\.([a-z][a-z0-9_]*)\.[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$/;
const PACKAGE_ID_PATTERN = /^[a-z][a-z0-9_.-]*$/;
const ALLOWED_RUNNERS = new Set(['bash', 'sh']);
const ALLOWED_INSTALL_KINDS = new Set([
  'verified_installer',
  'release_artifact',
  'copy_asset',
  'manual_step',
]);
const ALLOWED_CATEGORIES = new Set<ModuleCategory>([
  'base',
  'users',
  'filesystem',
  'shell',
  'cli',
  'network',
  'lang',
  'tools',
  'db',
  'cloud',
  'agents',
  'stack',
  'acfs',
]);
const ALLOWED_TOP_LEVEL_FIELDS = new Set([
  'schema',
  'schemaVersion',
  'packageId',
  'displayName',
  'version',
  'description',
  'publisher',
  'license',
  'docsUrl',
  'provenance',
  'targets',
  'capabilities',
  'modules',
  'offline',
  'extensions',
]);
const SECRET_FIELD_NAMES = new Set([
  'token',
  'apikey',
  'secret',
  'password',
  'passphrase',
  'privatekey',
  'clientsecret',
  'refreshtoken',
  'accesstoken',
  'cookie',
  'session',
  'vaultroottoken',
  'sshprivatekey',
]);
const DISALLOWED_INSTALL_FIELDS = new Set([
  'command',
  'commands',
  'shell',
  'script',
  'inlineScript',
  'inline_script',
  'heredoc',
  'eval',
]);

const PluginInstallSchema = z.object({ kind: z.string().min(1) }).passthrough();

const PluginTargetSchema = z
  .object({
    os: z.string().min(1),
    versions: z.array(z.string().min(1)).min(1),
    arch: z.array(z.string().min(1)).min(1),
    libc: z.array(z.string().min(1)).min(1),
  })
  .passthrough();

const PluginModuleSchema = z
  .object({
    id: z.string().min(1),
    description: z.string().min(1),
    category: z.string().min(1),
    phase: z.number().int().min(1).max(10),
    run_as: z.enum(['target_user', 'root', 'current']),
    optional: z.boolean(),
    enabled_by_default: z.boolean(),
    dependencies: z.array(z.string().min(1)).optional(),
    install: PluginInstallSchema,
    verify: z.array(z.string().min(1)).min(1),
    docs_url: z.string().url(),
    web: ModuleWebMetadataSchema.optional(),
  })
  .passthrough();

const PluginPackageSchema = z
  .object({
    schema: z.string(),
    schemaVersion: z.number().int(),
    packageId: z.string().regex(PACKAGE_ID_PATTERN),
    displayName: z.string().min(1),
    version: z.string().min(1),
    description: z.string().min(1),
    publisher: z
      .object({
        name: z.string().min(1),
        contactUrl: z.string().url(),
        sourceUrl: z.string().url(),
      })
      .passthrough(),
    license: z.string().min(1),
    docsUrl: z.string().url().optional(),
    provenance: z
      .object({
        generatedAt: z.string().min(1),
        sourceRef: z.string().min(1),
        sourceCommit: z.string().regex(/^[a-f0-9]{40}$/i),
        pluginSha256: z.string().regex(SHA256_HEX_PATTERN),
        acfsManifestVersion: z.number().int().positive(),
      })
      .passthrough(),
    targets: z.array(PluginTargetSchema).min(1),
    capabilities: z
      .object({
        allowed: z.array(z.string().min(1)),
        reviewRequired: z.array(z.string().min(1)),
        disallowed: z.array(z.string().min(1)),
      })
      .passthrough(),
    modules: z.array(PluginModuleSchema).min(1),
    offline: z
      .object({
        bundlingPolicy: z.enum(['bundled', 'metadata_only', 'live_required', 'prohibited']),
        liveAuthRequired: z.boolean(),
        providerInteractionRequired: z.boolean(),
      })
      .passthrough(),
    extensions: z.record(z.string(), z.unknown()).optional(),
  })
  .passthrough();

export type PluginPackage = z.output<typeof PluginPackageSchema>;
export type PluginModule = PluginPackage['modules'][number];

export type PluginDiagnosticCode =
  | 'plugin_schema_unsupported'
  | 'plugin_missing_required_field'
  | 'plugin_unknown_top_level_field'
  | 'plugin_archive_layout_invalid'
  | 'plugin_package_hash_mismatch'
  | 'plugin_target_unsupported'
  | 'plugin_module_id_invalid'
  | 'plugin_module_collision'
  | 'plugin_generated_function_collision'
  | 'plugin_dependency_invalid'
  | 'plugin_capability_undeclared'
  | 'plugin_review_required'
  | 'plugin_disallowed_behavior'
  | 'plugin_verified_installer_checksum_required'
  | 'plugin_artifact_hash_required'
  | 'plugin_secret_material_refused'
  | 'plugin_offline_policy_incompatible';

export type PluginDiagnosticSeverity = 'error' | 'review_required' | 'warning';

export interface PluginDiagnostic {
  code: PluginDiagnosticCode;
  message: string;
  path: string;
  severity: PluginDiagnosticSeverity;
  moduleId?: string;
  context?: Record<string, unknown>;
}

export interface PluginValidationTarget {
  os: string;
  version: string;
  arch: string;
  libc: string;
}

export interface PluginValidationOptions {
  firstPartyManifest: Manifest;
  installers?: Record<string, InstallerChecksumEntry>;
  target?: PluginValidationTarget;
  existingPluginModuleIds?: Iterable<string>;
}

export interface PluginValidationResult {
  valid: boolean;
  diagnostics: PluginDiagnostic[];
  package?: PluginPackage;
  manifestModules: Module[];
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function addDiagnostic(
  diagnostics: PluginDiagnostic[],
  diagnostic: PluginDiagnostic
): void {
  diagnostics.push(diagnostic);
}

function normalizeSecretFieldName(name: string): string {
  return name.replace(/[_-]/g, '').toLowerCase();
}

function packageSlug(packageId: string): string {
  return packageId
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/^_+|_+$/g, '');
}

function toFunctionName(moduleId: string): string {
  return `install_${moduleId.replace(/\./g, '_')}`;
}

function diagnosticPath(path: PropertyKey[]): string {
  if (path.length === 0) return '<root>';

  return path
    .map((part) => (typeof part === 'number' ? `[${part}]` : String(part)))
    .join('.')
    .replace(/\.\[/g, '[');
}

function stringField(record: Record<string, unknown>, key: string): string | undefined {
  const value = record[key];
  return typeof value === 'string' ? value : undefined;
}

function stringArrayField(record: Record<string, unknown>, key: string): string[] | undefined {
  const value = record[key];
  if (!Array.isArray(value)) return undefined;
  if (!value.every((entry) => typeof entry === 'string')) return undefined;
  return [...value];
}

function isHttpsUrl(value: string | undefined): value is string {
  if (!value) return false;
  try {
    return new URL(value).protocol === 'https:';
  } catch {
    return false;
  }
}

function isSafeRelativePath(value: string | undefined): value is string {
  if (!value) return false;
  if (value.startsWith('/') || value.includes('\0') || value.includes('\\')) return false;
  return !value.split('/').some((part) => part === '' || part === '.' || part === '..');
}

function containsSecretLikeValue(value: string): boolean {
  if (/-----BEGIN (OPENSSH|RSA|EC|DSA) PRIVATE KEY-----/i.test(value)) return true;
  if (/gh[pousr]_[A-Za-z0-9_]{20,}/.test(value)) return true;
  if (/sk-[A-Za-z0-9]{20,}/.test(value)) return true;
  if (/Bearer [A-Za-z0-9._~+/-]{12,}/.test(value)) return true;
  if (/\b(?:\d{1,3}\.){3}\d{1,3}\b/.test(value)) return true;
  return false;
}

function scanSecretMaterial(
  value: unknown,
  diagnostics: PluginDiagnostic[],
  path: string
): void {
  if (Array.isArray(value)) {
    value.forEach((entry, index) => {
      scanSecretMaterial(entry, diagnostics, `${path}[${index}]`);
    });
    return;
  }

  if (isRecord(value)) {
    for (const [key, child] of Object.entries(value)) {
      const childPath = path ? `${path}.${key}` : key;
      if (SECRET_FIELD_NAMES.has(normalizeSecretFieldName(key))) {
        addDiagnostic(diagnostics, {
          code: 'plugin_secret_material_refused',
          message: `Plugin field "${childPath}" is forbidden because plugins are not credential stores`,
          path: childPath,
          severity: 'error',
          context: { value: '<redacted>' },
        });
      }
      scanSecretMaterial(child, diagnostics, childPath);
    }
    return;
  }

  if (typeof value === 'string' && containsSecretLikeValue(value)) {
    const valuePath = path || '<root>';
    addDiagnostic(diagnostics, {
      code: 'plugin_secret_material_refused',
      message: `Plugin value at "${valuePath}" looks like secret or host-specific material`,
      path: valuePath,
      severity: 'error',
      context: { value: '<redacted>' },
    });
  }
}

function validateTopLevelFields(input: unknown, diagnostics: PluginDiagnostic[]): void {
  if (!isRecord(input)) return;

  for (const key of Object.keys(input)) {
    if (!ALLOWED_TOP_LEVEL_FIELDS.has(key)) {
      addDiagnostic(diagnostics, {
        code: 'plugin_unknown_top_level_field',
        message: `Unknown top-level plugin field "${key}" must move under extensions`,
        path: key,
        severity: 'error',
      });
    }
  }
}

function addSchemaDiagnostics(input: unknown, diagnostics: PluginDiagnostic[]): PluginPackage | undefined {
  const parsed = PluginPackageSchema.safeParse(input);
  if (!parsed.success) {
    for (const issue of parsed.error.issues) {
      const path = diagnosticPath(issue.path);
      addDiagnostic(diagnostics, {
        code: 'plugin_missing_required_field',
        message: issue.message,
        path,
        severity: 'error',
      });
    }
    return undefined;
  }

  const plugin = parsed.data;
  if (plugin.schema !== PLUGIN_SCHEMA || plugin.schemaVersion !== SUPPORTED_SCHEMA_VERSION) {
    addDiagnostic(diagnostics, {
      code: 'plugin_schema_unsupported',
      message: `Unsupported plugin schema ${plugin.schema}@${plugin.schemaVersion}`,
      path: 'schema',
      severity: 'error',
      context: {
        supportedSchema: PLUGIN_SCHEMA,
        supportedSchemaVersion: SUPPORTED_SCHEMA_VERSION,
      },
    });
  }

  return plugin;
}

function targetMatches(plugin: PluginPackage, target: PluginValidationTarget): boolean {
  return plugin.targets.some((candidate) => {
    return (
      candidate.os === target.os &&
      candidate.versions.includes(target.version) &&
      candidate.arch.includes(target.arch) &&
      candidate.libc.includes(target.libc)
    );
  });
}

function declaredCapabilitySet(plugin: PluginPackage): Set<string> {
  return new Set([
    ...plugin.capabilities.allowed,
    ...plugin.capabilities.reviewRequired,
    ...plugin.capabilities.disallowed,
  ]);
}

function validateCapabilityUse(
  plugin: PluginPackage,
  module: PluginModule,
  capability: string,
  diagnostics: PluginDiagnostic[],
  path: string
): void {
  const declared = declaredCapabilitySet(plugin);
  if (!declared.has(capability)) {
    addDiagnostic(diagnostics, {
      code: 'plugin_capability_undeclared',
      message: `Plugin module "${module.id}" uses undeclared capability "${capability}"`,
      path,
      severity: 'error',
      moduleId: module.id,
      context: { capability },
    });
    return;
  }

  if (plugin.capabilities.disallowed.includes(capability)) {
    addDiagnostic(diagnostics, {
      code: 'plugin_disallowed_behavior',
      message: `Plugin module "${module.id}" requested disallowed capability "${capability}"`,
      path,
      severity: 'error',
      moduleId: module.id,
      context: { capability },
    });
    return;
  }

  if (plugin.capabilities.reviewRequired.includes(capability)) {
    addDiagnostic(diagnostics, {
      code: 'plugin_review_required',
      message: `Plugin module "${module.id}" requires maintainer review for "${capability}"`,
      path,
      severity: 'review_required',
      moduleId: module.id,
      context: { capability },
    });
  }
}

function validateInstallFields(
  plugin: PluginPackage,
  module: PluginModule,
  moduleIndex: number,
  installers: Record<string, InstallerChecksumEntry>,
  diagnostics: PluginDiagnostic[]
): void {
  const install = module.install as Record<string, unknown>;
  const path = `modules[${moduleIndex}].install`;
  const kind = module.install.kind;

  for (const key of Object.keys(install)) {
    if (DISALLOWED_INSTALL_FIELDS.has(key)) {
      addDiagnostic(diagnostics, {
        code: 'plugin_disallowed_behavior',
        message: `Plugin module "${module.id}" uses forbidden executable install field "${key}"`,
        path: `${path}.${key}`,
        severity: 'error',
        moduleId: module.id,
        context: { field: key },
      });
    }
  }

  if (!ALLOWED_INSTALL_KINDS.has(kind)) {
    addDiagnostic(diagnostics, {
      code: 'plugin_disallowed_behavior',
      message: `Plugin module "${module.id}" uses unsupported install kind "${kind}"`,
      path: `${path}.kind`,
      severity: 'error',
      moduleId: module.id,
      context: { kind },
    });
    return;
  }

  validateCapabilityUse(plugin, module, kind, diagnostics, `${path}.kind`);

  switch (kind) {
    case 'verified_installer':
      validateVerifiedInstallerInstall(module, moduleIndex, installers, diagnostics);
      return;
    case 'release_artifact':
      validateReleaseArtifactInstall(module, moduleIndex, diagnostics);
      return;
    case 'copy_asset':
      validateCopyAssetInstall(module, moduleIndex, diagnostics);
      return;
    case 'manual_step':
      validateManualStepInstall(module, moduleIndex, diagnostics);
      return;
  }
}

function validateVerifiedInstallerInstall(
  module: PluginModule,
  moduleIndex: number,
  installers: Record<string, InstallerChecksumEntry>,
  diagnostics: PluginDiagnostic[]
): void {
  const install = module.install as Record<string, unknown>;
  const path = `modules[${moduleIndex}].install`;
  const tool = stringField(install, 'tool');
  const url = stringField(install, 'url');
  const runner = stringField(install, 'runner');

  if (!tool) {
    addDiagnostic(diagnostics, {
      code: 'plugin_missing_required_field',
      message: `Plugin module "${module.id}" is missing verified installer tool`,
      path: `${path}.tool`,
      severity: 'error',
      moduleId: module.id,
    });
  }

  if (!isHttpsUrl(url)) {
    addDiagnostic(diagnostics, {
      code: 'plugin_disallowed_behavior',
      message: `Plugin module "${module.id}" verified installer URL must use https://`,
      path: `${path}.url`,
      severity: 'error',
      moduleId: module.id,
      context: { url: url ?? '<missing>' },
    });
  }

  if (!runner || !ALLOWED_RUNNERS.has(runner)) {
    addDiagnostic(diagnostics, {
      code: 'plugin_disallowed_behavior',
      message: `Plugin module "${module.id}" verified installer runner must be bash or sh`,
      path: `${path}.runner`,
      severity: 'error',
      moduleId: module.id,
      context: { runner: runner ?? '<missing>', allowedRunners: Array.from(ALLOWED_RUNNERS) },
    });
  }

  if (install.fallback_url !== undefined) {
    addDiagnostic(diagnostics, {
      code: 'plugin_disallowed_behavior',
      message: `Plugin module "${module.id}" cannot use verified_installer.fallback_url`,
      path: `${path}.fallback_url`,
      severity: 'error',
      moduleId: module.id,
    });
  }

  if (!tool || !url) return;

  const entry = installers[tool];
  if (!entry?.url || !entry?.sha256) {
    addDiagnostic(diagnostics, {
      code: 'plugin_verified_installer_checksum_required',
      message: `checksums.yaml is missing a complete installer entry for "${tool}"`,
      path,
      severity: 'error',
      moduleId: module.id,
      context: { tool, hasUrl: Boolean(entry?.url), hasSha256: Boolean(entry?.sha256) },
    });
    return;
  }

  if (!SHA256_HEX_PATTERN.test(entry.sha256)) {
    addDiagnostic(diagnostics, {
      code: 'plugin_verified_installer_checksum_required',
      message: `checksums.yaml has an invalid sha256 for "${tool}"`,
      path,
      severity: 'error',
      moduleId: module.id,
      context: { tool, sha256: entry.sha256 },
    });
    return;
  }

  if (entry.url !== url) {
    addDiagnostic(diagnostics, {
      code: 'plugin_verified_installer_checksum_required',
      message: `Plugin module "${module.id}" verified installer URL does not match checksums.yaml`,
      path: `${path}.url`,
      severity: 'error',
      moduleId: module.id,
      context: { tool, manifestUrl: url, checksumsUrl: entry.url },
    });
  }
}

function validateReleaseArtifactInstall(
  module: PluginModule,
  moduleIndex: number,
  diagnostics: PluginDiagnostic[]
): void {
  const install = module.install as Record<string, unknown>;
  const path = `modules[${moduleIndex}].install`;
  const url = stringField(install, 'url');
  const sha256 = stringField(install, 'sha256');
  const targetPath = stringField(install, 'targetPath');

  if (!isHttpsUrl(url)) {
    addDiagnostic(diagnostics, {
      code: 'plugin_disallowed_behavior',
      message: `Plugin module "${module.id}" release artifact URL must use https://`,
      path: `${path}.url`,
      severity: 'error',
      moduleId: module.id,
    });
  }

  if (!sha256 || !SHA256_HEX_PATTERN.test(sha256)) {
    addDiagnostic(diagnostics, {
      code: 'plugin_artifact_hash_required',
      message: `Plugin module "${module.id}" release artifact requires a valid sha256`,
      path: `${path}.sha256`,
      severity: 'error',
      moduleId: module.id,
    });
  }

  if (!isSafeRelativePath(targetPath)) {
    addDiagnostic(diagnostics, {
      code: 'plugin_archive_layout_invalid',
      message: `Plugin module "${module.id}" release artifact targetPath must stay relative`,
      path: `${path}.targetPath`,
      severity: 'error',
      moduleId: module.id,
    });
  }
}

function validateCopyAssetInstall(
  module: PluginModule,
  moduleIndex: number,
  diagnostics: PluginDiagnostic[]
): void {
  const install = module.install as Record<string, unknown>;
  const path = `modules[${moduleIndex}].install`;
  const sourcePath = stringField(install, 'sourcePath');
  const targetPath = stringField(install, 'targetPath');

  if (!isSafeRelativePath(sourcePath)) {
    addDiagnostic(diagnostics, {
      code: 'plugin_archive_layout_invalid',
      message: `Plugin module "${module.id}" copy_asset sourcePath must stay relative`,
      path: `${path}.sourcePath`,
      severity: 'error',
      moduleId: module.id,
    });
  }

  if (!isSafeRelativePath(targetPath)) {
    addDiagnostic(diagnostics, {
      code: 'plugin_archive_layout_invalid',
      message: `Plugin module "${module.id}" copy_asset targetPath must stay relative`,
      path: `${path}.targetPath`,
      severity: 'error',
      moduleId: module.id,
    });
  }
}

function validateManualStepInstall(
  module: PluginModule,
  moduleIndex: number,
  diagnostics: PluginDiagnostic[]
): void {
  const install = module.install as Record<string, unknown>;
  const path = `modules[${moduleIndex}].install`;
  const summary = stringField(install, 'summary');
  const docsUrl = stringField(install, 'docs_url');

  if (!summary) {
    addDiagnostic(diagnostics, {
      code: 'plugin_missing_required_field',
      message: `Plugin module "${module.id}" manual_step requires summary`,
      path: `${path}.summary`,
      severity: 'error',
      moduleId: module.id,
    });
  }

  if (!isHttpsUrl(docsUrl)) {
    addDiagnostic(diagnostics, {
      code: 'plugin_missing_required_field',
      message: `Plugin module "${module.id}" manual_step requires docs_url`,
      path: `${path}.docs_url`,
      severity: 'error',
      moduleId: module.id,
    });
  }
}

function validateModuleIds(
  plugin: PluginPackage,
  options: PluginValidationOptions,
  diagnostics: PluginDiagnostic[]
): void {
  const slug = packageSlug(plugin.packageId);
  const seen = new Set<string>();
  const firstPartyIds = new Set(options.firstPartyManifest.modules.map((module) => module.id));
  const existingPluginIds = new Set(options.existingPluginModuleIds ?? []);

  plugin.modules.forEach((module, index) => {
    const path = `modules[${index}].id`;
    const match = MODULE_ID_PATTERN.exec(module.id);
    if (!match || match[1] !== slug) {
      addDiagnostic(diagnostics, {
        code: 'plugin_module_id_invalid',
        message: `Plugin module "${module.id}" must use the plugin.${slug}. namespace`,
        path,
        severity: 'error',
        moduleId: module.id,
        context: { expectedPrefix: `plugin.${slug}.` },
      });
    }

    if (seen.has(module.id) || firstPartyIds.has(module.id) || existingPluginIds.has(module.id)) {
      addDiagnostic(diagnostics, {
        code: 'plugin_module_collision',
        message: `Plugin module "${module.id}" collides with an existing module ID`,
        path,
        severity: 'error',
        moduleId: module.id,
      });
    }
    seen.add(module.id);
  });
}

function validateCategories(plugin: PluginPackage, diagnostics: PluginDiagnostic[]): void {
  plugin.modules.forEach((module, index) => {
    if (!ALLOWED_CATEGORIES.has(module.category as ModuleCategory)) {
      addDiagnostic(diagnostics, {
        code: 'plugin_missing_required_field',
        message: `Plugin module "${module.id}" category "${module.category}" is not a known ACFS category`,
        path: `modules[${index}].category`,
        severity: 'error',
        moduleId: module.id,
        context: { allowedCategories: Array.from(ALLOWED_CATEGORIES).sort() },
      });
    }
  });
}

function validateDependencies(
  plugin: PluginPackage,
  options: PluginValidationOptions,
  diagnostics: PluginDiagnostic[]
): void {
  const firstParty = new Map(options.firstPartyManifest.modules.map((module) => [module.id, module]));
  const own = new Map(plugin.modules.map((module) => [module.id, module]));
  const existingPluginIds = new Set(options.existingPluginModuleIds ?? []);

  plugin.modules.forEach((module, moduleIndex) => {
    for (const dependencyId of module.dependencies ?? []) {
      const path = `modules[${moduleIndex}].dependencies`;
      if (own.has(dependencyId) || firstParty.has(dependencyId)) {
        validateDependencyPhase(module, dependencyId, firstParty, own, diagnostics, path);
        continue;
      }

      if (dependencyId.startsWith('plugin.') && existingPluginIds.has(dependencyId)) {
        validateCapabilityUse(plugin, module, 'cross_plugin_dependency', diagnostics, path);
        continue;
      }

      addDiagnostic(diagnostics, {
        code: 'plugin_dependency_invalid',
        message: `Plugin module "${module.id}" depends on missing module "${dependencyId}"`,
        path,
        severity: 'error',
        moduleId: module.id,
        context: { missingDependency: dependencyId },
      });
    }
  });

  detectPluginDependencyCycles(plugin, diagnostics);
}

function validateDependencyPhase(
  module: PluginModule,
  dependencyId: string,
  firstParty: Map<string, Module>,
  own: Map<string, PluginModule>,
  diagnostics: PluginDiagnostic[],
  path: string
): void {
  const dependency = firstParty.get(dependencyId) ?? own.get(dependencyId);
  const dependencyPhase = dependency?.phase ?? 1;
  if (dependencyPhase > module.phase) {
    addDiagnostic(diagnostics, {
      code: 'plugin_dependency_invalid',
      message: `Plugin module "${module.id}" depends on "${dependencyId}" in a later phase`,
      path,
      severity: 'error',
      moduleId: module.id,
      context: { dependencyId, modulePhase: module.phase, dependencyPhase },
    });
  }
}

function detectPluginDependencyCycles(
  plugin: PluginPackage,
  diagnostics: PluginDiagnostic[]
): void {
  const own = new Map(plugin.modules.map((module) => [module.id, module]));
  const visiting = new Set<string>();
  const visited = new Set<string>();
  const reported = new Set<string>();

  function visit(moduleId: string, path: string[]): void {
    if (visiting.has(moduleId)) {
      const cycleStart = path.indexOf(moduleId);
      const cyclePath = [...path.slice(cycleStart), moduleId];
      const cycleKey = [...new Set(cyclePath)].sort().join(',');
      if (!reported.has(cycleKey)) {
        reported.add(cycleKey);
        addDiagnostic(diagnostics, {
          code: 'plugin_dependency_invalid',
          message: `Plugin dependency cycle detected: ${cyclePath.join(' -> ')}`,
          path: 'modules.dependencies',
          severity: 'error',
          moduleId,
          context: { cyclePath },
        });
      }
      return;
    }

    if (visited.has(moduleId)) return;

    const module = own.get(moduleId);
    if (!module) return;

    visiting.add(moduleId);
    for (const dependencyId of module.dependencies ?? []) {
      if (own.has(dependencyId)) {
        visit(dependencyId, [...path, moduleId]);
      }
    }
    visiting.delete(moduleId);
    visited.add(moduleId);
  }

  for (const module of plugin.modules) {
    visit(module.id, []);
  }
}

function validateGeneratedFunctionCollisions(
  plugin: PluginPackage,
  options: PluginValidationOptions,
  diagnostics: PluginDiagnostic[]
): void {
  const functionOwners = new Map<string, string>();

  for (const module of options.firstPartyManifest.modules) {
    functionOwners.set(toFunctionName(module.id), module.id);
  }

  plugin.modules.forEach((module, index) => {
    const functionName = toFunctionName(module.id);
    const existingOwner = functionOwners.get(functionName);
    if (existingOwner) {
      addDiagnostic(diagnostics, {
        code: 'plugin_generated_function_collision',
        message: `Plugin module "${module.id}" generates function "${functionName}" which collides with "${existingOwner}"`,
        path: `modules[${index}].id`,
        severity: 'error',
        moduleId: module.id,
        context: { functionName, collidingModule: existingOwner },
      });
    }
    functionOwners.set(functionName, module.id);
  });
}

function validateReviewRequiredCapabilities(
  plugin: PluginPackage,
  diagnostics: PluginDiagnostic[]
): void {
  plugin.modules.forEach((module, index) => {
    if (module.run_as === 'root') {
      validateCapabilityUse(plugin, module, 'root_run_as', diagnostics, `modules[${index}].run_as`);
    }
  });
}

function validateTarget(
  plugin: PluginPackage,
  target: PluginValidationTarget | undefined,
  diagnostics: PluginDiagnostic[]
): void {
  if (!target) return;
  if (targetMatches(plugin, target)) return;

  addDiagnostic(diagnostics, {
    code: 'plugin_target_unsupported',
    message: `Plugin package "${plugin.packageId}" does not support ${target.os} ${target.version} ${target.arch} ${target.libc}`,
    path: 'targets',
    severity: 'error',
    context: { ...target },
  });
}

function validateOfflinePolicy(plugin: PluginPackage, diagnostics: PluginDiagnostic[]): void {
  if (plugin.offline.bundlingPolicy === 'bundled') return;

  for (const module of plugin.modules) {
    if (!module.optional && plugin.offline.bundlingPolicy === 'prohibited') {
      addDiagnostic(diagnostics, {
        code: 'plugin_offline_policy_incompatible',
        message: `Required plugin module "${module.id}" cannot be fully offline when bundling is prohibited`,
        path: 'offline.bundlingPolicy',
        severity: 'error',
        moduleId: module.id,
      });
    }
  }
}

function validateModules(
  plugin: PluginPackage,
  options: PluginValidationOptions,
  diagnostics: PluginDiagnostic[]
): void {
  const installers = options.installers ?? {};

  validateModuleIds(plugin, options, diagnostics);
  validateCategories(plugin, diagnostics);
  validateDependencies(plugin, options, diagnostics);
  validateGeneratedFunctionCollisions(plugin, options, diagnostics);
  validateReviewRequiredCapabilities(plugin, diagnostics);

  plugin.modules.forEach((module, index) => {
    validateInstallFields(plugin, module, index, installers, diagnostics);
  });
}

function toManifestModule(plugin: PluginPackage, module: PluginModule): Module {
  const install = module.install as Record<string, unknown>;
  const kind = module.install.kind;
  const verifiedInstaller =
    kind === 'verified_installer'
      ? {
          tool: stringField(install, 'tool') ?? '',
          url: stringField(install, 'url'),
          runner: (stringField(install, 'runner') ?? 'bash') as 'bash' | 'sh',
          env: stringArrayField(install, 'env') ?? [],
          args: stringArrayField(install, 'args') ?? [],
        }
      : undefined;

  return {
    id: module.id,
    description: module.description,
    category: module.category,
    run_as: module.run_as as RunAs,
    verified_installer: verifiedInstaller,
    optional: module.optional,
    enabled_by_default: module.enabled_by_default,
    generated: kind === 'verified_installer',
    phase: module.phase,
    install: [],
    verify: [...module.verify],
    dependencies: module.dependencies ? [...module.dependencies] : undefined,
    docs_url: module.docs_url,
    web: module.web,
    plugin: {
      packageId: plugin.packageId,
      version: plugin.version,
      pluginSha256: plugin.provenance.pluginSha256,
      sourceRef: plugin.provenance.sourceRef,
      sourceCommit: plugin.provenance.sourceCommit,
    },
  };
}

export function validatePluginPackage(
  input: unknown,
  options: PluginValidationOptions
): PluginValidationResult {
  const diagnostics: PluginDiagnostic[] = [];
  const manifestModules: Module[] = [];

  validateTopLevelFields(input, diagnostics);
  scanSecretMaterial(input, diagnostics, '');

  if (!isRecord(input)) {
    addDiagnostic(diagnostics, {
      code: 'plugin_missing_required_field',
      message: 'Plugin package must be a JSON object',
      path: '<root>',
      severity: 'error',
    });
    return { valid: false, diagnostics, manifestModules };
  }

  const plugin = addSchemaDiagnostics(input, diagnostics);
  if (!plugin) {
    return { valid: false, diagnostics, manifestModules };
  }

  validateTarget(plugin, options.target, diagnostics);
  validateModules(plugin, options, diagnostics);
  validateOfflinePolicy(plugin, diagnostics);

  const valid = diagnostics.every((diagnostic) => diagnostic.severity === 'warning');
  if (valid) {
    manifestModules.push(...plugin.modules.map((module) => toManifestModule(plugin, module)));
  }

  return {
    valid,
    diagnostics,
    package: plugin,
    manifestModules,
  };
}

export function formatPluginDiagnostics(result: PluginValidationResult): string {
  if (result.valid) {
    return 'Plugin validation passed';
  }

  const lines = ['Plugin validation failed:', ''];
  for (const diagnostic of result.diagnostics) {
    const moduleLabel = diagnostic.moduleId ? ` ${diagnostic.moduleId}` : '';
    lines.push(
      `  [${diagnostic.code}]${moduleLabel} ${diagnostic.path}: ${diagnostic.message}`
    );
  }
  lines.push('');
  lines.push(`Total: ${result.diagnostics.length} diagnostic(s)`);
  return lines.join('\n');
}
