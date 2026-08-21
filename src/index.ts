import type { Row } from 'postgres';
import { createSql } from './db.js';

async function main(): Promise<void> {
  const sql = createSql();

  await sql`
    DROP TABLE IF EXISTS poc_items CASCADE;
    CREATE TABLE poc_items (id SERIAL PRIMARY KEY, name TEXT, price INT);
    INSERT INTO poc_items (name, price) VALUES ('widget', 10), ('gadget', 25), ('gizmo', 5);
  `.simple();

  // Three unrelated SELECTs, sent as one semicolon-separated string with no
  // parameter placeholders. .simple() puts the whole thing in a single
  // Postgres simple-query message - one round trip for all three - and
  // resolves with one row array per statement, in order.
  const runBatch = () =>
    sql`
      SELECT count(*) FROM poc_items;
      SELECT name FROM poc_items WHERE price > 10;
      SELECT now();
    `.simple();

  const results = (await runBatch()) as unknown as Row[][];
  results.forEach((rows, statementIndex) => {
    for (const row of rows) {
      console.log(`statement ${statementIndex} ->`, Object.values(row).join(' | '));
    }
  });

  // Now put a number on it: batched vs. the same 3 queries sent one at a
  // time, each awaited before the next fires.
  const iterations = 200;

  const batchedStart = performance.now();
  for (let i = 0; i < iterations; i++) {
    await runBatch();
  }
  const batchedMs = performance.now() - batchedStart;

  const separateStart = performance.now();
  for (let i = 0; i < iterations; i++) {
    await sql`SELECT count(*) FROM poc_items`;
    await sql`SELECT name FROM poc_items WHERE price > 10`;
    await sql`SELECT now()`;
  }
  const separateMs = performance.now() - separateStart;

  console.log(`\n--- timing over ${iterations} iterations ---`);
  console.log(`batched (1 round trip):    ${batchedMs.toFixed(2)}ms  (${(batchedMs / iterations).toFixed(3)}ms/iter)`);
  console.log(`separate (3 round trips):  ${separateMs.toFixed(2)}ms  (${(separateMs / iterations).toFixed(3)}ms/iter)`);

  await sql.end();
}

main().catch((err: unknown) => {
  console.error(err);
  process.exitCode = 1;
});
