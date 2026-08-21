package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dvdagreat/batch-fetch-postgres/internal/db"
)

// Gotcha: when a multi-statement batch has more than one statement,
// Postgres wraps them in one implicit transaction. If a later statement
// fails, everything earlier in that same batch gets rolled back too - even
// successful inserts. This example triggers that failure on purpose and
// shows how to read the error in Go, then proves the rollback by
// re-checking the table.
func main() {
	ctx := context.Background()

	conn, err := pgx.Connect(ctx, db.ConnString)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx,
		`DROP TABLE IF EXISTS poc_accounts;
		 CREATE TABLE poc_accounts (id SERIAL PRIMARY KEY, email TEXT UNIQUE);`,
	)
	if err != nil {
		log.Fatal(err)
	}

	// Statement 1 succeeds, statement 2 violates the UNIQUE constraint,
	// statement 3 never runs at all.
	batch := `INSERT INTO poc_accounts (email) VALUES ('a@example.com');
	          INSERT INTO poc_accounts (email) VALUES ('a@example.com');
	          SELECT count(*) FROM poc_accounts;`

	_, err = conn.PgConn().Exec(ctx, batch).ReadAll()
	if err == nil {
		fmt.Println("batch succeeded (unexpected)")
	} else {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			fmt.Printf("batch failed: %s\n", pgErr.Message)
			if pgErr.Code == "23505" {
				fmt.Println("-> it was a unique violation, as expected")
			}
		} else {
			fmt.Printf("batch failed: %s\n", err)
		}
	}

	// The implicit transaction rolled back statement 1 as well, so the table
	// should still be empty even though the first insert looked fine on its own.
	var count int64
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM poc_accounts").Scan(&count); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("rows in poc_accounts after the failed batch: %d\n", count)
}
