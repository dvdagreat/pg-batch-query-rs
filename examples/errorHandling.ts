import postgres from 'postgres';
import { createSql } from '../src/db.js';

// Gotcha: when a multi-statement batch has more than one statement,
// Postgres wraps them in one implicit transaction. If a later statement
// fails, everything earlier in that same batch gets rolled back too - even
// successful inserts. This example triggers that failure on purpose and
// shows how to read the error in TypeScript, then proves the rollback by
// re-checking the table.
async function main(): Promise<void> {
  const sql = createSql();

  await sql`
    DROP TABLE IF EXISTS poc_accounts;
    CREATE TABLE poc_accounts (id SERIAL PRIMARY KEY, email TEXT UNIQUE);
  `.simple();

  // Statement 1 succeeds, statement 2 violates the UNIQUE constraint,
  // statement 3 never runs at all.
  try {
    await sql`
      INSERT INTO poc_accounts (email) VALUES ('a@example.com');
      INSERT INTO poc_accounts (email) VALUES ('a@example.com');
      SELECT count(*) FROM poc_accounts;
    `.simple();
    console.log('batch succeeded (unexpected)');
  } catch (err) {
    if (err instanceof postgres.PostgresError) {
      console.log(`batch failed: ${err.message}`);
      if (err.code === '23505') {
        console.log('-> it was a unique violation, as expected');
      }
    } else {
      console.log(`batch failed: ${String(err)}`);
    }
  }

  // The implicit transaction rolled back statement 1 as well, so the table
  // should still be empty even though the first insert looked fine on its own.
  const [row] = await sql<{ count: string }[]>`SELECT count(*) FROM poc_accounts`;
  console.log(`rows in poc_accounts after the failed batch: ${row?.count}`);

  await sql.end();
}

main().catch((err: unknown) => {
  console.error(err);
  process.exitCode = 1;
});
