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
  via `CommandComplete`.
- No parameter binding — values go straight into the SQL string, which is a
  SQL injection risk for user input.
- One error fails the whole batch, and the failing statement isn't always
  obvious.
- Harder to review — one string of semicolon-joined SQL vs. separate,
  readable queries.

Best used for hot paths with known, non-parameterized reads — not as a
default way to write queries.
