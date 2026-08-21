package main

import (
	"context"
	"fmt"
	"log"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/dvdagreat/batch-fetch-postgres/internal/db"
)

// A multi-statement batch hands back one untyped result set per statement.
// This example maps each statement's rows into a type of our own, so
// callers get the same typed results they'd get from a normal parameterized
// query - just mapped once, in one place, instead of everywhere the batch
// is used.

type ItemCount struct {
	Count int64
}

type ExpensiveItem struct {
	Name string
}

type ServerTime struct {
	Now string
}

func main() {
	ctx := context.Background()

	conn, err := pgx.Connect(ctx, db.ConnString)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx,
		`DROP TABLE IF EXISTS poc_items CASCADE;
		 CREATE TABLE poc_items (id SERIAL PRIMARY KEY, name TEXT, price INT);
		 INSERT INTO poc_items (name, price) VALUES ('widget', 10), ('gadget', 25), ('gizmo', 5);`,
	)
	if err != nil {
		log.Fatal(err)
	}

	batch := `SELECT count(*) FROM poc_items;
	          SELECT name FROM poc_items WHERE price > 10;
	          SELECT now();`

	mrr := conn.PgConn().Exec(ctx, batch)
	defer mrr.Close()

	var counts []ItemCount
	var expensiveItems []ExpensiveItem
	var serverTimes []ServerTime

	statementIndex := 0
	for mrr.NextResult() {
		result := mrr.ResultReader()
		for result.NextRow() {
			values := result.Values()
			switch statementIndex {
			case 0:
				n, _ := strconv.ParseInt(string(values[0]), 10, 64)
				counts = append(counts, ItemCount{Count: n})
			case 1:
				expensiveItems = append(expensiveItems, ExpensiveItem{Name: string(values[0])})
			case 2:
				serverTimes = append(serverTimes, ServerTime{Now: string(values[0])})
			}
		}
		if _, err := result.Close(); err != nil {
			log.Fatal(err)
		}
		statementIndex++
	}
	if err := mrr.Close(); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("item count: %d\n", counts[0].Count)
	for _, item := range expensiveItems {
		fmt.Printf("expensive item: %s\n", item.Name)
	}
	fmt.Printf("server time: %s\n", serverTimes[0].Now)
}
