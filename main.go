package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dvdagreat/batch-fetch-postgres/internal/db"
)

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

	// Three unrelated SELECTs, sent as one semicolon-separated string with no
	// parameter placeholders. Postgres' simple query protocol puts the whole
	// thing in a single message - one round trip for all three - and returns
	// one result set per statement, read in order off the same response.
	batch := `SELECT count(*) FROM poc_items;
	          SELECT name FROM poc_items WHERE price > 10;
	          SELECT now();`

	if err := runBatch(ctx, conn, batch); err != nil {
		log.Fatal(err)
	}

	// Now put a number on it: batched vs. the same 3 queries sent one at a
	// time, each awaited before the next fires.
	const iterations = 200

	batchedStart := time.Now()
	for i := 0; i < iterations; i++ {
		mrr := conn.PgConn().Exec(ctx, batch)
		if err := mrr.Close(); err != nil {
			log.Fatal(err)
		}
	}
	batchedElapsed := time.Since(batchedStart)

	separateStart := time.Now()
	for i := 0; i < iterations; i++ {
		if _, err := conn.Exec(ctx, "SELECT count(*) FROM poc_items"); err != nil {
			log.Fatal(err)
		}
		if _, err := conn.Exec(ctx, "SELECT name FROM poc_items WHERE price > 10"); err != nil {
			log.Fatal(err)
		}
		if _, err := conn.Exec(ctx, "SELECT now()"); err != nil {
			log.Fatal(err)
		}
	}
	separateElapsed := time.Since(separateStart)

	fmt.Printf("\n--- timing over %d iterations ---\n", iterations)
	fmt.Printf("batched (1 round trip):    %v  (%v/iter)\n", batchedElapsed, batchedElapsed/iterations)
	fmt.Printf("separate (3 round trips):  %v  (%v/iter)\n", separateElapsed, separateElapsed/iterations)
}

// runBatch sends a semicolon-separated batch of statements as one simple-query
// message and prints each statement's rows as they come back.
func runBatch(ctx context.Context, conn *pgx.Conn, sql string) error {
	mrr := conn.PgConn().Exec(ctx, sql)
	defer mrr.Close()

	statementIndex := 0
	for mrr.NextResult() {
		result := mrr.ResultReader()
		for result.NextRow() {
			values := make([]string, len(result.Values()))
			for i, v := range result.Values() {
				values[i] = string(v)
			}
			fmt.Printf("statement %d -> %v\n", statementIndex, values)
		}
		if _, err := result.Close(); err != nil {
			return err
		}
		statementIndex++
	}
	return mrr.Close()
}
