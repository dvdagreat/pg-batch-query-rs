import postgres, { type Row, type Sql } from 'postgres';

// sql.begin(sql => [...]) pipelines every returned query onto the wire in
// one round trip and wraps them in a real transaction - so unlike a plain
// sequential await loop, this batch keeps the single-round-trip property
// *and* gets real $1/$2 parameter binding (via sql.unsafe, since our SQL
// and params are built dynamically rather than as literal tagged templates),
// typed results via a mapper per statement, and atomic rollback on failure.

/** Maps one result row into a caller-defined type. */
export type RowMapper<T> = (row: Row) => T;

/**
 * A mutable box for out-parameters. Primitives are copied by value in JS, so
 * a plain `let count = 0` passed into a builder call can't be written back
 * to - wrapping it in a ref object gives the builder something it can
 * actually mutate and the caller can read afterward.
 */
export interface Ref<T> {
  value: T;
}

export function createRef<T>(initial: T): Ref<T> {
  return { value: initial };
}

interface SelectStatement<T = unknown> {
  kind: 'select';
  label: string;
  sql: string;
  params: readonly unknown[];
  mapRow: RowMapper<T>;
  // Select output uses plain array-reference semantics: `out` is the same
  // array the caller holds, so mutating it in place (not reassigning it) is
  // enough for the caller to see the results - no ref box needed here.
  out: T[];
}

interface MutationStatement {
  kind: 'mutation';
  label: string;
  sql: string;
  params: readonly unknown[];
  rowsAffected?: Ref<number>;
}

type Statement = SelectStatement<any> | MutationStatement;

/** Which statement in the batch failed, and why. */
export class BatchError extends Error {
  readonly statementIndex: number;
  readonly label: string;
  override readonly cause: unknown;

  constructor(statementIndex: number, label: string, cause: unknown) {
    super(BatchError.describe(statementIndex, label, cause));
    this.name = 'BatchError';
    this.statementIndex = statementIndex;
    this.label = label;
    this.cause = cause;
  }

  private static describe(statementIndex: number, label: string, cause: unknown): string {
    const detail = cause instanceof postgres.PostgresError ? cause.message : String(cause);
    return `statement ${statementIndex} (${label}) failed: ${detail}`;
  }
}

/**
 * Chain SELECTs, INSERTs, UPDATEs and DELETEs, in any order, into one
 * batch. Call `build()` to seal it, then hand it to `BatchExecutor.execute`.
 */
export class QueryBuilder {
  private readonly statements: Statement[] = [];

  /**
   * Runs `sql`, parses every returned row through `mapRow`, and stores them
   * in `out`. `out` is only written once the whole batch has committed.
   */
  select<T>(label: string, sql: string, params: readonly unknown[], mapRow: RowMapper<T>, out: T[]): this {
    this.statements.push({ kind: 'select', label, sql, params, mapRow, out });
    return this;
  }

  insert(label: string, sql: string, params: readonly unknown[] = []): this {
    return this.mutation(label, sql, params);
  }

  update(label: string, sql: string, params: readonly unknown[] = []): this {
    return this.mutation(label, sql, params);
  }

  delete(label: string, sql: string, params: readonly unknown[] = []): this {
    return this.mutation(label, sql, params);
  }

  /** Same as `insert`/`update`/`delete`, but also captures how many rows the statement touched. */
  mutationCapturing(label: string, sql: string, params: readonly unknown[], rowsAffected: Ref<number>): this {
    return this.mutation(label, sql, params, rowsAffected);
  }

  private mutation(label: string, sql: string, params: readonly unknown[], rowsAffected?: Ref<number>): this {
    this.statements.push({ kind: 'mutation', label, sql, params, rowsAffected });
    return this;
  }

  build(): QueryBatch {
    if (this.statements.length === 0) {
      throw new Error('cannot build an empty query batch');
    }
    return new QueryBatch(this.statements.slice());
  }
}

export class QueryBatch {
  constructor(readonly statements: readonly Statement[]) {}
}

/** Runs a `QueryBatch` inside one transaction, pipelined in a single round trip. */
export class BatchExecutor {
  static async execute(sql: Sql, batch: QueryBatch): Promise<void> {
    const { statements } = batch;

    let results: readonly (readonly Row[])[];
    try {
      results = await sql.begin((tx) =>
        statements.map((stmt, index) =>
          // unsafe() defaults to prepare: false, which - unlike literal
          // tagged-template queries - opts out of the pipelining sql.begin
          // otherwise gives an array of queries; prepare: true restores it.
          tx.unsafe(stmt.sql, stmt.params as any[], { prepare: true }).catch((cause: unknown) => {
            throw new BatchError(index, stmt.label, cause);
          }),
        ),
      );
    } catch (err) {
      console.error(`[batch] ${err instanceof Error ? err.message : String(err)} - rolled back`);
      throw err;
    }

    statements.forEach((stmt, index) => {
      const result = results[index]!;
      if (stmt.kind === 'select') {
        stmt.out.length = 0;
        for (const row of result) {
          stmt.out.push(stmt.mapRow(row));
        }
      } else if (stmt.rowsAffected) {
        stmt.rowsAffected.value = (result as unknown as { count: number }).count;
      }
    });
  }
}
