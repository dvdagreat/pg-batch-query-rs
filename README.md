# batch-fetch-postgres

A tiny POC showing you can fire several unrelated `SELECT`s at Postgres in a
**single network round trip**, instead of paying a round trip per query.

## Getting started

```bash
go run .
```

Assumes a local Postgres reachable at `host=localhost`, user `postgres`,
password `password`, database `postgres` (see `internal/db/db.go`). Uses
[pgx](https://github.com/jackc/pgx) as the driver, no ORM.

## Why not just use an ORM?

Most Go ORMs and query builders (GORM, ent, sqlx-with-a-builder) give you
one query = one round trip. That's fine for a single fetch, but the moment a
request needs three or four unrelated pieces of data, you end up either:

- awaiting them one after another (N round trips, N × latency), or
- reaching for the ORM's own batching/transaction APIs, which usually still
  issue separate messages under the hood, or add a layer of query-building
  machinery just to get there.

This POC skips the ORM entirely and talks to Postgres directly via `pgx`.

## How it's solved

Send several `SELECT`s as one semicolon-separated string with no parameter
placeholders. Postgres' simple query protocol treats that as a single
message and returns one result set per statement, read off the same
response with `pgconn`'s `MultiResultReader`:

```go
batch := `SELECT count(*) FROM poc_items;
          SELECT name FROM poc_items WHERE price > 10;
          SELECT now();`

mrr := conn.PgConn().Exec(ctx, batch)
for mrr.NextResult() {
    result := mrr.ResultReader()
    for result.NextRow() {
        // one statement's rows at a time, in order
    }
    result.Close()
}
mrr.Close()
```

That's it — three unrelated queries, one call, one round trip. Compare that
to the "normal" way:

```go
conn.Exec(ctx, "SELECT count(*) FROM poc_items")
conn.Exec(ctx, "SELECT name FROM poc_items WHERE price > 10")
conn.Exec(ctx, "SELECT now()")
```

which is three separate request/response cycles, each paying network and
scheduling latency on its own.

## Does it actually help?

Yes. `main.go` runs 5,080 statements - the curated variety shown above
(aggregates, filters, joins, `version()`, `current_database()`, ...) plus a
long tail of cheap, uniform price-threshold filters, across `poc_items`,
`poc_categories` and `poc_customers`. Even on localhost, where round-trip
latency is about as cheap as it gets, batching came out roughly **5x
faster** over 15 iterations:

| Approach | Per iteration |
|---|---|
| Batched (1 round trip) | ~123-125ms |
| Separate (5,080 round trips) | ~629-646ms |

Worth being upfront about: this ceiling is lower than `rust-impl` hits with
the same technique (past 10x there). That's not a tuning miss on this
branch - it held steady across statement counts from 580 all the way to
20,080, the ratio barely moving once past a couple thousand statements.
`pgconn`'s per-statement cost of reading a `MultiResultReader` result (byte
slice allocation per value, per row) is apparently proportionally larger
relative to its own round-trip overhead than what Rust's client has, so the
batched and separate curves converge sooner here. The underlying idea still
holds: on a real network, where a round trip costs milliseconds instead of
microseconds, that fixed per-call cost matters far more than it does here,
so the gap only grows the further away your database is.

## The catch: maintainability

This is a performance win, not a free lunch:

- No per-statement typed results by default — you read each result set off
  `MultiResultReader` as raw byte values (see `examples/typedresults` for a
  fix).
- No parameter binding — this only works for plain SQL strings with no
  `$1`-style values, so anything dynamic has to be interpolated by hand,
  which is a SQL injection risk for user input.
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

`examples/dependentqueries` shows the fix: fold both statements into one
query with a data-modifying CTE. The `INSERT ... RETURNING` feeds the id
straight into the next `INSERT` inside Postgres itself, so it's still a
single round trip — and since it's one real statement, normal parameter
binding (`$1`, `$2`) works too:

```go
var eventID, itemID int32
var eventType string
err := conn.QueryRow(ctx,
    `WITH new_item AS (
         INSERT INTO poc_items (name, price) VALUES ($1, $2)
         RETURNING id
     )
     INSERT INTO poc_order_events (item_id, event_type)
     SELECT id, 'created' FROM new_item
     RETURNING id, item_id, event_type`,
    "thingamajig", 42,
).Scan(&eventID, &itemID, &eventType)
```

Run it with `go run ./examples/dependentqueries`.

## What if a query in the batch fails?

`examples/errorhandling` shows the other gotcha: a multi-statement batch
runs as one implicit transaction. If a later statement errors, everything
earlier in that same batch is rolled back too - even a successful insert.

```go
_, err := conn.PgConn().Exec(ctx, batch).ReadAll()
if err != nil {
    var pgErr *pgconn.PgError
    if errors.As(err, &pgErr) {
        fmt.Println("batch failed:", pgErr.Message)
    }
}
```

`pgconn.PgError` carries the Postgres error message and a `Code` (Postgres's
`SQLSTATE`, e.g. `23505` for a unique violation) so you can branch on what
went wrong. The example proves the rollback by re-querying the table
afterward - the row from the first, individually-valid insert is gone.

Run it with `go run ./examples/errorhandling`.

## Getting typed results back

`examples/typedresults` addresses the other maintainability complaint
above: a multi-statement batch only gives you raw byte values off
`MultiResultReader`. The fix is to define a struct and parse each result set
into it once, right where the batch is issued:

```go
type ItemCount struct {
    Count int64
}
```

Since the statement order in the batch is known, each result set just gets
routed to the matching struct as it streams in — callers get back the same
typed values they'd get from a normal query, instead of parsing raw bytes
everywhere the batch is used.

Run it with `go run ./examples/typedresults`.

## A proper query builder

The examples above are all built on multi-statement text batching, with the
trade-offs documented above. `internal/querybatch` takes a different
approach for when you want real ergonomics: a `Builder` you chain SELECTs,
INSERTs, UPDATEs and DELETEs onto in any order, `Build()` into a `Batch`,
and hand to `querybatch.Execute`. It's built on `pgx.Batch`, which pipelines
every parameterized statement onto the connection in one real round trip via
`SendBatch`, and it runs the whole thing inside one transaction:

```go
var products []Product
var rowsDeleted int64

b := querybatch.NewBuilder()
b.Insert("insert widget", "INSERT INTO poc_products (name, price) VALUES ($1, $2)", "widget", 10)
b.Update("bump gadget price", "UPDATE poc_products SET price = price + 5 WHERE name = $1", "gadget")
b.MutationCapturing("delete cheap products", "DELETE FROM poc_products WHERE price < $1", &rowsDeleted, 10)
querybatch.Select(b, "all products", "SELECT id, name, price FROM poc_products ORDER BY id", nil,
    pgx.RowToStructByPos[Product], &products)

batch, err := b.Build()
if err != nil {
    log.Fatal(err)
}
err = querybatch.Execute(ctx, conn, batch)
// products and rowsDeleted are populated here, but only if every
// statement above succeeded.
```

This directly answers the maintainability complaints from earlier - and,
unlike the plain multi-statement batching above, it keeps the single round
trip *and* gets everything else:

- **Real parameter binding** — `$1`, `$2`, ... per statement, not text
  interpolation.
- **Still one round trip** — `pgx.Batch`/`SendBatch` pipelines every queued
  statement's Parse/Bind/Execute onto the wire together, the same mechanism
  the plain batching demos use, just with real parameters this time.
- **Typed results, not raw bytes** — pass any `pgx.RowToFunc[T]` (pgx ships
  `RowTo[T]`, `RowToStructByPos[T]`, `RowToStructByName[T]`, or write your
  own) and `Select` populates a `[]T` for you.
- **Atomic by default** — the whole batch runs in one transaction. If any
  statement fails, everything is rolled back and none of your output
  variables get touched.
- **Errors point at the statement that failed** — each error carries the
  statement's index and label (e.g. `"insert duplicate gadget"`) and is
  logged as it happens.

One Go-specific wrinkle: `Select` is a free function, not a `Builder`
method. Go doesn't allow a method to introduce its own type parameter, so a
generic "give me a typed result back" step has to live outside the method
set - call it as `querybatch.Select(b, ...)` rather than `b.Select(...)`.
Output binding otherwise stays plain Go: a `*[]T` pointer for `Select`
results, and a `*int64` pointer for `MutationCapturing`'s row count - the
same pointer-to-a-caller's-variable pattern Go already uses everywhere else
for out-parameters.

Run it with `go run ./examples/querybuilder`.
