package main

import (
	"context"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5"

	"github.com/dvdagreat/batch-fetch-postgres/internal/db"
	"github.com/dvdagreat/batch-fetch-postgres/internal/querybatch"
)

type Product struct {
	ID    int32
	Name  string
	Price int32
}

func main() {
	ctx := context.Background()

	conn, err := pgx.Connect(ctx, db.ConnString)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx,
		`DROP TABLE IF EXISTS poc_products;
		 CREATE TABLE poc_products (id SERIAL PRIMARY KEY, name TEXT UNIQUE, price INT);`,
	)
	if err != nil {
		log.Fatal(err)
	}

	// A batch that mixes every statement kind, in whatever order the
	// problem calls for, on one builder.
	var allProducts []Product
	var productCount []int64
	var rowsDeleted int64

	b := querybatch.NewBuilder()
	b.Insert("insert widget", "INSERT INTO poc_products (name, price) VALUES ($1, $2)", "widget", 10)
	b.Insert("insert gadget", "INSERT INTO poc_products (name, price) VALUES ($1, $2)", "gadget", 25)
	b.Insert("insert gizmo", "INSERT INTO poc_products (name, price) VALUES ($1, $2)", "gizmo", 5)
	b.Update("bump gadget price", "UPDATE poc_products SET price = price + 5 WHERE name = $1", "gadget")
	b.MutationCapturing("delete cheap products", "DELETE FROM poc_products WHERE price < $1", &rowsDeleted, 10)
	querybatch.Select(b, "all products", "SELECT id, name, price FROM poc_products ORDER BY id", nil,
		pgx.RowToStructByPos[Product], &allProducts)
	querybatch.Select(b, "product count", "SELECT count(*) FROM poc_products", nil,
		pgx.RowTo[int64], &productCount)

	batch, err := b.Build()
	if err != nil {
		log.Fatal(err)
	}

	if err := querybatch.Execute(ctx, conn, batch); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("deleted %d cheap product(s)\n", rowsDeleted)
	for _, p := range allProducts {
		fmt.Printf("product %d: %s ($%d)\n", p.ID, p.Name, p.Price)
	}
	fmt.Printf("count query says: %d\n", productCount[0])

	// Now prove the transaction actually protects you: this batch inserts a
	// valid row, then a duplicate that violates the UNIQUE constraint.
	failingBuilder := querybatch.NewBuilder()
	failingBuilder.Insert("insert thingamajig", "INSERT INTO poc_products (name, price) VALUES ($1, $2)", "thingamajig", 42)
	failingBuilder.Insert("insert duplicate gadget", "INSERT INTO poc_products (name, price) VALUES ($1, $2)", "gadget", 99)
	failingBatch, err := failingBuilder.Build()
	if err != nil {
		log.Fatal(err)
	}

	if err := querybatch.Execute(ctx, conn, failingBatch); err != nil {
		fmt.Printf("failing batch was rejected as expected: %s\n", err)
	} else {
		fmt.Println("failing batch succeeded (unexpected)")
	}

	var count int64
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM poc_products").Scan(&count); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("count after the failed batch: %d (thingamajig was rolled back too)\n", count)
}
