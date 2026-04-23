// Promotion is handled by prisma/scripts/promote-staging.ts (TypeScript/Prisma),
// invoked via `pnpm scraper:promote` or `node scripts/run-promoter.mjs`.
//
// The Go scraper only writes to rebates_staging.  It never touches rebates directly.
package db
