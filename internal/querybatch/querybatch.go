// Package querybatch chains SELECTs, INSERTs, UPDATEs and DELETEs into one
// pgx.Batch, pipelining every statement onto the connection in a single
// round trip via SendBatch, and runs the whole thing inside one transaction.
package querybatch

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// RowMapper turns one result row into a caller-defined type T. It's an alias
// for pgx's own RowToFunc so pgx.RowTo[T], pgx.RowToStructByPos[T], etc. can
// be passed straight in.
type RowMapper[T any] = pgx.RowToFunc[T]

type statementKind int

const (
	kindSelect statementKind = iota
	kindMutation
)

type statement struct {
	kind  statementKind
	label string
	sql   string
	args  []any

	// set for kindSelect: consumes the rows pgx.Batch handed back for this
	// statement and writes the mapped results into the caller's slice.
	collect func(rows pgx.Rows) error

	// set for kindMutation when the caller wants the affected row count.
	rowsAffected *int64
}

// Builder chains statements, in any order, into a Batch. Unlike a fluent
// method chain, Select is a free function (Go doesn't allow a method to
// introduce its own type parameter), so it's called with the builder as its
// first argument instead of as b.Select(...).
type Builder struct {
	statements []statement
}

func NewBuilder() *Builder {
	return &Builder{}
}

// Select runs sql and maps every returned row through mapRow into out. out
// is only written once the whole batch has committed.
func Select[T any](b *Builder, label, sql string, args []any, mapRow RowMapper[T], out *[]T) *Builder {
	b.statements = append(b.statements, statement{
		kind:  kindSelect,
		label: label,
		sql:   sql,
		args:  args,
		collect: func(rows pgx.Rows) error {
			items, err := pgx.CollectRows(rows, mapRow)
			if err != nil {
				return err
			}
			*out = items
			return nil
		},
	})
	return b
}

func (b *Builder) Insert(label, sql string, args ...any) *Builder {
	return b.mutation(label, sql, args, nil)
}

func (b *Builder) Update(label, sql string, args ...any) *Builder {
	return b.mutation(label, sql, args, nil)
}

func (b *Builder) Delete(label, sql string, args ...any) *Builder {
	return b.mutation(label, sql, args, nil)
}

// MutationCapturing is the same as Insert/Update/Delete, but also captures
// how many rows the statement touched into rowsAffected.
func (b *Builder) MutationCapturing(label, sql string, rowsAffected *int64, args ...any) *Builder {
	return b.mutation(label, sql, args, rowsAffected)
}

func (b *Builder) mutation(label, sql string, args []any, rowsAffected *int64) *Builder {
	b.statements = append(b.statements, statement{
		kind:         kindMutation,
		label:        label,
		sql:          sql,
		args:         args,
		rowsAffected: rowsAffected,
	})
	return b
}

// Batch is a sealed, ready-to-run set of statements.
type Batch struct {
	statements []statement
}

func (b *Builder) Build() (*Batch, error) {
	if len(b.statements) == 0 {
		return nil, errors.New("cannot build an empty query batch")
	}
	return &Batch{statements: append([]statement(nil), b.statements...)}, nil
}

// Error identifies which statement in a batch failed and why.
type Error struct {
	StatementIndex int
	Label          string
	Err            error
}

func (e *Error) Error() string {
	return fmt.Sprintf("statement %d (%s) failed: %s", e.StatementIndex, e.Label, describe(e.Err))
}

func (e *Error) Unwrap() error {
	return e.Err
}

// describe prefers a raw Postgres error's message - pgconn.PgError's own
// Error() prepends "ERROR: " and a SQLSTATE code that just adds noise here.
func describe(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Message
	}
	return err.Error()
}

// Execute runs batch inside one transaction, statement by statement, over a
// single pipelined round trip. If anything fails, the whole batch is rolled
// back and none of the Select/MutationCapturing output variables are touched.
func Execute(ctx context.Context, conn *pgx.Conn, batch *Batch) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return &Error{StatementIndex: 0, Label: "BEGIN", Err: err}
	}

	pgBatch := &pgx.Batch{}
	for _, stmt := range batch.statements {
		pgBatch.Queue(stmt.sql, stmt.args...)
	}
	results := tx.SendBatch(ctx, pgBatch)

	var batchErr *Error
	for i, stmt := range batch.statements {
		if err := readStatementResult(results, stmt); err != nil && batchErr == nil {
			batchErr = &Error{StatementIndex: i, Label: stmt.label, Err: err}
		}
	}
	if err := results.Close(); err != nil && batchErr == nil {
		batchErr = &Error{StatementIndex: len(batch.statements), Label: "batch", Err: err}
	}

	if batchErr != nil {
		log.Printf("[batch] %s - rolling back", batchErr)
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			log.Printf("[batch] rollback also failed: %s", rbErr)
		}
		return batchErr
	}

	if err := tx.Commit(ctx); err != nil {
		return &Error{StatementIndex: len(batch.statements), Label: "COMMIT", Err: err}
	}
	return nil
}

func readStatementResult(results pgx.BatchResults, stmt statement) error {
	if stmt.kind == kindSelect {
		rows, err := results.Query()
		if err != nil {
			return err
		}
		return stmt.collect(rows)
	}

	tag, err := results.Exec()
	if err != nil {
		return err
	}
	if stmt.rowsAffected != nil {
		*stmt.rowsAffected = tag.RowsAffected()
	}
	return nil
}
