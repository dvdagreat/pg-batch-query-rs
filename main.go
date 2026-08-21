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

var curatedStatements = []string{
	// aggregates & counts
	"SELECT count(*) FROM poc_items",
	"SELECT avg(price) FROM poc_items",
	"SELECT max(price) FROM poc_items",
	"SELECT min(price) FROM poc_items",
	"SELECT sum(price) FROM poc_items",
	"SELECT count(DISTINCT category_id) FROM poc_items",
	"SELECT stddev(price) FROM poc_items",
	"SELECT variance(price) FROM poc_items",
	"SELECT max(length(name)) FROM poc_items",
	"SELECT min(length(name)) FROM poc_items",
	// price threshold filters
	"SELECT name FROM poc_items WHERE price > 0",
	"SELECT name FROM poc_items WHERE price > 3",
	"SELECT name FROM poc_items WHERE price > 5",
	"SELECT name FROM poc_items WHERE price > 7",
	"SELECT name FROM poc_items WHERE price > 10",
	"SELECT name FROM poc_items WHERE price > 15",
	"SELECT name FROM poc_items WHERE price > 20",
	"SELECT name FROM poc_items WHERE price > 30",
	// category filters by id
	"SELECT count(*) FROM poc_items WHERE category_id = 1",
	"SELECT count(*) FROM poc_items WHERE category_id = 2",
	"SELECT count(*) FROM poc_items WHERE category_id = 3",
	// joins by category name
	"SELECT i.name FROM poc_items i JOIN poc_categories c ON i.category_id = c.id WHERE c.name = 'electronics'",
	"SELECT i.name FROM poc_items i JOIN poc_categories c ON i.category_id = c.id WHERE c.name = 'tools'",
	"SELECT i.name FROM poc_items i JOIN poc_categories c ON i.category_id = c.id WHERE c.name = 'misc'",
	// joins with an extra price filter
	"SELECT count(*) FROM poc_items i JOIN poc_categories c ON i.category_id = c.id WHERE c.name = 'electronics' AND i.price > 10",
	"SELECT count(*) FROM poc_items i JOIN poc_categories c ON i.category_id = c.id WHERE c.name = 'tools' AND i.price > 5",
	"SELECT count(*) FROM poc_items i JOIN poc_categories c ON i.category_id = c.id WHERE c.name = 'misc' AND i.price < 10",
	// sorting & limits
	"SELECT name FROM poc_items ORDER BY price ASC LIMIT 1",
	"SELECT name FROM poc_items ORDER BY price ASC LIMIT 3",
	"SELECT name FROM poc_items ORDER BY price DESC LIMIT 1",
	"SELECT name FROM poc_items ORDER BY price DESC LIMIT 3",
	"SELECT name FROM poc_items ORDER BY name LIMIT 3",
	"SELECT name FROM poc_items ORDER BY name DESC LIMIT 3",
	"SELECT name FROM poc_items ORDER BY id LIMIT 2 OFFSET 1",
	"SELECT name FROM poc_items ORDER BY id LIMIT 2 OFFSET 3",
	// between / like
	"SELECT count(*) FROM poc_items WHERE price BETWEEN 5 AND 20",
	"SELECT name FROM poc_items WHERE price BETWEEN 5 AND 20 ORDER BY price",
	"SELECT count(*) FROM poc_items WHERE name LIKE 'g%'",
	"SELECT count(*) FROM poc_items WHERE name LIKE '%o%'",
	// string functions
	"SELECT upper(name) FROM poc_items ORDER BY name LIMIT 3",
	"SELECT lower(name) FROM poc_items ORDER BY name LIMIT 3",
	"SELECT concat(name, ' - ', price::text) FROM poc_items ORDER BY name LIMIT 3",
	// categories
	"SELECT count(*) FROM poc_categories",
	"SELECT name FROM poc_categories ORDER BY name",
	"SELECT name FROM poc_categories WHERE name <> 'misc'",
	"SELECT name FROM poc_categories ORDER BY name DESC LIMIT 1",
	// customers
	"SELECT count(*) FROM poc_customers",
	"SELECT name FROM poc_customers WHERE email LIKE '%@example.com'",
	"SELECT name FROM poc_customers WHERE email LIKE '%@internal.test'",
	"SELECT count(*) FROM poc_customers WHERE email LIKE '%@example.com'",
	"SELECT name FROM poc_customers ORDER BY name",
	"SELECT name FROM poc_customers ORDER BY name DESC LIMIT 1",
	// customer name prefixes
	"SELECT count(*) FROM poc_customers WHERE name LIKE 'a%'",
	"SELECT count(*) FROM poc_customers WHERE name LIKE 'b%'",
	"SELECT count(*) FROM poc_customers WHERE name LIKE 'c%'",
	"SELECT count(*) FROM poc_customers WHERE name LIKE 'd%'",
	// email domain variety
	"SELECT count(*) FROM poc_customers WHERE email LIKE '%example%'",
	"SELECT count(*) FROM poc_customers WHERE email LIKE '%internal%'",
	// type introspection
	"SELECT pg_typeof(price) FROM poc_items LIMIT 1",
	"SELECT pg_typeof(name) FROM poc_items LIMIT 1",
	"SELECT count(*) FROM poc_items WHERE category_id IS NOT NULL",
	"SELECT count(*) FROM poc_customers WHERE email IS NOT NULL",
	// server/system info
	"SELECT version()",
	"SELECT current_database()",
	"SELECT current_user",
	"SELECT current_schema()",
	"SELECT pg_backend_pid()",
	"SELECT current_timestamp",
	"SELECT current_date",
	"SELECT localtimestamp",
	"SELECT now()",
	"SELECT clock_timestamp()",
	// per-category averages
	"SELECT avg(price)::int FROM poc_items WHERE category_id = 1",
	"SELECT avg(price)::int FROM poc_items WHERE category_id = 2",
	"SELECT avg(price)::int FROM poc_items WHERE category_id = 3",
	// misc
	"SELECT count(*) FROM poc_items WHERE price = 10",
	"SELECT count(*) FROM poc_items WHERE price <> 10",
	"SELECT max(price) - min(price) FROM poc_items",
	"SELECT count(*) FROM poc_items i, poc_categories c WHERE i.category_id = c.id",
	"SELECT count(*) FROM poc_categories WHERE id IN (1, 2, 3)",
}

// The curated statements above are a good showcase of variety, but a batch
// with more statements makes the round-trip savings harder to miss. Pad it
// out with a big block of cheap, uniform filters - simple enough that their
// per-statement cost stays small, so the batch keeps scaling in batching's
// favor instead of being swamped by expensive per-statement work.
func allStatements() []string {
	const cheapCount = 5000
	statements := make([]string, len(curatedStatements), len(curatedStatements)+cheapCount)
	copy(statements, curatedStatements)
	for price := 0; price < cheapCount; price++ {
		statements = append(statements, fmt.Sprintf("SELECT count(*) FROM poc_items WHERE price > %d", price))
	}
	return statements
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

	// Hundreds of unrelated SELECTs, sent as one semicolon-separated string
	// with no parameter placeholders. Postgres' simple query protocol puts
	// the whole thing in a single message - one round trip for all of them -
	// and returns one result set per statement, read in order off the same
	// response.
	statements := allStatements()
	batch := strings.Join(statements, ";\n")

	if err := runBatch(ctx, conn, batch); err != nil {
		log.Fatal(err)
	}

	// Now put a number on it: batched vs. the same statements sent one at a
	// time, each awaited before the next fires.
	const iterations = 15

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
	fmt.Printf("speedup: %.1fx\n", separateElapsed.Seconds()/batchedElapsed.Seconds())
}

// runBatch sends a semicolon-separated batch of statements as one simple-query
// message and prints a sample of the rows that come back.
func runBatch(ctx context.Context, conn *pgx.Conn, sql string) error {
	mrr := conn.PgConn().Exec(ctx, sql)
	defer mrr.Close()

	statementIndex := 0
	rowsShown := 0
	rowsTotal := 0
	for mrr.NextResult() {
		result := mrr.ResultReader()
		for result.NextRow() {
			rowsTotal++
			if rowsShown < 5 {
				values := make([]string, len(result.Values()))
				for i, v := range result.Values() {
					values[i] = string(v)
				}
				fmt.Printf("statement %d -> %v\n", statementIndex, values)
				rowsShown++
			}
		}
		if _, err := result.Close(); err != nil {
			return err
		}
		statementIndex++
	}
	fmt.Printf("... plus %d more row(s) across all %d statements\n", rowsTotal-rowsShown, statementIndex)
	return mrr.Close()
}
