# batch-fetch-postgres

A tiny POC showing you can fire several unrelated `SELECT`s at Postgres in a
**single network round trip**, instead of paying a round trip per query.

## Why not just use an ORM?

Most Rust ORMs (Diesel, SeaORM, etc.) give you one query = one round trip.
That's fine for a single fetch, but the moment a request needs three or four
unrelated pieces of data, you end up either:

- awaiting them one after another (N round trips, N × latency), or
- reaching for the ORM's own batching/transaction APIs, which usually still
  issue separate messages under the hood, or add a layer of query-building
  machinery just to get there.

This POC skips the ORM entirely and talks to the wire protocol directly via
`tokio-postgres`. No query builder, no macros, no schema DSL — just SQL
strings and one method call.

## How it's solved

The core trick is `simple_query`. Postgres' simple query protocol lets you
send a semicolon-separated batch of statements as **one** message; the server
runs them in order and streams all the results back before a single
`ReadyForQuery`:

```rust
let batch = "SELECT count(*) FROM poc_items;
             SELECT name FROM poc_items WHERE price > 10;
             SELECT now();";

let messages = client.simple_query(batch).await?;
```

That's it — three unrelated queries, one call, one round trip. Compare that
to the "normal" way:

```rust
client.query("SELECT count(*) FROM poc_items", &[]).await?;
client.query("SELECT name FROM poc_items WHERE price > 10", &[]).await?;
client.query("SELECT now()", &[]).await?;
```

which is three separate request/response cycles, each paying network and
scheduling latency on its own.

## Does it actually help?

Yes — even on localhost, where round-trip latency is about as cheap as it
gets, batching came out roughly **4-5x faster** over 200 iterations:

| Approach | Per iteration |
|---|---|
| Batched (1 round trip) | ~250-270µs |
| Separate (3 round trips) | ~990µs-1.4ms |

The gap only grows on a real network — every extra round trip there costs
milliseconds, not microseconds, so batching pays off even more the further
away your database is.

## The catch: maintainability

This is a performance win, not a free lunch:

- No per-statement typed results — you track statement boundaries yourself
  via `CommandComplete` (see `examples/typed_results.rs` for a fix).
- No parameter binding — values go straight into the SQL string, which is a
  SQL injection risk for user input.
- One error fails the whole batch, and the failing statement isn't always
  obvious.
- Harder to review — one string of semicolon-joined SQL vs. separate,
  readable queries.

Best used for hot paths with known, non-parameterized reads — not as a
default way to write queries.

## What about dependent queries?

`simple_query` batches statements as static text sent all at once, so it
can't handle a query that needs a value produced by the one before it — e.g.
insert a record, then use its new id in the next insert. The client never
sees that id in time to splice it into the batch.

`examples/dependent_queries.rs` shows the fix: fold both statements into one
query with a data-modifying CTE. The `INSERT ... RETURNING` feeds the id
straight into the next `INSERT` inside Postgres itself, so it's still a
single round trip — and since it's one real statement, normal parameter
binding (`$1`, `$2`) works too:

```rust
let row = client
    .query_one(
        "WITH new_item AS (
             INSERT INTO poc_items (name, price) VALUES ($1, $2)
             RETURNING id
         )
         INSERT INTO poc_order_events (item_id, event_type)
         SELECT id, 'created' FROM new_item
         RETURNING id, item_id, event_type",
        &[&"thingamajig", &42],
    )
    .await?;
```

Run it with `cargo run --example dependent_queries`.

## What if a query in the batch fails?

`examples/error_handling.rs` shows the other gotcha: a multi-statement
`simple_query` batch runs as one implicit transaction. If a later statement
errors, everything earlier in that same batch is rolled back too - even a
successful insert.

```rust
match client.simple_query(batch).await {
    Ok(_) => println!("batch succeeded"),
    Err(err) => {
        if let Some(db_err) = err.as_db_error() {
            println!("batch failed: {}", db_err.message());
        }
    }
}
```

`tokio_postgres::Error::as_db_error()` gives you the Postgres error message
and `SqlState` (e.g. `UNIQUE_VIOLATION`) so you can branch on what went
wrong. The example proves the rollback by re-querying the table afterward -
the row from the first, individually-valid insert is gone.

Run it with `cargo run --example error_handling`.

## Getting typed results back

`examples/typed_results.rs` addresses the other maintainability complaint
above: `simple_query` only gives you untyped text rows. The fix is to define
a small struct per statement and parse each row into it once, right where
the batch is issued:

```rust
struct ItemCount { count: i64 }

impl ItemCount {
    fn from_row(row: &SimpleQueryRow) -> Self {
        Self { count: row.get(0).unwrap_or("0").parse().unwrap_or(0) }
    }
}
```

Since the statement order in the batch is known, each row just gets routed
to the matching struct as the results stream in — callers get back the same
typed values they'd get from `client.query()`, instead of parsing text
everywhere the batch is used.

Run it with `cargo run --example typed_results`.

## A proper query builder

The examples above are all built on `simple_query`, with the trade-offs
documented above. `src/lib.rs` takes a different approach for when you want
real ergonomics: a `QueryBuilder` you chain SELECTs, INSERTs, UPDATEs and
DELETEs onto in any order, `build()` into a `QueryBatch`, and hand to
`BatchExecutor::execute`. It's built on the extended (parameterized)
protocol instead of `simple_query` text batching, pipelining every
statement onto the connection rather than awaiting them one by one, and it
runs the whole thing inside one real transaction:

```rust
let mut products: Vec<Product> = Vec::new();
let mut rows_deleted: u64 = 0;

let batch = QueryBuilder::new()
    .insert("insert widget", "INSERT INTO poc_products (name, price) VALUES ($1, $2)", &[&"widget", &10])
    .update("bump gadget price", "UPDATE poc_products SET price = price + 5 WHERE name = $1", &[&"gadget"])
    .mutation_capturing("delete cheap products", "DELETE FROM poc_products WHERE price < $1", &[&10], &mut rows_deleted)
    .select("all products", "SELECT id, name, price FROM poc_products ORDER BY id", &[], &mut products)
    .build();

BatchExecutor::execute(&mut client, batch).await?;
// products and rows_deleted are populated here, but only if every
// statement above succeeded.
```

This directly answers the maintainability complaints from earlier:

- **Real parameter binding** — `$1`, `$2`, ... per statement, not text
  interpolation.
- **Typed results, not text parsing** — implement `FromRow` once per struct
  (see `examples/query_builder.rs`'s `Product`) and `select` populates a
  `Vec<T>` for you.
- **Atomic by default** — the whole batch runs in one transaction. If any
  statement fails, everything is rolled back and none of your output
  variables get touched, instead of the "later statements silently
  succeed/fail independently" surprise you'd get from plain pipelining.
- **Errors point at the statement that failed** — each error carries the
  statement's index and label (e.g. `"insert duplicate gadget"`) and is
  logged to stderr as it happens.

Output variables are populated through plain `&mut` references captured in
a closure per statement, not raw pointers - safe, and the borrow checker
guarantees they can't outlive the data they point at.

Run it with `cargo run --example query_builder`.
