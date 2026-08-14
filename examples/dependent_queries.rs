use tokio_postgres::NoTls;

// The batching trick from main.rs (simple_query) doesn't work here: the whole
// batch is sent as one block of text upfront, so the client never sees the
// new row's id in time to splice it into the next statement. To keep it to
// one round trip, we fold both statements into a single query using a
// data-modifying CTE — the INSERT's RETURNING feeds straight into the next
// INSERT via SELECT, all inside Postgres, in one query() call.
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
            "DROP TABLE IF EXISTS poc_order_events;
             DROP TABLE IF EXISTS poc_items;
             CREATE TABLE poc_items (id SERIAL PRIMARY KEY, name TEXT, price INT);
             CREATE TABLE poc_order_events (
                 id SERIAL PRIMARY KEY,
                 item_id INT REFERENCES poc_items(id),
                 event_type TEXT
             );",
        )
        .await?;

    // insert into poc_items, then use its new id to insert into
    // poc_order_events - all in one statement, one round trip.
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

    let event_id: i32 = row.get("id");
    let item_id: i32 = row.get("item_id");
    let event_type: &str = row.get("event_type");
    println!("event {event_id} -> item {item_id} ({event_type})");

    Ok(())
}
