import { describe, expect, test } from 'bun:test';
import {
  formatPluginDiagnostics,
  validatePluginPackage,
  type PluginValidationOptions,
} from './plugin.js';
import type { Manifest } from './types.js';

const CHECKSUM = 'a'.repeat(64);
const INSTALLER_URL = 'https://example.com/install.sh';

function firstPartyManifest(): Manifest {
  return {
    version: 1,
    name: 'ACFS Test Manifest',
    id: 'acfs',
    defaults: {
      user: 'ubuntu',
      workspace_root: '/data/projects',
      mode: 'vibe',
    },
    modules: [
      {
        id: 'base.system',
        description: 'Base system',
        category: 'base',
        phase: 1,
        run_as: 'target_user',
        optional: false,
        enabled_by_default: true,
        generated: true,
        install: ['echo base'],
        verify: ['true'],
      },
      {
        id: 'lang.bun',
        description: 'Bun runtime',
        category: 'lang',
        phase: 6,
        run_as: 'target_user',
        optional: false,
        enabled_by_default: true,
        generated: true,
        install: ['echo bun'],
        verify: ['bun --version'],
      },
    ],
  };
}

function validationOptions(overrides: Partial<PluginValidationOptions> = {}): PluginValidationOptions {
  return {
    firstPartyManifest: firstPartyManifest(),
    installers: {
      example_tools: {
        url: INSTALLER_URL,
        sha256: CHECKSUM,
      },
    },
    target: {
      os: 'ubuntu',
      version: '25.10',
      arch: 'x86_64',
      libc: 'glibc',
    },
    ...overrides,
  };
}

function validPlugin(): Record<string, unknown> {
  return {
    schema: 'acfs.plugin-package.v1',
    schemaVersion: 1,
    packageId: 'example.tools',
    displayName: 'Example Tools',
    version: '1.2.3',
    description: 'Installable ACFS modules for Example Tools.',
    publisher: {
      name: 'Example Maintainers',
      contactUrl: 'https://example.com/security',
      sourceUrl: 'https://github.com/example/acfs-plugin-example',
    },
    license: 'Apache-2.0',
    docsUrl: 'https://example.com/acfs-plugin-example',
    provenance: {
      generatedAt: '2026-05-08T00:00:00Z',
      sourceRef: 'main',
      sourceCommit: '0123456789abcdef0123456789abcdef01234567',
      pluginSha256: CHECKSUM,
      acfsManifestVersion: 1,
    },
    targets: [
      {
        os: 'ubuntu',
        versions: ['25.10'],
        arch: ['x86_64'],
        libc: ['glibc'],
      },
    ],
    capabilities: {
      allowed: ['verified_installer'],
      reviewRequired: ['root_run_as', 'cross_plugin_dependency'],
      disallowed: ['arbitrary_shell', 'secret_values'],
    },
    modules: [
      {
        id: 'plugin.example_tools.cli',
        description: 'Example command-line tool.',
        category: 'tools',
        phase: 6,
        run_as: 'target_user',
        optional: false,
        enabled_by_default: true,
        dependencies: ['lang.bun'],
        install: {
          kind: 'verified_installer',
          tool: 'example_tools',
          url: INSTALLER_URL,
          runner: 'bash',
          args: [],
          env: [],
        },
        verify: ['example --version'],
        docs_url: 'https://example.com/acfs-plugin-example/cli',
      },
    ],
    offline: {
      bundlingPolicy: 'metadata_only',
      liveAuthRequired: false,
      providerInteractionRequired: false,
    },
    extensions: {},
  };
}

function diagnosticCodes(plugin: Record<string, unknown>, options = validationOptions()): string[] {
  return validatePluginPackage(plugin, options).diagnostics.map((diagnostic) => diagnostic.code);
}

describe('validatePluginPackage', () => {
  test('accepts a valid verified-installer plugin and returns normalized modules', () => {
    const result = validatePluginPackage(validPlugin(), validationOptions());

    expect(result.valid).toBe(true);
    expect(result.diagnostics).toHaveLength(0);
    expect(result.manifestModules).toHaveLength(1);
    expect(result.manifestModules[0].id).toBe('plugin.example_tools.cli');
    expect(result.manifestModules[0].verified_installer?.tool).toBe('example_tools');
    expect(result.manifestModules[0].verified_installer?.url).toBe(INSTALLER_URL);
    expect(result.manifestModules[0].plugin).toEqual({
      packageId: 'example.tools',
      version: '1.2.3',
      pluginSha256: CHECKSUM,
      sourceRef: 'main',
      sourceCommit: '0123456789abcdef0123456789abcdef01234567',
    });
  });

  test('rejects unsupported schema versions', () => {
    const plugin = validPlugin();
    plugin.schemaVersion = 99;

    expect(diagnosticCodes(plugin)).toContain('plugin_schema_unsupported');
  });

  test('detects duplicate plugin module IDs', () => {
    const plugin = validPlugin();
    const modules = plugin.modules as Record<string, unknown>[];
    modules.push({ ...modules[0] });

    expect(diagnosticCodes(plugin)).toContain('plugin_module_collision');
  });

  test('detects first-party module ID collisions', () => {
    const plugin = validPlugin();
    const modules = plugin.modules as Record<string, unknown>[];
    modules[0] = { ...modules[0], id: 'lang.bun' };

    const codes = diagnosticCodes(plugin);
    expect(codes).toContain('plugin_module_collision');
    expect(codes).toContain('plugin_generated_function_collision');
  });

  test('rejects missing verified-installer checksum entries', () => {
    const result = validatePluginPackage(
      validPlugin(),
      validationOptions({ installers: {} })
    );

    expect(result.valid).toBe(false);
    expect(result.diagnostics[0].code).toBe('plugin_verified_installer_checksum_required');
    expect(formatPluginDiagnostics(result)).toContain(
      'plugin_verified_installer_checksum_required'
    );
  });

  test('rejects verified-installer URL drift from checksums.yaml', () => {
    const result = validatePluginPackage(
      validPlugin(),
      validationOptions({
        installers: {
          example_tools: {
            url: 'https://example.com/other-install.sh',
            sha256: CHECKSUM,
          },
        },
      })
    );

    expect(result.valid).toBe(false);
    expect(result.diagnostics.some((diagnostic) => diagnostic.code === 'plugin_verified_installer_checksum_required')).toBe(true);
  });

  test('rejects unsupported verified-installer runners', () => {
    const plugin = validPlugin();
    const modules = plugin.modules as Record<string, unknown>[];
    modules[0] = {
      ...modules[0],
      install: {
        ...((modules[0].install as Record<string, unknown>) ?? {}),
        runner: 'python',
      },
    };

    expect(diagnosticCodes(plugin)).toContain('plugin_disallowed_behavior');
  });

  test('rejects dependency on missing modules', () => {
    const plugin = validPlugin();
    const modules = plugin.modules as Record<string, unknown>[];
    modules[0] = { ...modules[0], dependencies: ['missing.module'] };

    expect(diagnosticCodes(plugin)).toContain('plugin_dependency_invalid');
  });

  test('rejects unsupported target platforms', () => {
    const result = validatePluginPackage(
      validPlugin(),
      validationOptions({
        target: {
          os: 'ubuntu',
          version: '25.10',
          arch: 'aarch64',
          libc: 'glibc',
        },
      })
    );

    expect(result.valid).toBe(false);
    expect(result.diagnostics[0].code).toBe('plugin_target_unsupported');
  });

  test('rejects disallowed executable install fields', () => {
    const plugin = validPlugin();
    const modules = plugin.modules as Record<string, unknown>[];
    modules[0] = {
      ...modules[0],
      install: {
        kind: 'verified_installer',
        tool: 'example_tools',
        url: INSTALLER_URL,
        runner: 'bash',
        command: 'curl https://example.com/install.sh | bash',
      },
    };

    expect(diagnosticCodes(plugin)).toContain('plugin_disallowed_behavior');
  });

  test('rejects non-normalized plugin artifact target paths', () => {
    const plugin = validPlugin();
    plugin.capabilities = {
      allowed: ['release_artifact'],
      reviewRequired: ['root_run_as', 'cross_plugin_dependency'],
      disallowed: ['arbitrary_shell', 'secret_values'],
    };
    const modules = plugin.modules as Record<string, unknown>[];
    modules[0] = {
      ...modules[0],
      install: {
        kind: 'release_artifact',
        url: 'https://example.com/tool.tar.gz',
        sha256: CHECKSUM,
        targetPath: 'bin/./example',
      },
    };

    expect(diagnosticCodes(plugin)).toContain('plugin_archive_layout_invalid');
  });

  test('rejects credential-bearing fields without echoing values', () => {
    const plugin = validPlugin();
    const modules = plugin.modules as Record<string, unknown>[];
    const forbiddenKey = ['to', 'ken'].join('');
    modules[0] = { ...modules[0], [forbiddenKey]: 'redacted-fixture-value' };

    const result = validatePluginPackage(plugin, validationOptions());
    const secretDiagnostic = result.diagnostics.find(
      (diagnostic) => diagnostic.code === 'plugin_secret_material_refused'
    );

    expect(secretDiagnostic).toBeDefined();
    expect(secretDiagnostic?.context?.value).toBe('<redacted>');
    expect(formatPluginDiagnostics(result)).not.toContain('redacted-fixture-value');
  });

  test('surfaces review-required root execution', () => {
    const plugin = validPlugin();
    const modules = plugin.modules as Record<string, unknown>[];
    modules[0] = { ...modules[0], run_as: 'root' };

    const result = validatePluginPackage(plugin, validationOptions());

    expect(result.valid).toBe(false);
    expect(result.diagnostics.some((diagnostic) => diagnostic.code === 'plugin_review_required')).toBe(true);
  });
});
