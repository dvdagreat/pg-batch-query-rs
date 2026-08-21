import postgres from 'postgres';

export function createSql(): postgres.Sql {
  return postgres({
    host: 'localhost',
    username: 'postgres',
    password: 'password',
    database: 'postgres',
    // Silence routine NOTICEs (e.g. from DROP ... CASCADE) - the examples
    // reuse tables across runs and this is expected, not worth logging.
    onnotice: () => {},
  });
}
