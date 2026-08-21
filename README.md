# batch-fetch-postgres

A tiny POC showing you can fire several unrelated `SELECT`s at Postgres in a
**single network round trip**, instead of paying a round trip per query.

## Getting started

```bash
npm install
npm start
```

Assumes a local Postgres reachable at `host=localhost`, user `postgres`,
password `password`, database `postgres` (see `src/db.ts`). Uses
[`postgres`](https://github.com/porsager/postgres) (Postgres.js) as the
driver, no ORM.

## Why not just use an ORM?

Most Node ORMs and query builders (Prisma, Drizzle, TypeORM, Sequelize) give
you one query = one round trip. That's fine for a single fetch, but the
moment a request needs three or four unrelated pieces of data, you end up
either:

- awaiting them one after another (N round trips, N × latency), or
- reaching for the ORM's own batching/transaction APIs, which usually still
  issue separate messages under the hood, or add a layer of query-building
  machinery just to get there.

This POC skips the ORM entirely and talks to Postgres directly. No query
builder, no schema DSL — just SQL and one call.

## Why `postgres` and not `pg`?

`pg` is the most widely used Postgres driver for Node, but its `Client`
processes queries strictly one at a time: even firing off several
`.query()` calls without awaiting doesn't help, since it queues them and
won't send the next one until the previous one's response has fully come
back. There's no way to pipeline a set of queries into a single round trip.

That's not a problem for the plain multi-statement batching this POC leans
on below (that's a Postgres protocol feature, and works the same with any
driver) — but it rules out ever building a proper query-builder abstraction
that keeps the round-trip savings once you add real parameter binding,
typed results, and transactions on top.

`postgres` ([Postgres.js](https://github.com/porsager/postgres)) doesn't
have that limitation: `sql.begin(tx => [...])` — returning an array of
queued queries from a transaction callback — pipelines them onto the wire
in one round trip and wraps them in a real transaction. That's the
mechanism "A proper query builder" further down is built on, which is why
`postgres` is the driver used everywhere in this repo, not just there.

## How it's solved

The core trick: send several `SELECT`s as one semicolon-separated string
with no parameter placeholders, using `.simple()` to force Postgres' simple
query protocol. That treats the whole string as a single message, runs each
statement in order, and resolves with one row array per statement:

```ts
const batch = sql`
  SELECT count(*) FROM poc_items;
  SELECT name FROM poc_items WHERE price > 10;
  SELECT now();
`.simple();

const [counts, expensive, times] = await batch;
```

That's it — three unrelated queries, one call, one round trip. Compare that
to the "normal" way:

```ts
await sql`SELECT count(*) FROM poc_items`;
await sql`SELECT name FROM poc_items WHERE price > 10`;
await sql`SELECT now()`;
```

which is three separate request/response cycles, each paying network and
scheduling latency on its own.

## Does it actually help?

Yes. `src/index.ts` runs 5,080 statements - the curated variety shown above
(aggregates, filters, joins, `version()`, `current_database()`, ...) plus a
long tail of cheap, uniform price-threshold filters, across `poc_items`,
`poc_categories` and `poc_customers`. Even on localhost, where round-trip
latency is about as cheap as it gets, batching came out roughly **6-7x
faster** over 15 iterations:

| Approach | Per iteration |
|---|---|
| Batched (1 round trip) | ~125-135ms |
| Separate (5,080 round trips) | ~790-825ms |

Worth being upfront about: this ceiling is lower than the other two
implementations in this repo hit with the same technique (Rust gets past
10x). That's not a tuning miss - it held steady across statement counts from
500 to 20,000, several `prepare` option combinations, and both `.unsafe()`
and parameterized tagged-template calls for the "separate" baseline. Adding
more statements past a few thousand actually made it *worse* (constructing
and parsing one enormous batch has its own cost that starts to dominate).
`postgres`'s per-call overhead for a one-off query is apparently just
proportionally smaller relative to its own per-statement batch-processing
cost than what Rust's driver has - so the two curves converge sooner. The
underlying idea still holds: on a real network, where a round trip costs
milliseconds instead of microseconds, that fixed per-call cost matters far
more than it does here, so the gap only grows the further away your
database is.

## The catch: maintainability

This is a performance win, not a free lunch:

- No per-statement typed results by default — `.simple()` hands back a row
  array per statement, with no structure of your own.
- No parameter binding — this only works for plain SQL strings with no
  placeholders, so anything dynamic has to be interpolated by hand, which is
  a SQL injection risk for user input.
- One error fails the whole batch, and the failing statement isn't always
  obvious.
- Harder to review — one string of semicolon-joined SQL vs. separate,
  readable queries.

Best used for hot paths with known, non-parameterized reads — not as a
default way to write queries.

## What about dependent queries?

Multi-statement batching sends the whole string as static text upfront, so
it can't handle a query that needs a value produced by the one before it —
e.g. insert a record, then use its new id in the next insert. The client
never sees that id in time to splice it into the batch.

`examples/dependentQueries.ts` shows the fix: fold both statements into one
query with a data-modifying CTE. The `INSERT ... RETURNING` feeds the id
straight into the next `INSERT` inside Postgres itself, so it's still a
single round trip — and since it's one real statement, normal parameter
binding works too:

```ts
const [row] = await sql<{ id: number; item_id: number; event_type: string }[]>`
  WITH new_item AS (
      INSERT INTO poc_items (name, price) VALUES (${name}, ${price})
      RETURNING id
  )
  INSERT INTO poc_order_events (item_id, event_type)
  SELECT id, 'created' FROM new_item
  RETURNING id, item_id, event_type
`;
```

Run it with `npm run example:dependent-queries`.

## What if a query in the batch fails?

`examples/errorHandling.ts` shows the other gotcha: a multi-statement batch
runs as one implicit transaction. If a later statement errors, everything
earlier in that same batch is rolled back too - even a successful insert.

```ts
try {
  await sql`...; ...;`.simple();
} catch (err) {
  if (err instanceof postgres.PostgresError) {
    console.log(`batch failed: ${err.message}`);
  }
}
```

`postgres.PostgresError` carries the message and a `code` (Postgres's
`SQLSTATE`, e.g. `23505` for a unique violation) so you can branch on what
went wrong. The example proves the rollback by re-querying the table
afterward - the row from the first, individually-valid insert is gone.

Run it with `npm run example:error-handling`.

## Getting typed results back

`examples/typedResults.ts` addresses the other maintainability complaint
above: a multi-statement batch only gives you an array of generic row
objects per statement. The fix is to define an interface and a small mapper
function per statement, right where the batch is issued:

```ts
interface ItemCount {
  count: number;
}

function itemCountFromRow(row: Row): ItemCount {
  return { count: Number(row.count) };
}
```

Since the statement order in the batch is known, each result just gets
mapped through the function that matches it - callers get back the same
typed values they'd get from a normal query, instead of reading untyped
fields everywhere the batch is used.

Run it with `npm run example:typed-results`.

## A proper query builder

The examples above are all built on multi-statement text batching, with the
trade-offs documented above. `src/queryBuilder.ts` takes a different
approach for when you want real ergonomics: a `QueryBuilder` you chain
SELECTs, INSERTs, UPDATEs and DELETEs onto in any order, `build()` into a
`QueryBatch`, and hand to `BatchExecutor.execute`:

```ts
const products: Product[] = [];
const rowsDeleted = createRef(0);

const batch = new QueryBuilder()
  .insert('insert widget', 'INSERT INTO poc_products (name, price) VALUES ($1, $2)', ['widget', 10])
  .update('bump gadget price', 'UPDATE poc_products SET price = price + 5 WHERE name = $1', ['gadget'])
  .mutationCapturing('delete cheap products', 'DELETE FROM poc_products WHERE price < $1', [10], rowsDeleted)
  .select('all products', 'SELECT id, name, price FROM poc_products ORDER BY id', [], productFromRow, products)
  .build();

await BatchExecutor.execute(sql, batch);
// products and rowsDeleted are populated here, but only if every
// statement above succeeded.
```

Under the hood it runs every statement through `sql.begin(tx => [...])`:
returning an array of queued queries from a transaction callback pipelines
them onto the wire in one round trip *and* wraps them in a real transaction.
Since our SQL and parameters are built dynamically rather than written as
literal tagged templates, each statement goes through `tx.unsafe(sql,
params, { prepare: true })` - the `prepare: true` matters here, since
`unsafe()` defaults to `prepare: false`, which turned out to quietly opt
each statement out of pipelining (confirmed by timing it: with the default,
a 3-statement batch was *slower* than sending them one at a time; forcing
`prepare: true` made it faster than both).

This directly answers the maintainability complaints from earlier - and,
unlike the plain multi-statement batching above, it keeps the single round
trip *and* gets everything else:

- **Real parameter binding** — `$1`, `$2`, ... per statement, not text
  interpolation.
- **Still one round trip** — pipelined via `sql.begin`, the same mechanism
  the plain batching demos use, just with real parameters this time.
- **Typed results, not generic rows** — write a `RowMapper<T>` once per
  shape (see `examples/queryBuilder.ts`'s `productFromRow`) and `select`
  populates a `T[]` for you.
- **Atomic by default** — the whole batch runs in one transaction. If any
  statement fails, everything is rolled back and none of your output
  variables get touched.
- **Errors point at the statement that failed** — each error carries the
  statement's index and label (e.g. `"insert duplicate gadget"`) and is
  logged to stderr as it happens.

Select output uses plain array-reference semantics — `out` is the same
array the caller passed in, mutated in place, so no special plumbing is
needed there. Scalar out-parameters (like `rowsDeleted` above) use a small
`Ref<T>` box instead, since JS primitives are copied by value and a plain
`let count = 0` can't be written back to from inside the builder.

Run it with `npm run example:query-builder`.
