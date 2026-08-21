# batch-fetch-postgres

A small POC exploring one question: can you fetch several Postgres queries
in **one network round trip** instead of one round trip per query — and
what does that cost you?

This branch is documentation only. The runnable code lives in per-language
branches:

| Branch | Language | Driver |
|---|---|---|
| [`rust-impl`](../../tree/rust-impl) | Rust | `tokio-postgres` |
| [`node-impl`](../../tree/node-impl) | Node.js / TypeScript | `postgres` |
| [`golang-impl`](../../tree/golang-impl) | Go | `pgx` |

Check out whichever branch matches the language you care about — each has
its own README with runnable examples and the technical details specific to
that driver.

## The core problem

An app that needs three unrelated pieces of data from Postgres usually pays
for three round trips: send a query, wait for the reply, send the next one.
Each round trip costs real latency, and it adds up fast the further away
the database is.

## The core idea

Postgres can run several statements from a single message and hand back one
result per statement — this is a feature of the wire protocol itself, not
of any particular driver, so it holds regardless of language:

```sql
SELECT count(*) FROM items;
SELECT name FROM items WHERE price > 10;
SELECT now();
```

Sent as one batch, that's one round trip for all three queries instead of
three. Every implementation in this repo builds on that same fact.

## What held true in every implementation

- **Batching independent reads** works the same way everywhere, since it's
  Postgres behavior, not driver behavior: one round trip for N unrelated
  statements.
- **Dependent queries** (insert a row, then use its new id) can't be
  batched as separate statements — the client never sees the new id in time
  to use it. The fix is always the same: collapse both statements into one,
  using a CTE with `RETURNING` to pass the value from one part of the query
  to the next, still in a single round trip.
- **A batch is one implicit transaction.** If a multi-statement batch has a
  failing statement, everything earlier in that same batch gets rolled back
  too — a real gotcha worth knowing about before relying on this.
- **Real ergonomics cost something.** Plain multi-statement batching has no
  parameter binding (values go into the SQL string by hand) and no typed
  results (you parse rows yourself). Every implementation ends up building
  a small "batch builder" abstraction to fix that — chain typed,
  parameterized statements, run them in a transaction, get typed results
  and clear per-statement errors back.

## Where this turned out to be a driver choice, not a language limit

Building that batch builder is where things first diverged — not every
driver can keep the "one round trip" property once you add real parameter
binding:

| Language | Driver | Parameterized batch, still one round trip? |
|---|---|---|
| Go | `pgx` | Yes — has a native pipelining API built for exactly this |
| Rust | `tokio-postgres` | Yes — achieved manually, by firing queries concurrently without awaiting each one |
| Node.js | `pg` | No — sends queries strictly one at a time, no pipelining at all |

`pg` is the most widely used Postgres driver for Node, so that looked like
a real language-level gap at first. It wasn't: switching to a different
driver, [`postgres`](https://github.com/porsager/postgres), closed it
completely — its `sql.begin(tx => [...])` pipelines an array of queries
into one round trip and wraps them in a transaction, matching what Go and
Rust could already do. The lesson generalizes: if your batch builder can't
keep the round-trip savings, check whether that's actually the language, or
just the specific driver in front of you.

That switch, and a non-obvious flag it needed to actually pipeline, is
covered in more depth on `node-impl`'s own README.
