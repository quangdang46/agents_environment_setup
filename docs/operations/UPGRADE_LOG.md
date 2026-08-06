# Dependency Upgrade Log

**Date:** 2026-01-24  |  **Project:** agents_environment_setup  |  **Package Manager:** Bun (monorepo)

## Summary
- **Updated:** 6
- **Skipped:** 0
- **Failed:** 0
- **Needs attention:** 0

## Updates

### next: 16.1.0 → 16.1.4
- **Breaking:** None (patch release)
- **Package:** `apps/web`
- **Tests:** Build successful

### eslint-config-next: 16.1.0 → 16.1.4
- **Breaking:** None (patch release)
- **Package:** `apps/web`
- **Tests:** Build successful

### @next/third-parties: ^16.1.0 → ^16.1.4
- **Breaking:** None (patch release)
- **Package:** `apps/web`
- **Tests:** Build successful

### @playwright/test: ^1.57.0 → ^1.58.0
- **Breaking:** None expected (minor version)
- **Package:** `apps/web`
- **Tests:** Build successful

### yaml: ^2.7.0 → ^2.8.2
- **Breaking:** None expected (minor version)
- **Package:** `packages/manifest`
- **Tests:** Type-check and build successful

### zod: ^3.24.1 → ^4.3.6
- **Breaking:** Yes - MAJOR version
- **Package:** `packages/manifest`
- **Migration:**
  - Changed `errorMap` to `error` in `z.enum()` call (schema.ts:33)
  - No other changes needed - existing code was already using `error.issues` (Zod 4 compatible)
- **Tests:** All 137 tests pass
- **Build:** Successful

---

## Verification
- `bun install` - Success
- `bun run type-check` - All workspaces passed
- `bun test` - All 137 manifest tests pass
- `bun run build` - All workspaces build successfully

---

## Sources
- [Zod v4 Migration Guide](https://zod.dev/v4/changelog)
- [Zod v4 Release Notes](https://zod.dev/v4)
