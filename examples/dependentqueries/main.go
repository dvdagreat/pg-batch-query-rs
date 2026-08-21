package main

import (
	"context"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5"

	"github.com/dvdagreat/batch-fetch-postgres/internal/db"
)

// The multi-statement batching trick from main.go doesn't work here: the
// whole batch is sent as one block of text upfront, so the client never
// sees the new row's id in time to splice it into the next statement. To
// keep it to one round trip, fold both statements into a single
// parameterized query using a data-modifying CTE - the INSERT's RETURNING
// feeds straight into the next INSERT via SELECT, all inside Postgres.
func main() {
	ctx := context.Background()

	conn, err := pgx.Connect(ctx, db.ConnString)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx,
		`DROP TABLE IF EXISTS poc_order_events;
		 DROP TABLE IF EXISTS poc_items;
		 CREATE TABLE poc_items (id SERIAL PRIMARY KEY, name TEXT, price INT);
		 CREATE TABLE poc_order_events (
		     id SERIAL PRIMARY KEY,
		     item_id INT REFERENCES poc_items(id),
		     event_type TEXT
		 );`,
	)
	if err != nil {
		log.Fatal(err)
	}

	// Insert into poc_items, then use its new id to insert into
	// poc_order_events - all in one statement, one round trip.
	var eventID, itemID int32
	var eventType string
	err = conn.QueryRow(ctx,
		`WITH new_item AS (
		     INSERT INTO poc_items (name, price) VALUES ($1, $2)
		     RETURNING id
		 )
		 INSERT INTO poc_order_events (item_id, event_type)
		 SELECT id, 'created' FROM new_item
		 RETURNING id, item_id, event_type`,
		"thingamajig", 42,
	).Scan(&eventID, &itemID, &eventType)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("event %d -> item %d (%s)\n", eventID, itemID, eventType)
}
