use std::time::Instant;
use tokio_postgres::{NoTls, SimpleQueryMessage};

#[tokio::main]
async fn main() -> Result<(), tokio_postgres::Error> {
    let (client, connection) = tokio_postgres::connect(
        "host=localhost user=postgres password=password dbname=postgres",
        NoTls,
    )
    .await?;

    tokio::spawn(async move {
        if let Err(e) = connection.await {
            eprintln!("connection error: {e}");
        }
    });

    client
        .batch_execute(
            "DROP TABLE IF EXISTS poc_items CASCADE;
             CREATE TABLE poc_items (id SERIAL PRIMARY KEY, name TEXT, price INT);
             INSERT INTO poc_items (name, price) VALUES ('widget', 10), ('gadget', 25), ('gizmo', 5);",
        )
        .await?;

    // The trick: bundle unrelated SELECTs into one string and fire them through
    // simple_query. Postgres' simple query protocol treats the whole string as a
    // single "Query" message, so all three statements travel in one round trip.
    let batch = "SELECT count(*) FROM poc_items;
                 SELECT name FROM poc_items WHERE price > 10;
                 SELECT now();";

    let messages = client.simple_query(batch).await?;

    let mut statement_idx = 0;
    for msg in messages {
        match msg {
            SimpleQueryMessage::Row(row) => {
                let cols: Vec<&str> = (0..row.len()).map(|i| row.get(i).unwrap_or("NULL")).collect();
                println!("statement {statement_idx} -> {}", cols.join(" | "));
            }
            SimpleQueryMessage::CommandComplete(_) => statement_idx += 1,
            _ => {}
        }
    }

    // Now let's put a number on it: batched vs. the same 3 queries sent one
    // at a time, each awaited before the next fires.
    let iterations = 200;

    let start = Instant::now();
    for _ in 0..iterations {
        client.simple_query(batch).await?;
    }
    let batched_elapsed = start.elapsed();

    let start = Instant::now();
    for _ in 0..iterations {
        client.query("SELECT count(*) FROM poc_items", &[]).await?;
        client
            .query("SELECT name FROM poc_items WHERE price > 10", &[])
            .await?;
        client.query("SELECT now()", &[]).await?;
    }
    let separate_elapsed = start.elapsed();

    println!("\n--- timing over {iterations} iterations ---");
    println!(
        "batched (1 round trip):    {batched_elapsed:?}  ({:?}/iter)",
        batched_elapsed / iterations
    );
    println!(
        "separate (3 round trips):  {separate_elapsed:?}  ({:?}/iter)",
        separate_elapsed / iterations
    );

    Ok(())
}
