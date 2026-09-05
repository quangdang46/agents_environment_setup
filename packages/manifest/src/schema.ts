/**
 * ACFS Manifest Schema
 * Zod schema definitions for validating manifest files
 */

import { z } from 'zod';

/**
 * Schema for manifest defaults
 */
export const ManifestDefaultsSchema = z.object({
  user: z
    .string()
    .min(1, 'User cannot be empty')
    .refine((s) => s.trim().length > 0, 'User cannot be only whitespace'),
  workspace_root: z
    .string()
    .min(1, 'Workspace root cannot be empty')
    .refine((s) => s.trim().length > 0, 'Workspace root cannot be only whitespace'),
  mode: z.enum(['vibe', 'safe']).default('vibe'),
});

/**
 * Schema for a single module
 */
const RunAsSchema = z.enum(['target_user', 'root', 'current']);

/**
 * Allowlist of verified installer runners.
 * SECURITY: Only allow known-safe shell interpreters to prevent command injection.
 * Expand only if there is a concrete, vetted need.
 */
const VerifiedInstallerRunnerSchema = z.enum(['bash', 'sh'], {
  error: 'verified_installer.runner must be "bash" or "sh" (security: runner allowlist)',
});

const VerifiedInstallerSchema = z
  .object({
    tool: z
      .string()
      .min(1, 'Verified installer tool cannot be empty')
      .regex(
        /^[a-z][a-z0-9_]*$/,
        'Tool name must be lowercase alphanumeric with underscores (e.g., "bun", "claude", "mcp_agent_mail")'
      ),
    // Optional canonical URL for drift detection against checksums.yaml.
    // SECURITY: require HTTPS because these installers are downloaded and executed.
    url: z
      .string()
      .url('verified_installer.url must be a valid URL')
      .refine(
        (value) => value.startsWith('https://'),
        'verified_installer.url must use https://'
      )
      .optional(),
    fallback_url: z
      .string()
      .url('verified_installer.fallback_url must be a valid URL')
      .refine(
        (value) => value.startsWith('https://'),
        'verified_installer.fallback_url must use https://'
      )
      .optional(),
    runner: VerifiedInstallerRunnerSchema,
    env: z
      .array(
        z
          .string()
          .regex(
            /^[A-Za-z_][A-Za-z0-9_]*=.*$/,
            'Verified installer env entries must be KEY=value assignments'
          )
      )
      .default([]),
    args: z.array(z.string()).default([]),
    // Run installer in detached tmux session (prevents blocking for long-running services)
    run_in_tmux: z.boolean().default(false),
  })
  .refine((installer) => installer.fallback_url === undefined, {
    path: ['fallback_url'],
    message:
      'verified_installer.fallback_url is unsupported. Verified installers fail closed; remove fallback_url.',
  });

export const ModuleSchema = z
  .object({
    id: z
      .string()
      .min(1, 'Module ID cannot be empty')
      .regex(
        /^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$/,
        'Module ID must be lowercase with dots (e.g., "shell.zsh", "lang.bun")'
      ),
    description: z
      .string()
      .min(1, 'Description cannot be empty')
      .refine((s) => s.trim().length > 0, 'Description cannot be only whitespace')
      .refine(
        (s) => !/[\n\r\t]/.test(s),
        'Description must be single-line with no tabs (newlines break generated bash scripts; tabs corrupt the tab-delimited doctor check records)'
      ),

    // SECURITY: Category is used in generated script filenames (install_<category>.sh)
    // and function names (install_<category>). Must be validated to prevent path traversal
    // or command injection in generated scripts.
    category: z
      .string()
      .regex(
        /^[a-z][a-z0-9_]*$/,
        'Category must be lowercase alphanumeric with underscores (e.g., "shell", "lang_tools")'
      )
      .optional(),

    // Execution context
    run_as: RunAsSchema.default('target_user'),

    // Verified installer reference
    verified_installer: VerifiedInstallerSchema.optional(),

    // Installation behavior
    optional: z.boolean().default(false),
    enabled_by_default: z.boolean().default(true),
    installed_check: z
      .object({
        run_as: RunAsSchema.default('target_user'),
        command: z
          .string()
          .min(1, 'Installed check command cannot be empty')
          .refine((s) => s.trim().length > 0, 'Installed check command cannot be only whitespace'),
      })
      .optional(),
    pre_install_check: z
      .object({
        run_as: RunAsSchema.default('target_user'),
        command: z
          .string()
          .min(1, 'Pre-install check command cannot be empty')
          .refine((s) => s.trim().length > 0, 'Pre-install check command cannot be only whitespace'),
        skip_message: z
          .string()
          .min(1, 'Pre-install check skip_message cannot be empty')
          .refine((s) => s.trim().length > 0, 'Pre-install check skip_message cannot be only whitespace'),
      })
      .optional(),
    generated: z.boolean().default(true),

    phase: z.number().int().min(1).max(10).optional(),

    // Install steps are shell strings (executed via run_as_*_shell).
    // Allow empty when verified_installer is provided.
    install: z.array(z.string()).default([]),
    verify: z.array(z.string()).min(1, 'At least one verify command required'),
    dependencies: z.array(z.string()).optional(),
    notes: z.array(z.string()).optional(),
    post_install_message: z
      .string()
      .min(1, 'post_install_message cannot be empty')
      .refine(
        (s) => s.trim().length > 0,
        'post_install_message cannot be only whitespace'
      )
      .optional(),
    tags: z.array(z.string()).optional(),
    docs_url: z.string().url().optional(),
    aliases: z.array(z.string()).optional(),
  })
  .refine(
    (module) =>
      module.generated === false ||
      module.verified_installer !== undefined ||
      module.install.length > 0,
    {
      message:
        'Module must define verified_installer or install commands (or set generated: false).',
    }
  );

/**
 * Schema for the complete manifest
 */
export const ManifestSchema = z.object({
  version: z.number().int().positive('Version must be a positive integer'),
  name: z
    .string()
    .min(1, 'Name cannot be empty')
    .refine((s) => s.trim().length > 0, 'Name cannot be only whitespace'),
  id: z
    .string()
    .min(1, 'ID cannot be empty')
    .regex(/^[a-z][a-z0-9_]*$/, 'ID must be lowercase alphanumeric with underscores'),
  defaults: ManifestDefaultsSchema,
  modules: z.array(ModuleSchema).min(1, 'At least one module required'),
});

/**
 * Type inference from schemas
 */
export type ManifestDefaultsInput = z.input<typeof ManifestDefaultsSchema>;
export type ManifestDefaultsOutput = z.output<typeof ManifestDefaultsSchema>;

export type ModuleInput = z.input<typeof ModuleSchema>;
export type ModuleOutput = z.output<typeof ModuleSchema>;

export type ManifestInput = z.input<typeof ManifestSchema>;
export type ManifestOutput = z.output<typeof ManifestSchema>;
