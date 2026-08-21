import type { QueryResult, QueryResultRow } from 'pg';
import { createClient } from '../src/db.js';

// A multi-statement batch hands back an array of untyped result objects.
// This example maps each statement's rows into a type of our own, so
// callers get the same typed results they'd get from a normal parameterized
// query - just mapped once, in one place, instead of everywhere the batch
// is used.

interface ItemCount {
  count: number;
}

function itemCountFromRow(row: QueryResultRow): ItemCount {
  return { count: Number(row.count) };
}

interface ExpensiveItem {
  name: string;
}

function expensiveItemFromRow(row: QueryResultRow): ExpensiveItem {
  return { name: String(row.name) };
}

interface ServerTime {
  now: string;
}

function serverTimeFromRow(row: QueryResultRow): ServerTime {
  return { now: String(row.now) };
}

async function main(): Promise<void> {
  const client = createClient();
  await client.connect();

  await client.query(
    `DROP TABLE IF EXISTS poc_items CASCADE;
     CREATE TABLE poc_items (id SERIAL PRIMARY KEY, name TEXT, price INT);
     INSERT INTO poc_items (name, price) VALUES ('widget', 10), ('gadget', 25), ('gizmo', 5);`,
  );

  const batch = `SELECT count(*) FROM poc_items;
                 SELECT name FROM poc_items WHERE price > 10;
                 SELECT now();`;

  const [countResult, expensiveResult, timeResult] = (await client.query(
    batch,
  )) as unknown as [QueryResult, QueryResult, QueryResult];

  const counts = countResult.rows.map(itemCountFromRow);
  const expensiveItems = expensiveResult.rows.map(expensiveItemFromRow);
  const serverTimes = timeResult.rows.map(serverTimeFromRow);

  console.log(`item count: ${counts[0]?.count}`);
  for (const item of expensiveItems) {
    console.log(`expensive item: ${item.name}`);
  }
  console.log(`server time: ${serverTimes[0]?.now}`);

  await client.end();
}

main().catch((err: unknown) => {
  console.error(err);
  process.exitCode = 1;
});
