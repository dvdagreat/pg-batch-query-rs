import type { Row } from 'postgres';
import { createSql } from '../src/db.js';

// A multi-statement batch hands back one untyped row array per statement.
// This example maps each statement's rows into a type of our own, so
// callers get the same typed results they'd get from a normal parameterized
// query - just mapped once, in one place, instead of everywhere the batch
// is used.

interface ItemCount {
  count: number;
}

function itemCountFromRow(row: Row): ItemCount {
  return { count: Number(row.count) };
}

interface ExpensiveItem {
  name: string;
}

function expensiveItemFromRow(row: Row): ExpensiveItem {
  return { name: String(row.name) };
}

interface ServerTime {
  now: string;
}

function serverTimeFromRow(row: Row): ServerTime {
  return { now: String(row.now) };
}

async function main(): Promise<void> {
  const sql = createSql();

  await sql`
    DROP TABLE IF EXISTS poc_items CASCADE;
    CREATE TABLE poc_items (id SERIAL PRIMARY KEY, name TEXT, price INT);
    INSERT INTO poc_items (name, price) VALUES ('widget', 10), ('gadget', 25), ('gizmo', 5);
  `.simple();

  const results = (await sql`
    SELECT count(*) FROM poc_items;
    SELECT name FROM poc_items WHERE price > 10;
    SELECT now();
  `.simple()) as unknown as [Row[], Row[], Row[]];

  const [countRows, expensiveRows, timeRows] = results;
  const counts = countRows.map(itemCountFromRow);
  const expensiveItems = expensiveRows.map(expensiveItemFromRow);
  const serverTimes = timeRows.map(serverTimeFromRow);

  console.log(`item count: ${counts[0]?.count}`);
  for (const item of expensiveItems) {
    console.log(`expensive item: ${item.name}`);
  }
  console.log(`server time: ${serverTimes[0]?.now}`);

  await sql.end();
}

main().catch((err: unknown) => {
  console.error(err);
  process.exitCode = 1;
});
