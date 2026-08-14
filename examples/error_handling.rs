use tokio_postgres::error::SqlState;
use tokio_postgres::NoTls;

// Gotcha: when a simple_query batch has multiple statements, Postgres wraps
// them in one implicit transaction. If a later statement fails, everything
// earlier in that same batch gets rolled back too - even successful inserts.
// This example triggers that failure on purpose and shows how to read the
// error in Rust, then proves the rollback by re-checking the table.
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
            "DROP TABLE IF EXISTS poc_accounts;
             CREATE TABLE poc_accounts (id SERIAL PRIMARY KEY, email TEXT UNIQUE);",
        )
        .await?;

    // Statement 1 succeeds, statement 2 violates the UNIQUE constraint,
    // statement 3 never runs at all.
    let batch = "INSERT INTO poc_accounts (email) VALUES ('a@example.com');
                 INSERT INTO poc_accounts (email) VALUES ('a@example.com');
                 SELECT count(*) FROM poc_accounts;";

    match client.simple_query(batch).await {
        Ok(_) => println!("batch succeeded (unexpected)"),
        Err(err) => {
            if let Some(db_err) = err.as_db_error() {
                println!("batch failed: {}", db_err.message());
                if *db_err.code() == SqlState::UNIQUE_VIOLATION {
                    println!("-> it was a unique violation, as expected");
                }
            } else {
                println!("batch failed: {err}");
            }
        }
    }

    // The implicit transaction rolled back statement 1 as well, so the table
    // should still be empty even though the first insert looked fine on its own.
    let row = client
        .query_one("SELECT count(*) FROM poc_accounts", &[])
        .await?;
    let count: i64 = row.get(0);
    println!("rows in poc_accounts after the failed batch: {count}");

    Ok(())
}
