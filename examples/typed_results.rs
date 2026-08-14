use tokio_postgres::{NoTls, SimpleQueryMessage, SimpleQueryRow};

// simple_query hands back an untyped stream of text rows. This example wraps
// each statement's rows into a struct of our own, so callers get the same
// typed results they'd get from client.query() - just parsed by hand once,
// in one place, instead of everywhere the batch is used.

struct ItemCount {
    count: i64,
}

impl ItemCount {
    fn from_row(row: &SimpleQueryRow) -> Self {
        let count = row.get(0).unwrap_or("0").parse().unwrap_or(0);
        Self { count }
    }
}

struct ExpensiveItem {
    name: String,
}

impl ExpensiveItem {
    fn from_row(row: &SimpleQueryRow) -> Self {
        Self {
            name: row.get(0).unwrap_or("").to_string(),
        }
    }
}

#[derive(Debug)]
struct ServerTime {
    now: String,
}


impl ServerTime {
    fn from_row(row: &SimpleQueryRow) -> Self {
        Self {
            now: row.get(0).unwrap_or("").to_string(),
        }
    }
}

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

    let batch = "SELECT count(*) FROM poc_items;
                 SELECT name FROM poc_items WHERE price > 10;
                 SELECT now();";

    let messages = client.simple_query(batch).await?;

    // Statement order is known ahead of time, so each row just gets routed
    // to the struct that matches its statement.
    let mut statement_idx = 0;
    let mut counts: Vec<ItemCount> = Vec::new();
    let mut expensive_items: Vec<ExpensiveItem> = Vec::new();
    let mut server_times: Vec<ServerTime> = Vec::new();

    for msg in messages {
        match msg {
            SimpleQueryMessage::Row(row) => match statement_idx {
                0 => counts.push(ItemCount::from_row(&row)),
                1 => expensive_items.push(ExpensiveItem::from_row(&row)),
                2 => server_times.push(ServerTime::from_row(&row)),
                _ => {}
            },
            SimpleQueryMessage::CommandComplete(_) => statement_idx += 1,
            _ => {}
        }
    }

    println!("item count: {}", counts[0].count);
    for item in &expensive_items {
        println!("expensive item: {}", item.name);
    }
    println!("server time: {}", server_times[0].now);

    Ok(())
}
