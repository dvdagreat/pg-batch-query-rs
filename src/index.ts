import type { Row } from 'postgres';
import { createSql } from './db.js';

async function main(): Promise<void> {
  const sql = createSql();

  await sql`
    DROP TABLE IF EXISTS poc_items CASCADE;
    DROP TABLE IF EXISTS poc_categories CASCADE;
    DROP TABLE IF EXISTS poc_customers CASCADE;

    CREATE TABLE poc_categories (id SERIAL PRIMARY KEY, name TEXT);
    CREATE TABLE poc_items (
        id SERIAL PRIMARY KEY,
        name TEXT,
        price INT,
        category_id INT REFERENCES poc_categories(id)
    );
    CREATE TABLE poc_customers (id SERIAL PRIMARY KEY, name TEXT, email TEXT);

    INSERT INTO poc_categories (name) VALUES ('tools'), ('electronics'), ('misc');

    INSERT INTO poc_items (name, price, category_id) VALUES
        ('widget', 10, 1),
        ('gadget', 25, 2),
        ('gizmo', 5, 3),
        ('doohickey', 15, 1),
        ('thingamajig', 42, 2),
        ('contraption', 8, 3);

    INSERT INTO poc_customers (name, email) VALUES
        ('alice', 'alice@example.com'),
        ('bob', 'bob@example.com'),
        ('carol', 'carol@example.com'),
        ('dave', 'dave@internal.test');
  `.simple();

  // 15 unrelated statements, sent as one semicolon-separated string with no
  // parameter placeholders. .simple() puts the whole thing in a single
  // Postgres simple-query message - one round trip for all fifteen - and
  // resolves with one row array per statement, in order.
  const statements = [
    'SELECT count(*) FROM poc_items',
    'SELECT name FROM poc_items WHERE price > 10',
    'SELECT now()',
    'SELECT avg(price) FROM poc_items',
    'SELECT max(price) FROM poc_items',
    'SELECT min(price) FROM poc_items',
    'SELECT sum(price) FROM poc_items',
    'SELECT count(DISTINCT category_id) FROM poc_items',
    'SELECT count(*) FROM poc_categories',
    'SELECT name FROM poc_categories ORDER BY name',
    'SELECT count(*) FROM poc_customers',
    "SELECT name FROM poc_customers WHERE email LIKE '%@example.com'",
    "SELECT i.name FROM poc_items i JOIN poc_categories c ON i.category_id = c.id WHERE c.name = 'electronics'",
    'SELECT version()',
    'SELECT current_database()',
  ];

  const runBatch = () => sql.unsafe(statements.join(';\n')).simple();

  const results = (await runBatch()) as unknown as Row[][];
  results.forEach((rows, statementIndex) => {
    for (const row of rows) {
      console.log(`statement ${statementIndex} ->`, Object.values(row).join(' | '));
    }
  });

  // Now put a number on it: batched vs. the same 15 queries sent one at a
  // time, each awaited before the next fires.
  const iterations = 200;

  const batchedStart = performance.now();
  for (let i = 0; i < iterations; i++) {
    await runBatch();
  }
  const batchedMs = performance.now() - batchedStart;

  const separateStart = performance.now();
  for (let i = 0; i < iterations; i++) {
    for (const statement of statements) {
      await sql.unsafe(statement);
    }
  }
  const separateMs = performance.now() - separateStart;

  console.log(`\n--- timing over ${iterations} iterations, ${statements.length} statements each ---`);
  console.log(`batched (1 round trip):     ${batchedMs.toFixed(2)}ms  (${(batchedMs / iterations).toFixed(3)}ms/iter)`);
  console.log(`separate (${statements.length} round trips):  ${separateMs.toFixed(2)}ms  (${(separateMs / iterations).toFixed(3)}ms/iter)`);

  await sql.end();
}

main().catch((err: unknown) => {
  console.error(err);
  process.exitCode = 1;
});
