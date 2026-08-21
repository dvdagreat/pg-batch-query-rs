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
                 ('dave', 'dave@internal.test');",
        )
        .await?;

    // The trick: bundle unrelated SELECTs into one string and fire them through
    // simple_query. Postgres' simple query protocol treats the whole string as a
    // single "Query" message, so every statement travels in one round trip -
    // 15 of them here, to make the round-trip savings hard to miss.
    let batch = "SELECT count(*) FROM poc_items;
                 SELECT name FROM poc_items WHERE price > 10;
                 SELECT now();
                 SELECT avg(price) FROM poc_items;
                 SELECT max(price) FROM poc_items;
                 SELECT min(price) FROM poc_items;
                 SELECT sum(price) FROM poc_items;
                 SELECT count(DISTINCT category_id) FROM poc_items;
                 SELECT count(*) FROM poc_categories;
                 SELECT name FROM poc_categories ORDER BY name;
                 SELECT count(*) FROM poc_customers;
                 SELECT name FROM poc_customers WHERE email LIKE '%@example.com';
                 SELECT i.name FROM poc_items i JOIN poc_categories c ON i.category_id = c.id WHERE c.name = 'electronics';
                 SELECT version();
                 SELECT current_database();";

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

    // Now let's put a number on it: batched vs. the same 15 queries sent one
    // at a time, each awaited before the next fires.
    let separate_statements = [
        "SELECT count(*) FROM poc_items",
        "SELECT name FROM poc_items WHERE price > 10",
        "SELECT now()",
        "SELECT avg(price) FROM poc_items",
        "SELECT max(price) FROM poc_items",
        "SELECT min(price) FROM poc_items",
        "SELECT sum(price) FROM poc_items",
        "SELECT count(DISTINCT category_id) FROM poc_items",
        "SELECT count(*) FROM poc_categories",
        "SELECT name FROM poc_categories ORDER BY name",
        "SELECT count(*) FROM poc_customers",
        "SELECT name FROM poc_customers WHERE email LIKE '%@example.com'",
        "SELECT i.name FROM poc_items i JOIN poc_categories c ON i.category_id = c.id WHERE c.name = 'electronics'",
        "SELECT version()",
        "SELECT current_database()",
    ];

    let iterations = 200;

    let start = Instant::now();
    for _ in 0..iterations {
        client.simple_query(batch).await?;
    }
    let batched_elapsed = start.elapsed();

    let start = Instant::now();
    for _ in 0..iterations {
        for stmt in &separate_statements {
            client.query(*stmt, &[]).await?;
        }
    }
    let separate_elapsed = start.elapsed();

    println!("\n--- timing over {iterations} iterations, {} statements each ---", separate_statements.len());
    println!(
        "batched (1 round trip):     {batched_elapsed:?}  ({:?}/iter)",
        batched_elapsed / iterations
    );
    println!(
        "separate ({} round trips):  {separate_elapsed:?}  ({:?}/iter)",
        separate_statements.len(),
        separate_elapsed / iterations
    );

    Ok(())
}
