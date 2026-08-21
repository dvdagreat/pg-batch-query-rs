use batch_fetch_poc::{BatchExecutor, FromRow, QueryBuilder};
use tokio_postgres::{NoTls, Row};

struct Product {
    id: i32,
    name: String,
    price: i32,
}

impl FromRow for Product {
    fn from_row(row: &Row) -> Result<Self, tokio_postgres::Error> {
        Ok(Self {
            id: row.try_get("id")?,
            name: row.try_get("name")?,
            price: row.try_get("price")?,
        })
    }
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let (mut client, connection) = tokio_postgres::connect(
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
            "DROP TABLE IF EXISTS poc_products;
             CREATE TABLE poc_products (id SERIAL PRIMARY KEY, name TEXT UNIQUE, price INT);",
        )
        .await?;

    // A batch that mixes every statement kind, in whatever order the
    // problem calls for, chained onto one builder.
    let mut all_products: Vec<Product> = Vec::new();
    let mut product_count: Vec<i64> = Vec::new();
    let mut rows_deleted: u64 = 0;

    let batch = QueryBuilder::new()
        .insert(
            "insert widget",
            "INSERT INTO poc_products (name, price) VALUES ($1, $2)",
            &[&"widget", &10],
        )
        .insert(
            "insert gadget",
            "INSERT INTO poc_products (name, price) VALUES ($1, $2)",
            &[&"gadget", &25],
        )
        .insert(
            "insert gizmo",
            "INSERT INTO poc_products (name, price) VALUES ($1, $2)",
            &[&"gizmo", &5],
        )
        .update(
            "bump gadget price",
            "UPDATE poc_products SET price = price + 5 WHERE name = $1",
            &[&"gadget"],
        )
        .mutation_capturing(
            "delete cheap products",
            "DELETE FROM poc_products WHERE price < $1",
            &[&10],
            &mut rows_deleted,
        )
        .select(
            "all products",
            "SELECT id, name, price FROM poc_products ORDER BY id",
            &[],
            &mut all_products,
        )
        .select("product count", "SELECT count(*) FROM poc_products", &[], &mut product_count)
        .build();

    BatchExecutor::execute(&mut client, batch).await?;

    println!("deleted {rows_deleted} cheap product(s)");
    for p in &all_products {
        println!("product {}: {} (${})", p.id, p.name, p.price);
    }
    println!("count query says: {}", product_count[0]);

    // Now prove the transaction actually protects you: this batch inserts
    // a valid row, then a duplicate that violates the UNIQUE constraint.
    let failing_batch = QueryBuilder::new()
        .insert(
            "insert thingamajig",
            "INSERT INTO poc_products (name, price) VALUES ($1, $2)",
            &[&"thingamajig", &42],
        )
        .insert(
            "insert duplicate gadget",
            "INSERT INTO poc_products (name, price) VALUES ($1, $2)",
            &[&"gadget", &99],
        )
        .build();

    match BatchExecutor::execute(&mut client, failing_batch).await {
        Ok(()) => println!("failing batch succeeded (unexpected)"),
        Err(e) => println!("failing batch was rejected as expected: {e}"),
    }

    let row = client.query_one("SELECT count(*) FROM poc_products", &[]).await?;
    let count: i64 = row.get(0);
    println!("count after the failed batch: {count} (thingamajig was rolled back too)");

    Ok(())
}
