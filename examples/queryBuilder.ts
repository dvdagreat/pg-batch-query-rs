import type { QueryResultRow } from 'pg';
import { createClient } from '../src/db.js';
import { BatchExecutor, createRef, QueryBuilder } from '../src/queryBuilder.js';

interface Product {
  id: number;
  name: string;
  price: number;
}

function productFromRow(row: QueryResultRow): Product {
  return { id: row.id, name: row.name, price: row.price };
}

async function main(): Promise<void> {
  const client = createClient();
  await client.connect();

  await client.query(
    `DROP TABLE IF EXISTS poc_products;
     CREATE TABLE poc_products (id SERIAL PRIMARY KEY, name TEXT UNIQUE, price INT);`,
  );

  // A batch that mixes every statement kind, in whatever order the problem
  // calls for, chained onto one builder.
  const allProducts: Product[] = [];
  const productCount: { count: number }[] = [];
  const rowsDeleted = createRef(0);

  const batch = new QueryBuilder()
    .insert('insert widget', 'INSERT INTO poc_products (name, price) VALUES ($1, $2)', ['widget', 10])
    .insert('insert gadget', 'INSERT INTO poc_products (name, price) VALUES ($1, $2)', ['gadget', 25])
    .insert('insert gizmo', 'INSERT INTO poc_products (name, price) VALUES ($1, $2)', ['gizmo', 5])
    .update('bump gadget price', 'UPDATE poc_products SET price = price + 5 WHERE name = $1', ['gadget'])
    .mutationCapturing('delete cheap products', 'DELETE FROM poc_products WHERE price < $1', [10], rowsDeleted)
    .select(
      'all products',
      'SELECT id, name, price FROM poc_products ORDER BY id',
      [],
      productFromRow,
      allProducts,
    )
    .select('product count', 'SELECT count(*) FROM poc_products', [], (row) => ({ count: Number(row.count) }), productCount)
    .build();

  await BatchExecutor.execute(client, batch);

  console.log(`deleted ${rowsDeleted.value} cheap product(s)`);
  for (const p of allProducts) {
    console.log(`product ${p.id}: ${p.name} ($${p.price})`);
  }
  console.log(`count query says: ${productCount[0]?.count}`);

  // Now prove the transaction actually protects you: this batch inserts a
  // valid row, then a duplicate that violates the UNIQUE constraint.
  const failingBatch = new QueryBuilder()
    .insert('insert thingamajig', 'INSERT INTO poc_products (name, price) VALUES ($1, $2)', ['thingamajig', 42])
    .insert('insert duplicate gadget', 'INSERT INTO poc_products (name, price) VALUES ($1, $2)', ['gadget', 99])
    .build();

  try {
    await BatchExecutor.execute(client, failingBatch);
    console.log('failing batch succeeded (unexpected)');
  } catch (err) {
    console.log(`failing batch was rejected as expected: ${err instanceof Error ? err.message : String(err)}`);
  }

  const result = await client.query('SELECT count(*) FROM poc_products');
  console.log(`count after the failed batch: ${result.rows[0].count} (thingamajig was rolled back too)`);

  await client.end();
}

main().catch((err: unknown) => {
  console.error(err);
  process.exitCode = 1;
});
