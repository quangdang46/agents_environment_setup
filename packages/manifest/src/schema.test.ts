/**
 * Tests for ACFS Manifest Schema (Zod schemas)
 * Related: bead dvt.2
 *
 * Tests schema validation behavior, error messages, and defaults.
 */

import { describe, test, expect } from 'bun:test';
import {
  ManifestSchema,
  ManifestDefaultsSchema,
  ModuleSchema,
} from './schema.js';

describe('ManifestDefaultsSchema', () => {
  test('validates complete defaults', () => {
    const result = ManifestDefaultsSchema.safeParse({
      user: 'ubuntu',
      workspace_root: '/data/projects',
      mode: 'vibe',
    });
    expect(result.success).toBe(true);
  });

  test('applies default mode when not specified', () => {
    const result = ManifestDefaultsSchema.safeParse({
      user: 'ubuntu',
      workspace_root: '/data/projects',
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.mode).toBe('vibe');
    }
  });

  test('rejects empty user', () => {
    const result = ManifestDefaultsSchema.safeParse({
      user: '',
      workspace_root: '/data/projects',
      mode: 'vibe',
    });
    expect(result.success).toBe(false);
  });

  test('rejects empty workspace_root', () => {
    const result = ManifestDefaultsSchema.safeParse({
      user: 'ubuntu',
      workspace_root: '',
      mode: 'vibe',
    });
    expect(result.success).toBe(false);
  });

  test('accepts safe mode', () => {
    const result = ManifestDefaultsSchema.safeParse({
      user: 'ubuntu',
      workspace_root: '/data',
      mode: 'safe',
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.mode).toBe('safe');
    }
  });

  test('rejects invalid mode', () => {
    const result = ManifestDefaultsSchema.safeParse({
      user: 'ubuntu',
      workspace_root: '/data',
      mode: 'invalid',
    });
    expect(result.success).toBe(false);
  });
});

describe('ModuleSchema', () => {
  const validMinimalModule = {
    id: 'base.system',
    description: 'Base system packages',
    install: ['apt-get update'],
    verify: ['curl --version'],
  };

  test('validates minimal module', () => {
    const result = ModuleSchema.safeParse(validMinimalModule);
    expect(result.success).toBe(true);
  });

  test('applies default values', () => {
    const result = ModuleSchema.safeParse(validMinimalModule);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.run_as).toBe('target_user');
      expect(result.data.optional).toBe(false);
      expect(result.data.enabled_by_default).toBe(true);
      expect(result.data.generated).toBe(true);
    }
  });

  test('rejects empty module ID', () => {
    const result = ModuleSchema.safeParse({
      ...validMinimalModule,
      id: '',
    });
    expect(result.success).toBe(false);
  });

  test('rejects uppercase module ID', () => {
    const result = ModuleSchema.safeParse({
      ...validMinimalModule,
      id: 'Base.System',
    });
    expect(result.success).toBe(false);
  });

  test('rejects module ID starting with number', () => {
    const result = ModuleSchema.safeParse({
      ...validMinimalModule,
      id: '1base.system',
    });
    expect(result.success).toBe(false);
  });

  test('accepts module ID with underscores', () => {
    const result = ModuleSchema.safeParse({
      ...validMinimalModule,
      id: 'stack.mcp_agent_mail',
    });
    expect(result.success).toBe(true);
  });

  test('accepts multi-segment module ID', () => {
    const result = ModuleSchema.safeParse({
      ...validMinimalModule,
      id: 'cloud.aws.s3',
    });
    expect(result.success).toBe(true);
  });

  test('rejects empty description', () => {
    const result = ModuleSchema.safeParse({
      ...validMinimalModule,
      description: '',
    });
    expect(result.success).toBe(false);
  });

  test('rejects empty verify array', () => {
    const result = ModuleSchema.safeParse({
      ...validMinimalModule,
      verify: [],
    });
    expect(result.success).toBe(false);
  });

  test('accepts empty install array when verified_installer is provided', () => {
    const result = ModuleSchema.safeParse({
      id: 'lang.bun',
      description: 'Bun runtime',
      install: [],
      verify: ['bun --version'],
      verified_installer: {
        tool: 'bun',
        runner: 'bash',
        args: [],
      },
    });
    expect(result.success).toBe(true);
  });

  test('rejects empty install array without verified_installer when generated is true', () => {
    const result = ModuleSchema.safeParse({
      id: 'lang.bun',
      description: 'Bun runtime',
      install: [],
      verify: ['bun --version'],
      generated: true,
    });
    expect(result.success).toBe(false);
  });

  test('accepts empty install array when generated is false', () => {
    const result = ModuleSchema.safeParse({
      id: 'lang.bun',
      description: 'Bun runtime',
      install: [],
      verify: ['bun --version'],
      generated: false,
    });
    expect(result.success).toBe(true);
  });

  test('validates run_as enum', () => {
    const runAsValues = ['target_user', 'root', 'current'];
    for (const runAs of runAsValues) {
      const result = ModuleSchema.safeParse({
        ...validMinimalModule,
        run_as: runAs,
      });
      expect(result.success).toBe(true);
    }
  });

  test('rejects invalid run_as value', () => {
    const result = ModuleSchema.safeParse({
      ...validMinimalModule,
      run_as: 'invalid',
    });
    expect(result.success).toBe(false);
  });

  test('validates phase range (1-10)', () => {
    // Valid phases
    for (const phase of [1, 5, 10]) {
      const result = ModuleSchema.safeParse({
        ...validMinimalModule,
        phase,
      });
      expect(result.success).toBe(true);
    }

    // Invalid phases
    for (const phase of [0, 11, -1, 100]) {
      const result = ModuleSchema.safeParse({
        ...validMinimalModule,
        phase,
      });
      expect(result.success).toBe(false);
    }
  });

  test('validates dependencies array', () => {
    const result = ModuleSchema.safeParse({
      ...validMinimalModule,
      dependencies: ['base.core', 'lang.bun'],
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.dependencies).toEqual(['base.core', 'lang.bun']);
    }
  });

  test('validates tags array', () => {
    const result = ModuleSchema.safeParse({
      ...validMinimalModule,
      tags: ['critical', 'runtime'],
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.tags).toContain('critical');
      expect(result.data.tags).toContain('runtime');
    }
  });

  test('validates notes array', () => {
    const result = ModuleSchema.safeParse({
      ...validMinimalModule,
      notes: ['First note', 'Second note'],
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.notes).toHaveLength(2);
    }
  });

  test('validates docs_url as valid URL', () => {
    const result = ModuleSchema.safeParse({
      ...validMinimalModule,
      docs_url: 'https://docs.example.com/module',
    });
    expect(result.success).toBe(true);
  });

  test('rejects invalid docs_url', () => {
    const result = ModuleSchema.safeParse({
      ...validMinimalModule,
      docs_url: 'not-a-valid-url',
    });
    expect(result.success).toBe(false);
  });

  test('validates verified_installer object', () => {
    const result = ModuleSchema.safeParse({
      id: 'lang.bun',
      description: 'Bun runtime',
      install: [],
      verify: ['bun --version'],
      verified_installer: {
        tool: 'bun',
        runner: 'bash',
        args: ['-s', '--'],
      },
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.verified_installer?.tool).toBe('bun');
      expect(result.data.verified_installer?.runner).toBe('bash');
      expect(result.data.verified_installer?.args).toEqual(['-s', '--']);
    }
  });

  test('validates verified_installer env assignments', () => {
    const result = ModuleSchema.safeParse({
      id: 'stack.ru',
      description: 'Repo Updater',
      install: [],
      verify: ['ru --version'],
      verified_installer: {
        tool: 'ru',
        runner: 'bash',
        env: ['RU_NON_INTERACTIVE=1'],
        args: [],
      },
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.verified_installer?.env).toEqual(['RU_NON_INTERACTIVE=1']);
    }
  });

  test('accepts https verified_installer url metadata', () => {
    const result = ModuleSchema.safeParse({
      id: 'stack.rch',
      description: 'Remote Compilation Helper',
      install: [],
      verify: ['rch --version'],
      verified_installer: {
        tool: 'rch',
        url: 'https://raw.githubusercontent.com/Dicklesworthstone/remote_compilation_helper/main/install.sh',
        runner: 'bash',
      },
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.verified_installer?.url).toBe(
        'https://raw.githubusercontent.com/Dicklesworthstone/remote_compilation_helper/main/install.sh'
      );
    }
  });

  test('rejects non-https verified_installer url metadata', () => {
    const result = ModuleSchema.safeParse({
      id: 'stack.rch',
      description: 'Remote Compilation Helper',
      install: [],
      verify: ['rch --version'],
      verified_installer: {
        tool: 'rch',
        url: 'http://example.com/install.sh',
        runner: 'bash',
      },
    });
    expect(result.success).toBe(false);
  });

  test('rejects invalid verified_installer env entries', () => {
    const result = ModuleSchema.safeParse({
      id: 'stack.ru',
      description: 'Repo Updater',
      install: [],
      verify: ['ru --version'],
      verified_installer: {
        tool: 'ru',
        runner: 'bash',
        env: ['not-an-assignment'],
        args: [],
      },
    });
    expect(result.success).toBe(false);
  });

  test('rejects verified_installer with python runner (security)', () => {
    const result = ModuleSchema.safeParse({
      id: 'lang.python',
      description: 'Python',
      install: [],
      verify: ['python --version'],
      verified_installer: {
        tool: 'python',
        runner: 'python',
        args: [],
      },
    });
    expect(result.success).toBe(false);
  });

  test('accepts sh as verified_installer runner', () => {
    const result = ModuleSchema.safeParse({
      id: 'tools.test',
      description: 'Test',
      install: [],
      verify: ['test --version'],
      verified_installer: {
        tool: 'test',
        runner: 'sh',
        args: [],
      },
    });
    expect(result.success).toBe(true);
  });

  test('validates installed_check object', () => {
    const result = ModuleSchema.safeParse({
      ...validMinimalModule,
      installed_check: {
        run_as: 'target_user',
        command: 'which curl',
      },
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.installed_check?.command).toBe('which curl');
    }
  });

  test('rejects installed_check with empty command', () => {
    const result = ModuleSchema.safeParse({
      ...validMinimalModule,
      installed_check: {
        run_as: 'target_user',
        command: '',
      },
    });
    expect(result.success).toBe(false);
  });

  test('validates pre_install_check object', () => {
    const result = ModuleSchema.safeParse({
      ...validMinimalModule,
      pre_install_check: {
        run_as: 'target_user',
        command: 'command -v claude >/dev/null 2>&1',
        skip_message: 'Skipping PCR - Claude Code not found',
      },
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.pre_install_check?.command).toBe('command -v claude >/dev/null 2>&1');
      expect(result.data.pre_install_check?.skip_message).toBe('Skipping PCR - Claude Code not found');
    }
  });

  test('validates aliases array', () => {
    const result = ModuleSchema.safeParse({
      ...validMinimalModule,
      aliases: ['sys', 'system'],
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.aliases).toContain('sys');
      expect(result.data.aliases).toContain('system');
    }
  });
});

describe('ManifestSchema', () => {
  const validMinimalManifest = {
    version: 1,
    name: 'Test Manifest',
    id: 'test',
    defaults: {
      user: 'ubuntu',
      workspace_root: '/data/projects',
      mode: 'vibe',
    },
    modules: [
      {
        id: 'base.system',
        description: 'Base system',
        install: ['apt-get update'],
        verify: ['curl --version'],
      },
    ],
  };

  test('validates complete manifest', () => {
    const result = ManifestSchema.safeParse(validMinimalManifest);
    expect(result.success).toBe(true);
  });

  test('rejects negative version', () => {
    const result = ManifestSchema.safeParse({
      ...validMinimalManifest,
      version: -1,
    });
    expect(result.success).toBe(false);
  });

  test('rejects zero version', () => {
    const result = ManifestSchema.safeParse({
      ...validMinimalManifest,
      version: 0,
    });
    expect(result.success).toBe(false);
  });

  test('rejects non-integer version', () => {
    const result = ManifestSchema.safeParse({
      ...validMinimalManifest,
      version: 1.5,
    });
    expect(result.success).toBe(false);
  });

  test('rejects empty name', () => {
    const result = ManifestSchema.safeParse({
      ...validMinimalManifest,
      name: '',
    });
    expect(result.success).toBe(false);
  });

  test('rejects empty id', () => {
    const result = ManifestSchema.safeParse({
      ...validMinimalManifest,
      id: '',
    });
    expect(result.success).toBe(false);
  });

  test('rejects uppercase id', () => {
    const result = ManifestSchema.safeParse({
      ...validMinimalManifest,
      id: 'TestManifest',
    });
    expect(result.success).toBe(false);
  });

  test('accepts id with underscores', () => {
    const result = ManifestSchema.safeParse({
      ...validMinimalManifest,
      id: 'test_manifest',
    });
    expect(result.success).toBe(true);
  });

  test('rejects empty modules array', () => {
    const result = ManifestSchema.safeParse({
      ...validMinimalManifest,
      modules: [],
    });
    expect(result.success).toBe(false);
  });

  test('validates multiple modules', () => {
    const result = ManifestSchema.safeParse({
      ...validMinimalManifest,
      modules: [
        {
          id: 'base.system',
          description: 'Base system',
          install: ['apt-get update'],
          verify: ['curl --version'],
        },
        {
          id: 'shell.zsh',
          description: 'Zsh shell',
          dependencies: ['base.system'],
          install: ['apt-get install zsh'],
          verify: ['zsh --version'],
        },
      ],
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.modules).toHaveLength(2);
    }
  });

  test('rejects missing defaults', () => {
    const { defaults, ...withoutDefaults } = validMinimalManifest;
    const result = ManifestSchema.safeParse(withoutDefaults);
    expect(result.success).toBe(false);
  });

  test('provides descriptive error messages', () => {
    const result = ManifestSchema.safeParse({
      version: -1,
      name: '',
      id: 'UPPERCASE',
      defaults: { user: '', workspace_root: '', mode: 'invalid' },
      modules: [],
    });
    expect(result.success).toBe(false);
    if (!result.success) {
      // Should have multiple errors
      expect(result.error.issues.length).toBeGreaterThan(0);
    }
  });
});
