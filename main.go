package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dvdagreat/batch-fetch-postgres/internal/db"
)

var statements = []string{
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
		     ('dave', 'dave@internal.test');`,
	)
	if err != nil {
		log.Fatal(err)
	}

	// 15 unrelated SELECTs, sent as one semicolon-separated string with no
	// parameter placeholders. Postgres' simple query protocol puts the whole
	// thing in a single message - one round trip for all fifteen - and
	// returns one result set per statement, read in order off the same
	// response.
	batch := strings.Join(statements, ";\n")

	if err := runBatch(ctx, conn, batch); err != nil {
		log.Fatal(err)
	}

	// Now put a number on it: batched vs. the same 15 queries sent one at a
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
		for _, stmt := range statements {
			if _, err := conn.Exec(ctx, stmt); err != nil {
				log.Fatal(err)
			}
		}
	}
	separateElapsed := time.Since(separateStart)

	fmt.Printf("\n--- timing over %d iterations, %d statements each ---\n", iterations, len(statements))
	fmt.Printf("batched (1 round trip):     %v  (%v/iter)\n", batchedElapsed, batchedElapsed/iterations)
	fmt.Printf("separate (%d round trips):  %v  (%v/iter)\n", len(statements), separateElapsed, separateElapsed/iterations)
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
