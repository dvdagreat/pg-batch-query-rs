use tokio_postgres::types::ToSql;
use tokio_postgres::{Client, Row};

// Unlike the simple_query POC in main.rs/examples, this builder runs each
// statement through the extended (parameterized) protocol and pipelines
// them by firing every future before awaiting any of them - tokio-postgres
// queues and flushes them together instead of waiting on each in turn. The
// whole batch runs inside one real transaction, so a failure anywhere rolls
// everything back and none of the output variables below get touched.

/// Maps one result row into a caller-defined type. Implement this for
/// whatever struct (or scalar) a SELECT in a batch should come back as.
pub trait FromRow: Sized {
    fn from_row(row: &Row) -> Result<Self, tokio_postgres::Error>;
}

impl FromRow for i64 {
    fn from_row(row: &Row) -> Result<Self, tokio_postgres::Error> {
        row.try_get(0)
    }
}

impl FromRow for i32 {
    fn from_row(row: &Row) -> Result<Self, tokio_postgres::Error> {
        row.try_get(0)
    }
}

/// Which statement in the batch failed, and why.
#[derive(Debug)]
pub struct BatchError {
    pub statement_index: usize,
    pub label: String,
    pub source: BatchErrorSource,
}

#[derive(Debug)]
pub enum BatchErrorSource {
    Db(tokio_postgres::Error),
    RowMapping(tokio_postgres::Error),
}

impl std::fmt::Display for BatchError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match &self.source {
            // tokio_postgres::Error's own Display just says "db error" - the
            // real message lives behind as_db_error().
            BatchErrorSource::Db(e) => {
                let detail = e
                    .as_db_error()
                    .map(|db_err| db_err.message().to_string())
                    .unwrap_or_else(|| e.to_string());
                write!(f, "statement {} ({}) failed: {detail}", self.statement_index, self.label)
            }
            BatchErrorSource::RowMapping(e) => write!(
                f,
                "statement {} ({}) returned a row that didn't match its target type: {e}",
                self.statement_index, self.label
            ),
        }
    }
}

impl std::error::Error for BatchError {}

enum Outcome<'a> {
    Rows(Box<dyn FnMut(Vec<Row>) -> Result<(), BatchErrorSource> + 'a>),
    Affected(Option<Box<dyn FnMut(u64) + 'a>>),
}

enum RawOutcome {
    Rows(Vec<Row>),
    Affected(u64),
}

struct Statement<'a> {
    label: String,
    sql: &'a str,
    params: &'a [&'a (dyn ToSql + Sync)],
    outcome: Outcome<'a>,
}

/// Chain SELECTs, INSERTs, UPDATEs and DELETEs, in any order, into one
/// batch. Call `build()` to seal it, then hand it to `BatchExecutor::execute`.
#[derive(Default)]
pub struct QueryBuilder<'a> {
    statements: Vec<Statement<'a>>,
}

impl<'a> QueryBuilder<'a> {
    pub fn new() -> Self {
        Self { statements: Vec::new() }
    }

    /// Runs `sql`, parses every returned row into `T`, and stores them in
    /// `out`. `out` is only written once the whole batch has committed.
    pub fn select<T: FromRow + 'a>(
        mut self,
        label: &str,
        sql: &'a str,
        params: &'a [&'a (dyn ToSql + Sync)],
        out: &'a mut Vec<T>,
    ) -> Self {
        let sink = move |rows: Vec<Row>| -> Result<(), BatchErrorSource> {
            out.clear();
            for row in &rows {
                out.push(T::from_row(row).map_err(BatchErrorSource::RowMapping)?);
            }
            Ok(())
        };
        self.statements.push(Statement {
            label: label.to_string(),
            sql,
            params,
            outcome: Outcome::Rows(Box::new(sink)),
        });
        self
    }

    pub fn insert(self, label: &str, sql: &'a str, params: &'a [&'a (dyn ToSql + Sync)]) -> Self {
        self.mutation(label, sql, params, None)
    }

    pub fn update(self, label: &str, sql: &'a str, params: &'a [&'a (dyn ToSql + Sync)]) -> Self {
        self.mutation(label, sql, params, None)
    }

    pub fn delete(self, label: &str, sql: &'a str, params: &'a [&'a (dyn ToSql + Sync)]) -> Self {
        self.mutation(label, sql, params, None)
    }

    /// Same as `insert`/`update`/`delete`, but also captures how many rows
    /// the statement touched into `rows_affected`.
    pub fn mutation_capturing(
        self,
        label: &str,
        sql: &'a str,
        params: &'a [&'a (dyn ToSql + Sync)],
        rows_affected: &'a mut u64,
    ) -> Self {
        self.mutation(label, sql, params, Some(rows_affected))
    }

    fn mutation(
        mut self,
        label: &str,
        sql: &'a str,
        params: &'a [&'a (dyn ToSql + Sync)],
        out: Option<&'a mut u64>,
    ) -> Self {
        let sink = out.map(|o| -> Box<dyn FnMut(u64) + 'a> { Box::new(move |n| *o = n) });
        self.statements.push(Statement {
            label: label.to_string(),
            sql,
            params,
            outcome: Outcome::Affected(sink),
        });
        self
    }

    pub fn build(self) -> QueryBatch<'a> {
        QueryBatch { statements: self.statements }
    }
}

pub struct QueryBatch<'a> {
    statements: Vec<Statement<'a>>,
}

/// Runs a `QueryBatch` inside one transaction, pipelining every statement
/// onto the connection instead of awaiting them one by one.
pub struct BatchExecutor;

impl BatchExecutor {
    pub async fn execute(client: &mut Client, batch: QueryBatch<'_>) -> Result<(), BatchError> {
        let mut statements = batch.statements;
        if statements.is_empty() {
            return Ok(());
        }

        let txn = client.transaction().await.map_err(|e| BatchError {
            statement_index: 0,
            label: "BEGIN".to_string(),
            source: BatchErrorSource::Db(e),
        })?;

        let futures = statements.iter().enumerate().map(|(index, stmt)| {
            let txn = &txn;
            async move {
                let outcome = match &stmt.outcome {
                    Outcome::Rows(_) => txn.query(stmt.sql, stmt.params).await.map(RawOutcome::Rows),
                    Outcome::Affected(_) => txn.execute(stmt.sql, stmt.params).await.map(RawOutcome::Affected),
                };
                outcome.map_err(|e| BatchError {
                    statement_index: index,
                    label: stmt.label.clone(),
                    source: BatchErrorSource::Db(e),
                })
            }
        });
        let mut results = futures_util::future::join_all(futures).await;

        if let Some(pos) = results.iter().position(Result::is_err) {
            let err = match results.swap_remove(pos) {
                Err(e) => e,
                Ok(_) => unreachable!(),
            };
            eprintln!("[batch] {err} - rolling back");
            if let Err(rollback_err) = txn.rollback().await {
                eprintln!("[batch] rollback also failed: {rollback_err}");
            }
            return Err(err);
        }

        for (index, (stmt, result)) in statements.iter_mut().zip(results.into_iter()).enumerate() {
            let raw = match result {
                Ok(raw) => raw,
                Err(_) => unreachable!("already checked for errors above"),
            };
            let mapped = match (&mut stmt.outcome, raw) {
                (Outcome::Rows(sink), RawOutcome::Rows(rows)) => sink(rows),
                (Outcome::Affected(Some(sink)), RawOutcome::Affected(n)) => {
                    sink(n);
                    Ok(())
                }
                (Outcome::Affected(None), RawOutcome::Affected(_)) => Ok(()),
                _ => unreachable!("outcome kind always matches how the statement was executed"),
            };
            if let Err(source) = mapped {
                let err = BatchError { statement_index: index, label: stmt.label.clone(), source };
                eprintln!("[batch] {err} - rolling back");
                if let Err(rollback_err) = txn.rollback().await {
                    eprintln!("[batch] rollback also failed: {rollback_err}");
                }
                return Err(err);
            }
        }

        txn.commit().await.map_err(|e| BatchError {
            statement_index: statements.len(),
            label: "COMMIT".to_string(),
            source: BatchErrorSource::Db(e),
        })
    }
}
