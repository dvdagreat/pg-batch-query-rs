import { createSql } from '../src/db.js';

// The multi-statement batching trick from src/index.ts doesn't work here:
// the whole batch is sent as one block of text upfront, so the client never
// sees the new row's id in time to splice it into the next statement. To
// keep it to one round trip, fold both statements into a single
// parameterized query using a data-modifying CTE - the INSERT's RETURNING
// feeds straight into the next INSERT via SELECT, all inside Postgres.
async function main(): Promise<void> {
  const sql = createSql();

  await sql`
    DROP TABLE IF EXISTS poc_order_events;
    DROP TABLE IF EXISTS poc_items;
    CREATE TABLE poc_items (id SERIAL PRIMARY KEY, name TEXT, price INT);
    CREATE TABLE poc_order_events (
        id SERIAL PRIMARY KEY,
        item_id INT REFERENCES poc_items(id),
        event_type TEXT
    );
  `.simple();

  // Insert into poc_items, then use its new id to insert into
  // poc_order_events - all in one statement, one round trip.
  const name = 'thingamajig';
  const price = 42;
  const [row] = await sql<{ id: number; item_id: number; event_type: string }[]>`
    WITH new_item AS (
        INSERT INTO poc_items (name, price) VALUES (${name}, ${price})
        RETURNING id
    )
    INSERT INTO poc_order_events (item_id, event_type)
    SELECT id, 'created' FROM new_item
    RETURNING id, item_id, event_type
  `;

  console.log(`event ${row?.id} -> item ${row?.item_id} (${row?.event_type})`);

  await sql.end();
}

main().catch((err: unknown) => {
  console.error(err);
  process.exitCode = 1;
});
